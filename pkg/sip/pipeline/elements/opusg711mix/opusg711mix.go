package opusg711mix

import (
	"fmt"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"opus_g711_mix",
	gst.DebugColorNone,
	"opus_g711_mix Element",
)

type branch struct {
	GhostPad      *gst.GhostPad
	RtpOpusDepay  *gst.Element
	OpusDec       *gst.Element
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	AudioRate     *gst.Element
}

type OpusG711Mix struct {
	Branches   []*branch
	AudioMixer *gst.Element
	G711Enc    *gst.Element
	RtpG711Pay *gst.Element
	Identity   *gst.Element
}

func (e *OpusG711Mix) New() glib.GoObjectSubclass {
	return &OpusG711Mix{}
}

func (e *OpusG711Mix) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Opus to G711 Transcoder/Muxer",
		"Audio/Mixer/Converter",
		"Decodes Opus, resamples, and encodes to G711",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)48000, encoding-name=(string)OPUS"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string){ PCMU, PCMA }"),
	))
}

func (e *OpusG711Mix) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.AudioMixer, err = gst.NewElement("audiomixer")
	if err != nil {
		self.Error("Failed to create audiomixer element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audiomixer element: %v", err))
		return
	}
	// e.AudioMixer.GetStaticPad("src").AddProbe(gst.PadProbeTypeBlockDownstream, func(p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	// 	return e.G711Setup(self, p, info) // TODO: do that cause any leaks?
	// })

	e.Identity, err = gst.NewElement("identity")
	if err != nil {
		self.Error("Failed to create identity element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create identity element: %v", err))
		return
	}

	if err := self.AddMany(
		e.AudioMixer,
		e.Identity,
	); err != nil {
		self.Error("Failed to add elements to OpusG711Mix bin", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to OpusG711Mix bin: %v", err))
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Identity.GetStaticPad("src"), elemClass.GetPadTemplate("src"))

	eweak := weak.Make(e)
	ghostSrc.SetLinkFunction(func(self *gst.Pad, parent *gst.Object, peer *gst.Pad) gst.PadLinkReturn {
		ptr := eweak.Value()
		if ptr == nil {
			return gst.PadLinkRefused
		}
		return e.SrcLinkFunction(self, parent, peer)
	})

	self.AddPad(ghostSrc.Pad)
}

func (e *OpusG711Mix) SrcLinkFunction(pad *gst.Pad, parent *gst.Object, peer *gst.Pad) gst.PadLinkReturn {
	self := gst.ToGstBin(parent)

	self.Log(CAT, gst.LevelDebug, "G711Setup called")

	pcmuCaps := gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)PCMU")
	pcmaCaps := gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)PCMA")

	if peer.QueryAcceptCaps(pcmuCaps) {
		self.Log(CAT, gst.LevelInfo, "Peer accepts PCMU caps, setting up PCMU elements")
		if err := e.setupPCMU(); err != nil {
			self.Error("Failed to setup PCMU elements", err)
			return gst.PadLinkRefused
		}
	} else if peer.QueryAcceptCaps(pcmaCaps) {
		self.Log(CAT, gst.LevelInfo, "Peer accepts PCMA caps, setting up PCMA elements")
		if err := e.setupPCMA(); err != nil {
			self.Error("Failed to setup PCMA elements", err)
			return gst.PadLinkRefused
		}
	} else {
		self.Log(CAT, gst.LevelWarning, "Peer does not accept PCMU or PCMA caps")
		return gst.PadLinkNoFormat
	}

	if err := e.setupCodec(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to setup codec elements: %v", err))
		self.Error("Failed to setup codec elements", err)
		return gst.PadLinkRefused
	}

	self.Log(CAT, gst.LevelInfo, "Successfully set up codec elements")
	return gst.PadLinkOK
}

