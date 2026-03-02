package h264vp8

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"h264-vp8",
	gst.DebugColorNone,
	"h264-vp8 Element",
)

type H264Vp8 struct {
	H264Depay    *gst.Element
	H264Parse    *gst.Element
	H264Dec      *gst.Element
	VideoConvert *gst.Element
	VideoScale   *gst.Element
	VideoRate    *gst.Element
	Filter       *gst.Element
	Vp8Enc       *gst.Element
	Vp8Pay       *gst.Element
}

func (h *H264Vp8) New() glib.GoObjectSubclass {
	return &H264Vp8{}
}

func (h *H264Vp8) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"H264 to VP8 Transcoder",
		"Video/Converter",
		"Decodes H264, scales, and encodes to VP8",
		"Your Name <you@example.com>",
	)

	// 1. Sink Pad Template (Input: H264)
	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)H264"),
	))

	// 2. Src Pad Template (Output: VP8)
	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)VP8"),
	))
}

func (h *H264Vp8) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	h.H264Depay, err = gst.NewElementWithProperties("rtph264depay", map[string]interface{}{})
	if err != nil {
		self.Error("Failed to create rtph264depay element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtph264depay element: %v", err))
		return
	}

	h.H264Parse, err = gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		self.Error("Failed to create h264parse element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264parse element: %v", err))
		return
	}

	h.H264Dec, err = gst.NewElementWithProperties("avdec_h264", map[string]interface{}{
		"max-threads": int(4),
	})
	if err != nil {
		self.Error("Failed to create avdec_h264 element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create avdec_h264 element: %v", err))
		return
	}

	h.VideoConvert, err = gst.NewElementWithProperties("videoconvert", map[string]interface{}{})
	if err != nil {
		self.Error("Failed to create videoconvert element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoconvert element: %v", err))
		return
	}

	h.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		self.Error("Failed to create videoscale element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoscale element: %v", err))
		return
	}

	h.VideoRate, err = gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only":     false,
		"skip-to-first": true,
	})
	if err != nil {
		self.Error("Failed to create videorate element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videorate element: %v", err))
		return
	}

	h.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1,framerate=24/1"),
	})
	if err != nil {
		self.Error("Failed to create capsfilter element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element: %v", err))
		return
	}

	h.Vp8Enc, err = gst.NewElementWithProperties("vp8enc", map[string]interface{}{
		"deadline":            int(1),
		"target-bitrate":      int(2_000_000),
		"cpu-used":            int(8),
		"keyframe-max-dist":   int(12),
		"lag-in-frames":       int(0),
		"threads":             int(4),
		"buffer-initial-size": int(100),
		"buffer-optimal-size": int(150),
		"buffer-size":         int(200),
		"min-quantizer":       int(4),
		"max-quantizer":       int(32),
		"cq-level":            int(10),
		"error-resilient":     int(1),
		"end-usage":           int(1),
	})
	if err != nil {
		self.Error("Failed to create vp8enc element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp8enc element: %v", err))
		return
	}

	h.Vp8Pay, err = gst.NewElementWithProperties("rtpvp8pay", map[string]interface{}{
		"pt":              int(96),
		"mtu":             int(1200),
		"picture-id-mode": int(2),
	})
	if err != nil {
		self.Error("Failed to create rtpvp8pay element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpvp8pay element: %v", err))
		return
	}

	// Add all elements to the bin
	if err := self.AddMany(
		h.H264Depay,
		h.H264Parse,
		h.H264Dec,
		h.VideoConvert,
		h.VideoScale,
		h.VideoRate,
		h.Filter,
		h.Vp8Enc,
		h.Vp8Pay,
	); err != nil {
		self.Error("Failed to add elements to bin", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		return
	}

	// Link the elements together
	if err := gst.ElementLinkMany(
		h.H264Depay,
		h.H264Parse,
		h.H264Dec,
		h.VideoConvert,
		h.VideoScale,
		h.VideoRate,
		h.Filter,
		h.Vp8Enc,
		h.Vp8Pay,
	); err != nil {
		self.Error("Failed to link elements", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		return
	}

	class := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", h.H264Depay.GetStaticPad("sink"), class.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", h.Vp8Pay.GetStaticPad("src"), class.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (h *H264Vp8) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		h.H264Depay = nil
		h.H264Parse = nil
		h.H264Dec = nil
		h.VideoConvert = nil
		h.VideoScale = nil
		h.VideoRate = nil
		h.Filter = nil
		h.Vp8Enc = nil
		h.Vp8Pay = nil
	}

	return ret
}
