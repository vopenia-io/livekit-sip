package opusg711

import (
	"fmt"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"opus-g711",
	gst.DebugColorNone,
	"opus-g711 Element",
)

type OpusG711 struct {
	RtpOpusDepay  *gst.Element
	OpusDec       *gst.Element
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	AudioRate     *gst.Element
	G711Enc       *gst.Element
	RtpG711Pay    *gst.Element
	Identity      *gst.Element
}

func (e *OpusG711) New() glib.GoObjectSubclass {
	return &OpusG711{}
}

func (e *OpusG711) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Opus to G711 Transcoder",
		"Audio/Converter",
		"Decodes Opus, resamples, and encodes to G711",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)48000, encoding-name=(string)OPUS"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string){ PCMU, PCMA }"),
	))
}

func (e *OpusG711) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.RtpOpusDepay, err = gst.NewElement("rtpopusdepay")
	if err != nil {
		self.Error("Failed to create rtpopusdepay element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpopusdepay element: %v", err))
		return
	}

	e.OpusDec, err = gst.NewElement("opusdec")
	if err != nil {
		self.Error("Failed to create opusdec element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create opusdec element: %v", err))
		return
	}

	e.AudioConvert, err = gst.NewElement("audioconvert")
	if err != nil {
		self.Error("Failed to create audioconvert element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioconvert element: %v", err))
		return
	}

	e.AudioResample, err = gst.NewElement("audioresample")
	if err != nil {
		self.Error("Failed to create audioresample element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioresample element: %v", err))
		return
	}

	e.AudioRate, err = gst.NewElement("audiorate")
	if err != nil {
		self.Error("Failed to create audiorate element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audiorate element: %v", err))
		return
	}
	eWeak := weak.Make(e)
	e.AudioRate.GetStaticPad("src").AddProbe(gst.PadProbeTypeBlockDownstream, func(p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		e := eWeak.Value()
		if e == nil {
			return gst.PadProbeRemove
		}
		return e.G711Setup(self, p, info) // TODO: do that cause any leaks?
	})

	// e.MuLawEnc, err = gst.NewElement("mulawenc")
	// if err != nil {
	// 	self.Error("Failed to create mulawenc element", err)
	// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create mulawenc element: %v", err))
	// 	return
	// }

	// e.RtpPcmuPay, err = gst.NewElement("rtppcmupay")
	// if err != nil {
	// 	self.Error("Failed to create rtppcmupay element", err)
	// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtppcmupay element: %v", err))
	// 	return
	// }

	e.Identity, err = gst.NewElement("identity")
	if err != nil {
		self.Error("Failed to create identity element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create identity element: %v", err))
		return
	}

	self.AddMany(
		e.RtpOpusDepay,
		e.OpusDec,
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
		e.Identity,
	)

	if err := gst.ElementLinkMany(
		e.RtpOpusDepay,
		e.OpusDec,
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
	); err != nil {
		self.Error("Failed to link elements", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.RtpOpusDepay.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Identity.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *OpusG711) setupPCMU(self *gst.Bin, p *gst.Pad) (err error) {
	e.G711Enc, err = gst.NewElement("mulawenc")
	if err != nil {
		return fmt.Errorf("failed to create mulawenc element: %w", err)
	}

	e.RtpG711Pay, err = gst.NewElement("rtppcmupay")
	if err != nil {
		return fmt.Errorf("failed to create rtppcmupay element: %w", err)
	}

	return nil
}

func (e *OpusG711) setupPCMA(self *gst.Bin, p *gst.Pad) (err error) {
	e.G711Enc, err = gst.NewElement("alawenc")
	if err != nil {
		return fmt.Errorf("failed to create alawenc element: %w", err)
	}

	e.RtpG711Pay, err = gst.NewElement("rtppcmapay")
	if err != nil {
		return fmt.Errorf("failed to create rtppcmapay element: %w", err)
	}

	return nil
}

func (e *OpusG711) setupCodec(self *gst.Bin) (err error) {
	if err := self.AddMany(
		e.G711Enc,
		e.RtpG711Pay,
	); err != nil {
		return fmt.Errorf("failed to add G711 elements: %w", err)
	}

	if err := gst.ElementLinkMany(
		e.AudioRate,
		e.G711Enc,
		e.RtpG711Pay,
		e.Identity,
	); err != nil {
		return fmt.Errorf("failed to link G711 elements: %w", err)
	}

	for _, elem := range []*gst.Element{e.RtpG711Pay, e.G711Enc} {
		if !elem.SyncStateWithParent() {
			return fmt.Errorf("failed to sync state for element %s", elem.GetName())
		}
	}

	return nil
}

func (e *OpusG711) G711Setup(self *gst.Bin, p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	self.Log(CAT, gst.LevelDebug, "G711Setup called")
	src := self.GetStaticPad("src")
	peer := src.GetPeer()
	if peer == nil {
		self.Log(CAT, gst.LevelError, "Identity src pad has no peer")
		return gst.PadProbeOK
	}

	pcmuCaps := gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)PCMU")
	pcmaCaps := gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)PCMA")

	if peer.QueryAcceptCaps(pcmuCaps) {
		self.Log(CAT, gst.LevelInfo, "Peer accepts PCMU caps, setting up PCMU elements")
		if err := e.setupPCMU(self, p); err != nil {
			self.Error("Failed to setup PCMU elements", err)
			return gst.PadProbeRemove
		}
	} else if peer.QueryAcceptCaps(pcmaCaps) {
		self.Log(CAT, gst.LevelInfo, "Peer accepts PCMA caps, setting up PCMA elements")
		if err := e.setupPCMA(self, p); err != nil {
			self.Error("Failed to setup PCMA elements", err)
			return gst.PadProbeRemove
		}
	} else {
		self.Log(CAT, gst.LevelWarning, "Peer does not accept PCMU or PCMA caps")
		return gst.PadProbeRemove
	}

	if err := e.setupCodec(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to setup codec elements: %v", err))
		self.Error("Failed to setup codec elements", err)
		return gst.PadProbeRemove
	}

	self.Log(CAT, gst.LevelInfo, "Successfully set up codec elements")
	return gst.PadProbeRemove
}

func (e *OpusG711) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.RtpOpusDepay = nil
		e.OpusDec = nil
		e.AudioConvert = nil
		e.AudioResample = nil
		e.AudioRate = nil
		e.G711Enc = nil
		e.RtpG711Pay = nil
		e.Identity = nil
	}
	return ret
}