func (e *OpusG711Mix) setupPCMU() (err error) {
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

func (e *OpusG711Mix) setupPCMA() (err error) {
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

func (e *OpusG711Mix) setupCodec(self *gst.Bin) (err error) {
	if err := self.AddMany(
		e.G711Enc,
		e.RtpG711Pay,
	); err != nil {
		return fmt.Errorf("failed to add G711 elements: %w", err)
	}

	if err := gst.ElementLinkMany(
		e.AudioMixer,
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

func (e *OpusG711Mix) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.Branches = nil
		e.AudioMixer = nil
		e.G711Enc = nil
		e.RtpG711Pay = nil
		e.Identity = nil
	}
	return ret
}

func (e *OpusG711Mix) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("RequestNewPad called with name: %s", name))
	pad, err := e.AddBranch(self)
	if err != nil {
		self.Error("Failed to add branch for new pad", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add branch for new pad: %v", err))
		return nil
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added branch for new pad: %s", pad.GetName()))
	return pad
}

func (e *OpusG711Mix) AddBranch(self *gst.Bin) (pad *gst.Pad, err error) {
	b := &branch{}

	b.RtpOpusDepay, err = gst.NewElement("rtpopusdepay")
	if err != nil {
		self.Error("Failed to create rtpopusdepay element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpopusdepay element: %v", err))
		return
	}

	b.OpusDec, err = gst.NewElement("opusdec")
	if err != nil {
		self.Error("Failed to create opusdec element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create opusdec element: %v", err))
		return
	}

	b.AudioConvert, err = gst.NewElement("audioconvert")
	if err != nil {
		self.Error("Failed to create audioconvert element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioconvert element: %v", err))
		return
	}

	b.AudioResample, err = gst.NewElement("audioresample")
	if err != nil {
		self.Error("Failed to create audioresample element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioresample element: %v", err))
		return
	}

	b.AudioRate, err = gst.NewElement("audiorate")
	if err != nil {
		self.Error("Failed to create audiorate element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audiorate element: %v", err))
		return
	}

	if err = self.AddMany(
		b.RtpOpusDepay,
		b.OpusDec,
		b.AudioConvert,
		b.AudioResample,
		b.AudioRate,
	); err != nil {
		self.Error("Failed to add elements to bin", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		return
	}

	if err = gst.ElementLinkMany(
		b.RtpOpusDepay,
		b.OpusDec,
		b.AudioConvert,
		b.AudioResample,
		b.AudioRate,
	); err != nil {
		self.Error("Failed to link elements", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		return
	}

	sinkPad := e.AudioMixer.GetRequestPad("sink_%u")
	if sinkPad == nil {
		self.Log(CAT, gst.LevelError, "Failed to get request pad from audiomixer element")
		return nil, fmt.Errorf("failed to get request pad from audiomixer element")
	}
	if ret := b.AudioRate.GetStaticPad("src").Link(sinkPad); ret != gst.PadLinkOK {
		self.Error("Failed to link audio rate to mixer", fmt.Errorf("pad link failed with return code: %v", ret))
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link audio rate to mixer: pad link failed with return code: %v", ret))
		err = fmt.Errorf("failed to link audio rate to mixer: pad link failed with return code: %v", ret)
		return
	}

	for _, elem := range []*gst.Element{b.RtpOpusDepay, b.OpusDec, b.AudioConvert, b.AudioResample, b.AudioRate} {
		if !elem.SyncStateWithParent() {
			self.Error(fmt.Sprintf("Failed to sync state for element %s", elem.GetName()), nil)
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to sync state for element %s", elem.GetName()))
			err = fmt.Errorf("failed to sync state for element %s", elem.GetName())
			return
		}
	}

	class := gst.ToElementClass(self.Class())

	b.GhostPad = gst.NewGhostPadFromTemplate(fmt.Sprintf("sink_%d", len(e.Branches)), b.RtpOpusDepay.GetStaticPad("sink"), class.GetPadTemplate("sink_%u"))
	self.AddPad(b.GhostPad.Pad)

	if !b.GhostPad.SetActive(true) {
		self.Error("Failed to set ghost pad active", nil)
		self.Log(CAT, gst.LevelError, "Failed to set ghost pad active")
		return nil, fmt.Errorf("failed to set ghost pad active")
	}

	e.Branches = append(e.Branches, b)
	return b.GhostPad.Pad, nil
}
