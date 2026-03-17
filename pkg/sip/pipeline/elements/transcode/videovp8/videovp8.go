package videovp8

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"video-vp8",
	gst.DebugColorNone,
	"video-vp8 Element",
)

type VideoVp8 struct {
	Vp8Enc *gst.Element
	Vp8Pay *gst.Element
}

func (e *VideoVp8) New() glib.GoObjectSubclass {
	return &VideoVp8{}
}

func (e *VideoVp8) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Video to VP8 Encoder",
		"Video/Encoder",
		"Encodes raw video to VP8 RTP",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("video/x-raw"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)VP8"),
	))
}

func (e *VideoVp8) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.Vp8Enc, err = gst.NewElementWithProperties("vp8enc", map[string]interface{}{
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
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp8enc element: %v", err))
		self.Error("Failed to create vp8enc element", err)
		return
	}

	e.Vp8Pay, err = gst.NewElementWithProperties("rtpvp8pay", map[string]interface{}{
		"pt":              int(96),
		"mtu":             int(1200),
		"picture-id-mode": int(2),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpvp8pay element: %v", err))
		self.Error("Failed to create rtpvp8pay element", err)
		return
	}

	self.AddMany(
		e.Vp8Enc,
		e.Vp8Pay,
	)

	if err := gst.ElementLinkMany(
		e.Vp8Enc,
		e.Vp8Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.Vp8Enc.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Vp8Pay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *VideoVp8) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.Vp8Enc = nil
		e.Vp8Pay = nil
	}
	return ret
}
