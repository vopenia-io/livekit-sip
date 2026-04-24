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
	Level      *gst.Element
	OpusEnc    *gst.Element
	RtpOpusPay *gst.Element
	RtpFilter  *gst.Element
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
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
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

	e.Level, err = gst.NewElementWithProperties("level", map[string]interface{}{
		"audio-level-meta": true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create level element: %v", err))
		self.Error("Failed to create level element", err)
		return
	}

	e.OpusEnc, err = gst.NewElementWithProperties("opusenc", map[string]interface{}{
		"frame-size": int(2), // 2.5ms
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create opusenc element: %v", err))
		self.Error("Failed to create opusenc element", err)
		return
	}

	e.RtpOpusPay, err = gst.NewElementWithProperties("rtpopuspay", map[string]interface{}{
		"pt": 111,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpopuspay element: %v", err))
		self.Error("Failed to create rtpopuspay element", err)
		return
	}

	e.RtpFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)48000, encoding-name=(string)OPUS, extmap-1=(string)< \"\", urn:ietf:params:rtp-hdrext:ssrc-audio-level, \"vad=on\" >"),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element: %v", err))
		self.Error("Failed to create capsfilter element", err)
		return
	}

	self.AddMany(
		e.Level,
		e.OpusEnc,
		e.RtpOpusPay,
		e.RtpFilter,
	)

	if err := gst.ElementLinkMany(
		e.Level,
		e.OpusEnc,
		e.RtpOpusPay,
		e.RtpFilter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.Level.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpFilter.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *AudioOpus) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing AudioOpus element")

	e.Level = nil
	e.OpusEnc = nil
	e.RtpOpusPay = nil
	e.RtpFilter = nil
}
