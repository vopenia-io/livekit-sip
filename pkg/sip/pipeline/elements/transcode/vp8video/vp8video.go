package vp8video

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"vp8-video",
	gst.DebugColorNone,
	"vp8-video Element",
)

type Vp8Video struct {
	Vp8Depay   *gst.Element
	Vp8Dec     *gst.Element
	VideoScale *gst.Element
	VideoRate  *gst.Element
	Filter     *gst.Element
}

func (e *Vp8Video) New() glib.GoObjectSubclass {
	return &Vp8Video{}
}

func (e *Vp8Video) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"VP8 to Video Decoder",
		"Video/Decoder",
		"Decodes VP8 RTP to raw video",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)VP8"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("video/x-raw"),
	))
}

func (e *Vp8Video) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.Vp8Depay, err = gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe":  true,
		"wait-for-keyframe": false,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpvp8depay element: %v", err))
		self.Error("Failed to create rtpvp8depay element", err)
		return
	}

	e.Vp8Dec, err = gst.NewElementWithProperties("vp8dec", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp8dec element: %v", err))
		self.Error("Failed to create vp8dec element", err)
		return
	}

	e.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoscale element: %v", err))
		self.Error("Failed to create videoscale element", err)
		return
	}

	e.VideoRate, err = gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videorate element: %v", err))
		self.Error("Failed to create videorate element", err)
		return
	}

	e.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1,framerate=24/1"),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element: %v", err))
		self.Error("Failed to create capsfilter element", err)
		return
	}

	self.AddMany(
		e.Vp8Depay,
		e.Vp8Dec,
		e.VideoScale,
		e.VideoRate,
		e.Filter,
	)

	if err := gst.ElementLinkMany(
		e.Vp8Depay,
		e.Vp8Dec,
		e.VideoScale,
		e.VideoRate,
		e.Filter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.Vp8Depay.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Filter.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *Vp8Video) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.Vp8Depay = nil
		e.Vp8Dec = nil
		e.VideoScale = nil
		e.VideoRate = nil
		e.Filter = nil
	}

	return ret
}
