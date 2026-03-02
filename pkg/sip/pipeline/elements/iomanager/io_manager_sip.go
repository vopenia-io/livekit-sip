package iomanager

import (
	"fmt"
	"strings"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

type IoManagerSip struct {
	Audio        *gst.Element // g711-opus-dtmf
	Camera       *gst.Element // h264-vp8
}

func (e *IoManagerSip) New() glib.GoObjectSubclass {
	return &IoManagerSip{}
}

func (e *IoManagerSip) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"io_manager_sip",
		"Audio/Video/Converter",
		"Manages the input and output of the SIP pipeline",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"recv_rtp_sink_%u_%u_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewCapsFromString("application/x-rtp"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"send_rtp_src_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewCapsFromString("application/x-rtp"),
	))
}

func (e *IoManagerSip) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.Audio = nil
		e.Camera = nil
	}
	return ret
}

func (e *IoManagerSip) setupAudio(self *gst.Bin) error {
	if e.Audio != nil {
		return fmt.Errorf("Audio element already exists in SIP IO bin")
	}

	var err error
	e.Audio, err = gst.NewElement("g711-opus-dtmf")
	if err != nil {
		self.Error("Failed to create g711-opus-dtmf element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create g711-opus-dtmf element: %v", err))
		return err
	}

	if err := self.Add(e.Audio); err != nil {
		self.Error("Failed to add g711-opus-dtmf element to SIP IO bin", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add g711-opus-dtmf element to SIP IO bin: %v", err))
		return err
	}

	if !e.Audio.SyncStateWithParent() {
		self.Error("Failed to sync state of g711-opus-dtmf element with parent", nil)
		self.Log(CAT, gst.LevelError, "Failed to sync state of g711-opus-dtmf element with parent")
		return fmt.Errorf("Failed to sync state of g711-opus-dtmf element with parent")
	}

	gsrc := e.Audio.GetStaticPad("src")
	if gsrc == nil {
		self.Log(CAT, gst.LevelError, "Failed to get src pad from g711-opus-dtmf element")
		return fmt.Errorf("Failed to get src pad from g711-opus-dtmf element")
	}

	pname := fmt.Sprintf("send_rtp_src_%d", SessionKindMicrophone)
	class := gst.ToElementClass(self.Class())

	gsrcp := gst.NewGhostPadFromTemplate(pname, gsrc, class.GetPadTemplate("send_rtp_src_%u"))
	if !self.AddPad(gsrcp.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad %s to SIP IO element", pname))
		return fmt.Errorf("Failed to add ghost pad %s to SIP IO element", pname)
	}

	if !gsrcp.Pad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad %s", pname))
		// TODO: do we need to remove the pad here?
		return fmt.Errorf("Failed to activate ghost pad %s", pname)
	}

	self.Log(CAT, gst.LevelInfo, "Successfully set up audio element in SIP IO bin")

	return nil
}

func (e *IoManagerSip) linkNewPadAudio(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	event := info.GetEvent()
	if event.Type() != gst.EventTypeCaps {
		return gst.PadProbeOK
	}

	gpad := pad.AsGhostPad()
	instance := gpad.GetParent()
	self := gst.ToGstBin(instance)
	if self == nil {
		CAT.Log(gst.LevelError, "Failed to get SIP IO element from ghost pad parent when linking new audio pad")
		return gst.PadProbeRemove
	}

	caps := event.ParseCaps()
	if caps == nil {
		self.Log(CAT, gst.LevelError, "Failed to query caps from peer pad when linking new audio pad in SIP IO element")
		return gst.PadProbeRemove
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linking new audio pad %s in SIP IO element with caps: %s", gpad.GetName(), caps.String()))

	var err error
	defer func() {
		if err == nil {
			return
		}
		self.Error("Failed to link new audio pad in SIP IO element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link new audio pad in SIP IO element: %v", err))

		if !self.RemovePad(gpad.Pad) {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad %s from SIP IO element", gpad.GetName()))
		}
	}()

	encVal, err := caps.GetStructureAt(0).GetValue("encoding-name")
	if err != nil {
		err = fmt.Errorf("Failed to get encoding name from caps: %w (%s)", err, caps.String())
		return gst.PadProbeRemove
	}

	enc, ok := encVal.(string)
	if !ok {
		err = fmt.Errorf("Encoding name in caps is not a string: %T (%s)", encVal, caps.String())
		return gst.PadProbeRemove
	}

	var sink *gst.Pad
	switch strings.ToLower(enc) {
	case "pcmu", "pcma":
		sink = e.Audio.GetStaticPad("sink")
	case "telephone-event":
		sink = e.Audio.GetRequestPad("sink_dtmf")
	default:
		err = fmt.Errorf("Unsupported encoding: %s", enc)
		return gst.PadProbeRemove
	}

	if sink == nil {
		err = fmt.Errorf("Failed to get sink pad from g711-opus-dtmf element")
		return gst.PadProbeRemove
	}

	if sink.IsLinked() {
		err = fmt.Errorf("g711-opus-dtmf sink pad is already linked")
		return gst.PadProbeRemove
	}

	if !gpad.SetTarget(sink) {
		err = fmt.Errorf("Failed to set target pad for ghost pad %s", gpad.GetName())
		return gst.PadProbeRemove
	}

	return gst.PadProbeRemove
}

