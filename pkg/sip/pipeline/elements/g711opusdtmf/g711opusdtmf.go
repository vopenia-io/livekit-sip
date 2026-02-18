package g711opusdtmf

import (
	"fmt"
	"strings"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"g711-opus-dtmf",
	gst.DebugColorNone,
	"g711-opus-dtmf Element",
)

var properties = []*glib.ParamSpec{
	glib.NewBoolParam(
		"drop-audio",
		"Drop Audio",
		"Whether to drop audio packets and only process DTMF events",
		false,
		glib.ParameterWritable,
	),
}

type G711OpusDtmf struct {
	Identity      *gst.Element
	RtpG711Depay  *gst.Element
	G711Dec       *gst.Element
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	AudioRate     *gst.Element
	DtmfDetect    *gst.Element
	Valve         *gst.Element
	OpusEnc       *gst.Element
	RtpOpusPay    *gst.Element

	RtpDtmlDepay *gst.Element
	FakeSink     *gst.Element
}

func (e *G711OpusDtmf) New() glib.GoObjectSubclass {
	return &G711OpusDtmf{}
}

func (e *G711OpusDtmf) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"G711 to Opus Transcoder with DTMF Detection",
		"Audio/Converter",
		"Decodes G711, resamples, and encodes to Opus",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string){ PCMU, PCMA }"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_dtmf",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)TELEPHONE-EVENT"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)48000, encoding-name=(string)OPUS"),
	))

	class.InstallProperties(properties)
}

func (e *G711OpusDtmf) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.Identity, err = gst.NewElement("identity")
	if err != nil {
		self.Error("Failed to create identity element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create identity element: %v", err))
		return
	}
	e.Identity.GetStaticPad("sink").AddProbe(gst.PadProbeTypeEventDownstream, func(p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		return e.G711Setup(self, p, info) // TODO: do that cause any leaks?
	})

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

	e.DtmfDetect, err = gst.NewElement("dtmfdetect")
	if err != nil {
		self.Error("Failed to create dtmfdetect element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create dtmfdetect element: %v", err))
		return
	}

	e.Valve, err = gst.NewElementWithProperties("valve", map[string]interface{}{
		"drop":      false,
		"drop-mode": int(1), // forward-sticky-events
	})

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

	e.RtpDtmlDepay, err = gst.NewElement("rtpdtmfdepay")
	if err != nil {
		self.Error("Failed to create rtpdtmfdepay element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpdtmfdepay element: %v", err))
		return
	}

	e.FakeSink, err = gst.NewElementWithProperties("fakesink", map[string]interface{}{
		"sync":  false,
		"async": false,
	})
	if err != nil {
		self.Error("Failed to create fakesink element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create fakesink element: %v", err))
		return
	}

	self.AddMany(
		e.Identity,
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
		e.DtmfDetect,
		e.Valve,
		e.OpusEnc,
		e.RtpOpusPay,
		e.RtpDtmlDepay,
		e.FakeSink,
	)

	if err := gst.ElementLinkMany(
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
		e.DtmfDetect,
		e.Valve,
		e.OpusEnc,
		e.RtpOpusPay,
	); err != nil {
		self.Error("Failed to link elements", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		return
	}

	if err := gst.ElementLinkMany(
		e.RtpDtmlDepay,
		e.FakeSink,
	); err != nil {
		self.Error("Failed to link DTMF elements", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link DTMF elements: %v", err))
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.Identity.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSinkDtmf := gst.NewGhostPadFromTemplate("sink_dtmf", e.RtpDtmlDepay.GetStaticPad("sink"), elemClass.GetPadTemplate("sink_dtmf"))
	self.AddPad(ghostSinkDtmf.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpOpusPay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *G711OpusDtmf) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "drop-audio":
		gv, _ := value.GoValue()
		val, _ := gv.(bool)
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Setting drop-audio to %t", val))
		if err := e.Valve.SetProperty("drop", val); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set drop-audio: %v", err))
		}
	}
}

func (e *G711OpusDtmf) setupPCMU() (err error) {
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

func (e *G711OpusDtmf) setupPCMA() (err error) {
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

func (e *G711OpusDtmf) setupCodec(self *gst.Bin) (err error) {
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

func (e *G711OpusDtmf) G711Setup(self *gst.Bin, p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
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
				self.Error("Failed to setup PCMU elements", err)
				return gst.PadProbeRemove
			}
		case "PCMA":
			if err := e.setupPCMA(); err != nil {
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

func (e *G711OpusDtmf) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
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
		e.OpusEnc = nil
		e.RtpOpusPay = nil
	}
	return ret
}
