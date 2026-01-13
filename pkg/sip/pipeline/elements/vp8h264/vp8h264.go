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

type Vp8H264 struct {
	self *gst.Bin

	Vp8Depay   *gst.Element
	Vp8Dec     *gst.Element
	VideoScale *gst.Element
	VideoRate  *gst.Element
	Filter     *gst.Element
	Queue      *gst.Element
	X264Enc    *gst.Element
	H264Parse  *gst.Element
	RtpH264Pay *gst.Element
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
	); err != nil {
		h.self.Error("Failed to link elements", err)
		h.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		return
	}

	// h.X264Enc.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	// 	buffer := info.GetBuffer()
	// 	if buffer == nil {
	// 		return gst.PadProbeOK
	// 	}

	// 	// Check if the timestamp is near the 1000h offset (sanity check)
	// 	// 3600 seconds * 1000 = 3,600,000 seconds
	// 	if buffer.PresentationTimestamp() > gst.ClockTime(time.Hour*1000) {
	// 		// Subtract 1000 hours
	// 		newPts := buffer.PresentationTimestamp() - gst.ClockTime(time.Hour*1000)
	// 		newDts := buffer.DecodingTimestamp() - gst.ClockTime(time.Hour*1000)

	// 		buffer.SetPresentationTimestamp(newPts)
	// 		buffer.SetDecodingTimestamp(newDts)
	// 	}

	// 	return gst.PadProbeOK
	// })

	elemClass := gst.ToElementClass(h.self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", h.Vp8Depay.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	h.self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", h.RtpH264Pay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	h.self.AddPad(ghostSrc.Pad)
}
