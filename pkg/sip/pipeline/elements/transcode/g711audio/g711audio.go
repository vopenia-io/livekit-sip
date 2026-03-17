package g711audio

import (
	"fmt"
	"strings"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"g711-audio",
	gst.DebugColorNone,
	"g711-audio Element",
)

type G711Audio struct {
	Identity      *gst.Element
	RtpG711Depay  *gst.Element
	G711Dec       *gst.Element
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	AudioRate     *gst.Element
}

func (e *G711Audio) New() glib.GoObjectSubclass {
	return &G711Audio{}
}

func (e *G711Audio) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"G711 to Audio Decoder",
		"Audio/Decoder",
		"Decodes G711 RTP to raw audio",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string){ PCMU, PCMA }"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("audio/x-raw"),
	))
}

func (e *G711Audio) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.Identity, err = gst.NewElement("identity")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create identity element: %v", err))
		self.Error("Failed to create identity element", err)
		return
	}
	eWeak := weak.Make(e)
	e.Identity.GetStaticPad("sink").AddProbe(gst.PadProbeTypeEventDownstream, func(p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		e := eWeak.Value()
		if e == nil {
			return gst.PadProbeRemove
		}
		return e.G711Setup(self, p, info)
	})

	e.AudioConvert, err = gst.NewElement("audioconvert")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioconvert element: %v", err))
		self.Error("Failed to create audioconvert element", err)
		return
	}

	e.AudioResample, err = gst.NewElement("audioresample")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioresample element: %v", err))
		self.Error("Failed to create audioresample element", err)
		return
	}

	e.AudioRate, err = gst.NewElement("audiorate")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audiorate element: %v", err))
		self.Error("Failed to create audiorate element", err)
		return
	}

	self.AddMany(
		e.Identity,
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
	)

	if err := gst.ElementLinkMany(
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.Identity.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.AudioRate.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *G711Audio) setupPCMU() (err error) {
	e.RtpG711Depay, err = gst.NewElement("rtppcmudepay")
	if err != nil {
		return fmt.Errorf("failed to create rtppcmudepay element: %w", err)
	}

	e.G711Dec, err = gst.NewElement("mulawdec")
	if err != nil {
		return fmt.Errorf("failed to create mulawdec element: %w", err)
	}

	return nil
}

func (e *G711Audio) setupPCMA() (err error) {
	e.RtpG711Depay, err = gst.NewElement("rtppcmadepay")
	if err != nil {
		return fmt.Errorf("failed to create rtppcmadepay element: %w", err)
	}

	e.G711Dec, err = gst.NewElement("alawdec")
	if err != nil {
		return fmt.Errorf("failed to create alawdec element: %w", err)
	}

	return nil
}

func (e *G711Audio) setupCodec(self *gst.Bin) (err error) {
	if err := self.AddMany(
		e.RtpG711Depay,
		e.G711Dec,
	); err != nil {
		return fmt.Errorf("failed to add G711 elements: %w", err)
	}

	if err := gst.ElementLinkMany(
		e.Identity,
		e.RtpG711Depay,
		e.G711Dec,
		e.AudioConvert,
	); err != nil {
		return fmt.Errorf("failed to link G711 elements: %w", err)
	}

	for _, elem := range []*gst.Element{e.RtpG711Depay, e.G711Dec} {
		if !elem.SyncStateWithParent() {
			return fmt.Errorf("failed to sync state for element %s", elem.GetName())
		}
	}

	return nil
}

func (e *G711Audio) G711Setup(self *gst.Bin, p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	event := info.GetEvent()
	if event == nil || event.Type() != gst.EventTypeCaps {
		return gst.PadProbePass
	}

	caps := event.ParseCaps()
	for i := range caps.GetSize() {
		structure := caps.GetStructureAt(i)
		obj, err := structure.GetValue("encoding-name")
		if err != nil {
			continue
		}
		encodingName, ok := obj.(string)
		if !ok {
			continue
		}

		switch strings.ToUpper(encodingName) {
		case "PCMU":
			if err := e.setupPCMU(); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to setup PCMU elements: %v", err))
				self.Error("Failed to setup PCMU elements", err)
				return gst.PadProbeRemove
			}
		case "PCMA":
			if err := e.setupPCMA(); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to setup PCMA elements: %v", err))
				self.Error("Failed to setup PCMA elements", err)
				return gst.PadProbeRemove
			}
		default:
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unsupported encoding: %s", encodingName))
			continue
		}
		if err := e.setupCodec(self); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to setup codec elements: %v", err))
			self.Error("Failed to setup codec elements", err)
			return gst.PadProbeRemove
		}
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully set up codec for encoding: %s", encodingName))
		return gst.PadProbeRemove
	}
	self.Log(CAT, gst.LevelWarning, "No valid encoding-name found in caps")
	return gst.PadProbePass
}

func (e *G711Audio) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.Identity = nil
		e.RtpG711Depay = nil
		e.G711Dec = nil
		e.AudioConvert = nil
		e.AudioResample = nil
		e.AudioRate = nil
	}
	return ret
}
