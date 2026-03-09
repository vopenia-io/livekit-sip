package audiog711

import (
	"fmt"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"audio-g711",
	gst.DebugColorNone,
	"audio-g711 Element",
)

type AudioG711 struct {
	G711Enc    *gst.Element
	RtpG711Pay *gst.Element
	Identity   *gst.Element
	GhostSrc   *gst.GhostPad
}

func (e *AudioG711) New() glib.GoObjectSubclass {
	return &AudioG711{}
}

func (e *AudioG711) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Audio to G711 Encoder",
		"Audio/Encoder",
		"Encodes raw audio to G711 RTP",
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
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string){ PCMU, PCMA }"),
	))
}

func (e *AudioG711) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.Identity, err = gst.NewElement("identity")
	if err != nil {
		self.Error("Failed to create identity element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create identity element: %v", err))
		return
	}

	if err := self.AddMany(
		e.Identity,
	); err != nil {
		self.Error("Failed to add identity element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add identity element: %v", err))
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.Identity.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	if !self.AddPad(ghostSink.Pad) {
		self.Error("Failed to add ghost sink pad", fmt.Errorf("failed to add ghost sink pad"))
		self.Log(CAT, gst.LevelError, "Failed to add ghost sink pad")
		return
	}

	ghostSrc := gst.NewGhostPadNoTargetFromTemplate("src", elemClass.GetPadTemplate("src"))

	eWeak := weak.Make(e)
	ghostSrc.Pad.SetLinkFunction(func(pad *gst.Pad, parent *gst.Object, peer *gst.Pad) gst.PadLinkReturn {
		e := eWeak.Value()
		if e == nil {
			return gst.PadLinkRefused
		}
		return e.SrcLinkFunction(pad, parent, peer)
	})

	if !self.AddPad(ghostSrc.Pad) {
		self.Error("Failed to add ghost src pad", fmt.Errorf("failed to add ghost src pad"))
		self.Log(CAT, gst.LevelError, "Failed to add ghost src pad")
		return
	}

	e.GhostSrc = ghostSrc
}

func (e *AudioG711) SrcLinkFunction(pad *gst.Pad, parent *gst.Object, peer *gst.Pad) gst.PadLinkReturn {
	self := gst.ToGstBin(parent)

	self.Log(CAT, gst.LevelDebug, "SrcLinkFunction called")

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

func (e *AudioG711) setupPCMU() (err error) {
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

func (e *AudioG711) setupPCMA() (err error) {
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

func (e *AudioG711) setupCodec(self *gst.Bin) (err error) {
	if err := self.AddMany(
		e.G711Enc,
		e.RtpG711Pay,
	); err != nil {
		return fmt.Errorf("failed to add G711 elements: %w", err)
	}

	if err := gst.ElementLinkMany(
		e.Identity,
		e.G711Enc,
		e.RtpG711Pay,
	); err != nil {
		return fmt.Errorf("failed to link G711 elements: %w", err)
	}

	for _, elem := range []*gst.Element{e.G711Enc, e.RtpG711Pay} {
		if !elem.SyncStateWithParent() {
			return fmt.Errorf("failed to sync state for element %s", elem.GetName())
		}
	}

	e.GhostSrc.SetTarget(e.RtpG711Pay.GetStaticPad("src"))

	return nil
}

func (e *AudioG711) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.G711Enc = nil
		e.RtpG711Pay = nil
		e.Identity = nil
		e.GhostSrc = nil
	}
	return ret
}
