package vp8h264

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"vp8-h264",
	gst.DebugColorNone,
	"vp8-h264 Element",
)

var properties = []*glib.ParamSpec{
	glib.NewBoxedParam(
		"h264-caps",
		"H264 Caps",
		"The caps of H264 RTP stream",
		gst.TypeCaps,
		glib.ParameterWritable,
	),
}

type Vp8H264 struct {
	self *gst.Bin

	Vp8Depay   *gst.Element
	Vp8Dec     *gst.Element
	VideoScale *gst.Element
	VideoRate  *gst.Element
	Filter     *gst.Element
	X264Enc    *gst.Element
	H264Parse  *gst.Element
	RtpH264Pay *gst.Element
	H264Caps   *gst.Element
}

func (h *Vp8H264) New() glib.GoObjectSubclass {
	return &Vp8H264{}
}

func (h *Vp8H264) ClassInit(klass *glib.ObjectClass) {
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
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)VP8"),
	))

	// 2. Src Pad Template (Output: VP8)
	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)H264"),
	))

	class.InstallProperties(properties)
}

func (h *Vp8H264) InstanceInit(self *glib.Object) {
	h.self = gst.ToGstBin(self)
	var err error

	h.Vp8Depay, err = gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{})
	if err != nil {
		h.self.Error("Failed to create rtpvp8depay element", err)
		h.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpvp8depay element: %v", err))
		return
	}

	h.Vp8Dec, err = gst.NewElementWithProperties("vp8dec", map[string]interface{}{})
	if err != nil {
		h.self.Error("Failed to create vp8dec element", err)
		h.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp8dec element: %v", err))
		return
	}

	h.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		h.self.Error("Failed to create videoscale element", err)
		h.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoscale element: %v", err))
		return
	}

	h.VideoRate, err = gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true,
	})
	if err != nil {
		h.self.Error("Failed to create videorate element", err)
		h.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videorate element: %v", err))
		return
	}

	h.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1,framerate=24/1"),
	})
	if err != nil {
		h.self.Error("Failed to create capsfilter element", err)
		h.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element: %v", err))
		return
	}

	h.X264Enc, err = gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"bitrate":          uint(2000),
		"speed-preset":     int(1),
		"tune":             uint(4),
		"key-int-max":      uint(12),
		"bframes":          uint(0),
		"vbv-buf-capacity": uint(2000),
	})
	if err != nil {
		h.self.Error("Failed to create x264enc element", err)
		h.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create x264enc element: %v", err))
		return
	}

	h.H264Parse, err = gst.NewElementWithProperties("h264parse", map[string]interface{}{})
	if err != nil {
		h.self.Error("Failed to create h264parse element", err)
		h.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264parse element: %v", err))
		return
	}

	h.RtpH264Pay, err = gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"mtu":             int(1200),
		"config-interval": int(1),
		"aggregate-mode":  int(1),
	})
	if err != nil {
		h.self.Error("Failed to create rtph264pay element", err)
		h.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtph264pay element: %v", err))
		return
	}

	h.H264Caps, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)H264"),
	})
	if err != nil {
		h.self.Error("Failed to create H264 capsfilter element", err)
		h.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create H264 capsfilter element: %v", err))
		return
	}

	// Add all elements to the bin
	h.self.AddMany(
		h.Vp8Depay,
		h.Vp8Dec,
		h.VideoScale,
		h.VideoRate,
		h.Filter,
		h.X264Enc,
		h.H264Parse,
		h.RtpH264Pay,
		h.H264Caps,
	)

	// Link the elements together
	if err := gst.ElementLinkMany(
		h.Vp8Depay,
		h.Vp8Dec,
		h.VideoScale,
		h.VideoRate,
		h.Filter,
		h.X264Enc,
		h.H264Parse,
		h.RtpH264Pay,
		h.H264Caps,
	); err != nil {
		h.self.Error("Failed to link elements", err)
		h.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		return
	}

	elemClass := gst.ToElementClass(h.self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", h.Vp8Depay.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	h.self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", h.H264Caps.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	h.self.AddPad(ghostSrc.Pad)
}

func (h *Vp8H264) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "h264-caps":
		val, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting caps property value: %v", err))
			return
		}
		caps, ok := val.(*gst.Caps)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for caps property")
			return
		}
		if caps == nil {
			self.Log(CAT, gst.LevelError, "Nil caps provided")
			return
		}
		if err := h.H264Caps.SetProperty("caps", caps.Copy().Ref()); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set caps property: %v", err))
		}
	}
}
