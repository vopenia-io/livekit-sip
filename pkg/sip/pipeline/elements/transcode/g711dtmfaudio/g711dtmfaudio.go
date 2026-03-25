package g711dtmfaudio

import (
	"fmt"
	"strings"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"g711dtmf-audio",
	gst.DebugColorNone,
	"g711dtmf-audio Element",
)

type G711DTMFAudio struct {
	RtpG711Depay  *gst.Element
	G711Dec       *gst.Element
	AudioConvert  *gst.Element
	DtmfDetect    *gst.Element
	AudioResample *gst.Element
	AudioRate     *gst.Element
}

func (e *G711DTMFAudio) New() glib.GoObjectSubclass {
	return &G711DTMFAudio{}
}

func (e *G711DTMFAudio) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"G711 + DTMF to Audio Decoder",
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

func (e *G711DTMFAudio) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.AudioConvert, err = gst.NewElement("audioconvert")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioconvert element: %v", err))
		self.Error("Failed to create audioconvert element", err)
		return
	}

	e.DtmfDetect, err = gst.NewElement("dtmfdetect")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create dtmfdetect element: %v", err))
		self.Error("Failed to create dtmfdetect element", err)
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
		e.AudioConvert,
		e.DtmfDetect,
		e.AudioResample,
		e.AudioRate,
	)

	if err := gst.ElementLinkMany(
		e.AudioConvert,
		e.DtmfDetect,
		e.AudioResample,
		e.AudioRate,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	eWeak := weak.Make(e)
	ghostSink := gst.NewGhostPadNoTargetFromTemplate("sink", elemClass.GetPadTemplate("sink"))
	ghostSink.AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		e := eWeak.Value()
		if e == nil {
			return gst.PadProbeRemove
		}

		self := gst.ToGstBin(pad.GetParent())
		if self == nil || self.Instance() == nil {
			return gst.PadProbeRemove
		}

		return e.G711Setup(self, pad, info)
	})
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.AudioRate.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *G711DTMFAudio) setupPCMU() (err error) {
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

func (e *G711DTMFAudio) setupPCMA() (err error) {
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

func (e *G711DTMFAudio) setupCodec(self *gst.Bin, gpad *gst.GhostPad) (err error) {
	if err := self.AddMany(
		e.RtpG711Depay,
		e.G711Dec,
	); err != nil {
		return fmt.Errorf("failed to add G711 elements: %w", err)
	}

	if !gpad.SetTarget(e.RtpG711Depay.GetStaticPad("sink")) {
		return fmt.Errorf("failed to set ghost pad target")
	}

	if err := gst.ElementLinkMany(
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

func (e *G711DTMFAudio) G711Setup(self *gst.Bin, pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	event := info.GetEvent()
	if event == nil || event.Type() != gst.EventTypeCaps {
		return gst.PadProbePass
	}

	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelError, "Failed to cast pad to ghost pad")
		self.Error("Failed to cast pad to ghost pad", fmt.Errorf("pad is not a ghost pad"))
		return gst.PadProbeRemove
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
		if err := e.setupCodec(self, gpad); err != nil {
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

func (e *G711DTMFAudio) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.RtpG711Depay = nil
		e.G711Dec = nil
		e.AudioConvert = nil
		e.DtmfDetect = nil
		e.AudioResample = nil
		e.AudioRate = nil
	}
	return ret
}