func (e *IoManagerSip) requestNewPadAudio(self *gst.Bin, session, ssrc, pt int) *gst.Pad {
	if e.Audio == nil {
		if err := e.setupAudio(self); err != nil {
			return nil
		}
	}

	pname := fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, pt)
	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadNoTargetFromTemplate(pname, class.GetPadTemplate("recv_rtp_sink_%u_%u_%u"))
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad %s to SIP IO element", pname))
		return nil
	}

	if !gpad.Pad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad %s", pname))
		// TODO: do we need to remove the pad here?
		return nil
	}

	weakE := weak.Make(e)
	gpad.AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ptr := weakE.Value()
		if ptr == nil {
			return gst.PadProbeRemove
		}
		return ptr.linkNewPadAudio(pad, info)
	})

	return gpad.Pad
}

func (e *IoManagerSip) setupCamera(self *gst.Bin) error {
	if e.Camera != nil {
		return fmt.Errorf("Camera element already exists in SIP IO bin")
	}

	var err error
	e.Camera, err = gst.NewElement("h264-vp8")
	if err != nil {
		self.Error("Failed to create h264-vp8 element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264-vp8 element: %v", err))
		return err
	}

	if err := self.Add(e.Camera); err != nil {
		self.Error("Failed to add h264-vp8 element to SIP IO bin", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add h264-vp8 element to SIP IO bin: %v", err))
		return err
	}

	if !e.Camera.SyncStateWithParent() {
		self.Error("Failed to sync state of h264-vp8 element with parent", nil)
		self.Log(CAT, gst.LevelError, "Failed to sync state of h264-vp8 element with parent")
		return fmt.Errorf("Failed to sync state of h264-vp8 element with parent")
	}

	gsrc := e.Camera.GetStaticPad("src")
	if gsrc == nil {
		self.Log(CAT, gst.LevelError, "Failed to get src pad from h264-vp8 element")
		return fmt.Errorf("Failed to get src pad from h264-vp8 element")
	}

	pname := fmt.Sprintf("send_rtp_src_%d", SessionKindCamera)
	class := gst.ToElementClass(self.Class())

	gsrcp := gst.NewGhostPadFromTemplate(pname, gsrc, class.GetPadTemplate("send_rtp_src_%u"))
	if !self.AddPad(gsrcp.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad %s to SIP IO element", pname))
		return fmt.Errorf("Failed to add ghost pad %s to SIP IO element", pname)
	}

	if !gsrcp.Pad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad %s", pname))
		// TODO: do we need to remove the pad here?
		return fmt.Errorf("Failed to activate ghost pad %s", pname)
	}

	self.Log(CAT, gst.LevelInfo, "Successfully set up camera element in SIP IO bin")

	return nil
}

func (e *IoManagerSip) requestNewPadCamera(self *gst.Bin, session, ssrc, pt int) *gst.Pad {
	if e.Camera == nil {
		if err := e.setupCamera(self); err != nil {
			return nil
		}
	}

	sink := e.Camera.GetStaticPad("sink")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to get sink pad from h264-vp8 element")
		return nil
	}

	if sink.IsLinked() {
		self.Log(CAT, gst.LevelError, "h264-vp8 sink pad is already linked")
		return nil
	}

	pname := fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, pt)
	class := gst.ToElementClass(self.Class())

	gsink := gst.NewGhostPadFromTemplate(pname, sink, class.GetPadTemplate("recv_rtp_sink_%u_%u_%u"))
	if !self.AddPad(gsink.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad %s to SIP IO element", pname))
		return nil
	}

	if !gsink.Pad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad %s", pname))
		// TODO: do we need to remove the pad here?
		return nil
	}

	return gsink.Pad
}

func (e *IoManagerSip) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)

	pname := pad.GetName()
	if !strings.HasPrefix(pname, "recv_rtp_sink_") {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid pad name %s, expected to start with recv_rtp_sink_", pname))
		return
	}

	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad %s is not a ghost pad, cannot release", pname))
		return
	}

	var session, ssrc, pt int
	if _, err := fmt.Sscanf(pname, "recv_rtp_sink_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse pad name %s: %v", pname, err))
		return
	}

	switch SessionKind(session) {
	case SessionKindMicrophone:
		if e.Audio != nil {
			e.Audio.ReleaseRequestPad(gpad.GetTarget())
		}
	}
	if !pad.SetActive(false) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to deactivate ghost pad %s", pname))
		return
	}
	if !self.RemovePad(pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove ghost pad %s from SIP IO element", pname))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released pad %s", pname))
}

func (e *IoManagerSip) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	var session, ssrc, pt int
	if _, err := fmt.Sscanf(name, "recv_rtp_sink_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse pad name %s: %v", name, err))
		return nil
	}

	if pt < 0 || pt > 127 {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid payload type in pad name %s: %d", name, pt))
		return nil
	}

	if session < 0 || session > int(SessionKindScreenShareAudio) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid session kind in pad name %s: %d (%s)", name, session, SessionKind(session).String()))
		return nil
	}

	switch SessionKind(session) {
	case SessionKindMicrophone:
		return e.requestNewPadAudio(self, session, ssrc, pt)
	case SessionKindCamera:
		return e.requestNewPadCamera(self, session, ssrc, pt)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name %s: %d (%s)", name, session, SessionKind(session).String()))
		return nil
	}
}
