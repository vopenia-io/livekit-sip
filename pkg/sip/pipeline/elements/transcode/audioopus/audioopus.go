package audioopus

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"audio-opus",
	gst.DebugColorNone,
	"audio-opus Element",
)

type AudioOpus struct {
	OpusEnc    *gst.Element
	RtpOpusPay *gst.Element
}

func (e *AudioOpus) New() glib.GoObjectSubclass {
	return &AudioOpus{}
}

func (e *AudioOpus) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Audio to Opus Encoder",
		"Audio/Encoder",
		"Encodes raw audio to Opus RTP",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("audio/x-raw"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)48000, encoding-name=(string)OPUS"),
	))
}

func (e *AudioOpus) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.OpusEnc, err = gst.NewElementWithProperties("opusenc", map[string]interface{}{
		"frame-size": int(2), // 2.5ms
	})
	if err != nil {
		self.Error("Failed to create opusenc element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create opusenc element: %v", err))
		return
	}

	e.RtpOpusPay, err = gst.NewElementWithProperties("rtpopuspay", map[string]interface{}{
		"pt": 111,
	})
	if err != nil {
		self.Error("Failed to create rtpopuspay element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpopuspay element: %v", err))
		return
	}

	self.AddMany(
		e.OpusEnc,
		e.RtpOpusPay,
	)

	if err := gst.ElementLinkMany(
		e.OpusEnc,
		e.RtpOpusPay,
	); err != nil {
		self.Error("Failed to link elements", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.OpusEnc.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpOpusPay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *AudioOpus) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.OpusEnc = nil
		e.RtpOpusPay = nil
	}
	return ret
}
