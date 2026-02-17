package iomanager

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

type IoManagerLivekit struct {
	Audio  *gst.Element // opus_g711_mix
	Camera *gst.Element // vp8-h264
}

func (e *IoManagerLivekit) New() glib.GoObjectSubclass {
	return &IoManagerLivekit{}
}

func (e *IoManagerLivekit) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"io_manager_livekit",
		"Audio/Video/Converter",
		"Manages the input and output of the LiveKit pipeline",
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

func (e *IoManagerLivekit) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
	}
	return ret
}

func (e *IoManagerLivekit) setupAudio(self *gst.Bin) error {
	if e.Audio != nil {
		return fmt.Errorf("Audio element already exists in LiveKit IO bin")
	}

	var err error
	e.Audio, err = gst.NewElement("opus_g711_mix")
	if err != nil {
		self.Error("Failed to create opus_g711_mix element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create opus_g711_mix element: %v", err))
		return err
	}

	if err := self.Add(e.Audio); err != nil {
		self.Error("Failed to add opus_g711_mix element to LiveKit IO bin", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add opus_g711_mix element to LiveKit IO bin: %v", err))
		return err
	}

	if !e.Audio.SyncStateWithParent() {
		self.Error("Failed to sync state of opus_g711_mix element with parent", nil)
		self.Log(CAT, gst.LevelError, "Failed to sync state of opus_g711_mix element with parent")
		return fmt.Errorf("Failed to sync state of opus_g711_mix element with parent")
	}

	gsrc := e.Audio.GetStaticPad("src")
	if gsrc == nil {
		self.Log(CAT, gst.LevelError, "Failed to get src pad from opus_g711_mix element")
		return fmt.Errorf("Failed to get src pad from opus_g711_mix element")
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

func (e *IoManagerLivekit) requestNewPadAudio(self *gst.Bin, session, ssrc, pt int) *gst.Pad {
	if e.Audio == nil {
		if err := e.setupAudio(self); err != nil {
			return nil
		}
	}

	destPad := e.Audio.GetRequestPad("sink_%u")
	if destPad == nil {
		self.Log(CAT, gst.LevelError, "Failed to get request pad from opus_g711_mix element")
		return nil
	}

	pname := fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, pt)
	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(pname, destPad, class.GetPadTemplate("recv_rtp_sink_%u_%u_%u"))
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad %s to SIP IO element", pname))
		return nil
	}

	if !gpad.Pad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad %s", pname))
		// TODO: do we need to remove the pad here?
		return nil
	}

	return gpad.Pad
}

func (e *IoManagerLivekit) setupCamera(self *gst.Bin) error {
	if e.Camera != nil {
		return fmt.Errorf("Camera element already exists in SIP IO bin")
	}

	var err error
	e.Camera, err = gst.NewElementWithProperties("vp8-h264", map[string]interface{}{
		"h264-pt": int(109),
	})
	if err != nil {
		self.Error("Failed to create vp8-h264 element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp8-h264 element: %v", err))
		return err
	}

	if err := self.Add(e.Camera); err != nil {
		self.Error("Failed to add vp8-h264 element to SIP IO bin", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add vp8-h264 element to SIP IO bin: %v", err))
		return err
	}

	if !e.Camera.SyncStateWithParent() {
		self.Error("Failed to sync state of vp8-h264 element with parent", nil)
		self.Log(CAT, gst.LevelError, "Failed to sync state of vp8-h264 element with parent")
		return fmt.Errorf("Failed to sync state of vp8-h264 element with parent")
	}

	gsrc := e.Camera.GetStaticPad("src")
	if gsrc == nil {
		self.Log(CAT, gst.LevelError, "Failed to get src pad from vp8-h264 element")
		return fmt.Errorf("Failed to get src pad from vp8-h264 element")
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

func (e *IoManagerLivekit) requestNewPadCamera(self *gst.Bin, session, ssrc, pt int) *gst.Pad {
	if e.Camera == nil {
		if err := e.setupCamera(self); err != nil {
			return nil
		}
	}

	sink := e.Camera.GetStaticPad("sink")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to get sink pad from vp8-h264 element")
		return nil
	}

	if sink.IsLinked() {
		self.Log(CAT, gst.LevelError, "vp8-h264 sink pad is already linked")
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

func (e *IoManagerLivekit) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
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
