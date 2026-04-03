package h264video

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"h264-video",
	gst.DebugColorNone,
	"h264-video Element",
)

type H264Video struct {
	H264Depay    *gst.Element
	H264Parse    *gst.Element
	H264Dec      *gst.Element
	VideoConvert *gst.Element
	VideoScale   *gst.Element
	VideoRate    *gst.Element
	Filter       *gst.Element
}

func (e *H264Video) New() glib.GoObjectSubclass {
	return &H264Video{}
}

func (e *H264Video) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"H264 to Video Decoder",
		"Video/Decoder",
		"Decodes H264 RTP to raw video",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)H264"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("video/x-raw"),
	))
}

func (e *H264Video) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.H264Depay, err = gst.NewElementWithProperties("rtph264depay", map[string]interface{}{
		"request-keyframe":  true,
		"wait-for-keyframe": false,
	})
	if err != nil {
		self.Error("Failed to create rtph264depay element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtph264depay element: %v", err))
		return
	}

	e.H264Parse, err = gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		self.Error("Failed to create h264parse element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264parse element: %v", err))
		return
	}

	e.H264Dec, err = gst.NewElementWithProperties("avdec_h264", map[string]interface{}{
		"max-threads": int(4),
	})
	if err != nil {
		self.Error("Failed to create avdec_h264 element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create avdec_h264 element: %v", err))
		return
	}

	e.VideoConvert, err = gst.NewElementWithProperties("videoconvert", map[string]interface{}{})
	if err != nil {
		self.Error("Failed to create videoconvert element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoconvert element: %v", err))
		return
	}

	e.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true,
	})
	if err != nil {
		self.Error("Failed to create videoscale element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoscale element: %v", err))
		return
	}

	e.VideoRate, err = gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only":     false,
		"skip-to-first": true,
	})
	if err != nil {
		self.Error("Failed to create videorate element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videorate element: %v", err))
		return
	}

	e.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1,framerate=24/1"),
	})
	if err != nil {
		self.Error("Failed to create capsfilter element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element: %v", err))
		return
	}

	if err := self.AddMany(
		e.H264Depay,
		e.H264Parse,
		e.H264Dec,
		e.VideoConvert,
		e.VideoScale,
		e.VideoRate,
		e.Filter,
	); err != nil {
		self.Error("Failed to add elements to bin", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		return
	}

	if err := gst.ElementLinkMany(
		e.H264Depay,
		e.H264Parse,
		e.H264Dec,
		e.VideoConvert,
		e.VideoScale,
		e.VideoRate,
		e.Filter,
	); err != nil {
		self.Error("Failed to link elements", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.H264Depay.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Filter.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *H264Video) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.H264Depay = nil
		e.H264Parse = nil
		e.H264Dec = nil
		e.VideoConvert = nil
		e.VideoScale = nil
		e.VideoRate = nil
		e.Filter = nil
	}

	return ret
}
