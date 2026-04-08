package audiopcmu

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"audio-pcmu",
	gst.DebugColorNone,
	"audio-pcmu Element",
)

type AudioPcmu struct {
	MuLawEnc   *gst.Element
	RtpPcmuPay *gst.Element
}

func (e *AudioPcmu) New() glib.GoObjectSubclass {
	return &AudioPcmu{}
}

func (e *AudioPcmu) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Audio to PCMU Encoder",
		"Audio/Encoder",
		"Encodes raw audio to PCMU RTP",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	// PCMU RTP (RFC 3551) is fixed at 8kHz mono. Hardcode the constraint so
	// upstream audioconvert/audioresample negotiate to the only format that
	// can actually flow through the mulawenc → rtppcmupay chain.
	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("audio/x-raw, format=(string)S16LE, layout=(string)interleaved, rate=(int)8000, channels=(int)1"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)PCMU"),
	))
}

func (e *AudioPcmu) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.MuLawEnc, err = gst.NewElementWithProperties("mulawenc", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create mulawenc element: %v", err))
		self.Error("Failed to create mulawenc element", err)
		return
	}

	e.RtpPcmuPay, err = gst.NewElementWithProperties("rtppcmupay", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtppcmupay element: %v", err))
		self.Error("Failed to create rtppcmupay element", err)
		return
	}

	if err := self.AddMany(
		e.MuLawEnc,
		e.RtpPcmuPay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.MuLawEnc,
		e.RtpPcmuPay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.MuLawEnc.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpPcmuPay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *AudioPcmu) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing AudioPCMU element")

	e.MuLawEnc = nil
	e.RtpPcmuPay = nil
}
