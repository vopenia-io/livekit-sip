package iomanager

import (
	"fmt"
	"strings"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/tracks"
)

type IoManagerLivekit struct {
	mu     sync.Mutex
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

	gst.SignalNew(
		class.Type(),
		"active-speakers-changed",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure, // TrackSourceInfo
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

func (e *IoManagerLivekit) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	eweak := weak.Make(e)
	if _, err := self.Connect("active-speakers-changed", func(instance *gst.Element, structure *gst.Structure) {
		ptr := eweak.Value()
		if ptr != nil {
			ptr.onActiveSpeakersChanged(instance, structure)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to active-speakers-changed signal: %v", err))
		self.Error("Failed to connect to active-speakers-changed signal", err)
		return
	}
}

func (e *IoManagerLivekit) onActiveSpeakersChanged(instance *gst.Element, structure *gst.Structure) {
	self := gst.ToGstBin(instance)

	if e.Camera == nil {
		self.Log(CAT, gst.LevelWarning, "Camera element not set up yet, skipping active speaker change handling")
		return
	}

	info, err := tracks.ActiveSpeakerChangeInfoFromStructure(structure)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse active speaker change info from structure: %v", err))
		self.Error("Failed to parse active speaker change info from structure", err)
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Active speakers changed: %v", info))

	pads, err := e.Camera.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get camera sink pads: %v", err))
		self.Error("Failed to get camera sink pads", err)
		return
	}

	for _, sid := range info.ParticipantsSID {
		for _, pad := range pads {
			padInfo, err := tracks.PadGetTrackSourceInfo(pad)
			if err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get track source info for pad %s: %v", pad.GetName(), err))
				continue
			}
			if padInfo.ParticipantSID == sid {
				self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Setting active pad to %s for participant %s", pad.GetName(), sid))
				if err := e.Camera.SetProperty("active-pad", pad); err != nil {
					self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set active pad to %s: %v", pad.GetName(), err))
				}
				return
			}
		}
	}
	self.Log(CAT, gst.LevelInfo, "No active speaker pad found, setting active pad to nil")
}

func (e *IoManagerLivekit) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
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

func (e *IoManagerLivekit) ghostSinkPad(self *gst.Bin, session, ssrc, pt int, destPad *gst.Pad) (*gst.GhostPad, error) {
	pname := fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, pt)
	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(pname, destPad, class.GetPadTemplate("recv_rtp_sink_%u_%u_%u"))
	if !self.AddPad(gpad.Pad) {
		return nil, fmt.Errorf("Failed to add ghost pad %s to SIP IO element", pname)
	}

	if !gpad.Pad.SetActive(true) {
		return nil, fmt.Errorf("Failed to activate ghost pad %s", pname)
	}

	return gpad, nil
}

func (e *IoManagerLivekit) setupAudio(self *gst.Bin) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.Audio != nil {
		return nil
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
	if err := e.setupAudio(self); err != nil {
		return nil
	}

	destPad := e.Audio.GetRequestPad("sink_%u")
	if destPad == nil {
		self.Log(CAT, gst.LevelError, "Failed to get request pad from opus_g711_mix element")
		return nil
	}

	gpad, err := e.ghostSinkPad(self, session, ssrc, pt, destPad)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for audio session %d: %v", session, err))
		self.Error(fmt.Sprintf("Failed to create ghost pad for audio session %d", session), err)
		return nil
	}

	return gpad.Pad

	// pname := fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, pt)
	// class := gst.ToElementClass(self.Class())
	// gpad := gst.NewGhostPadFromTemplate(pname, destPad, class.GetPadTemplate("recv_rtp_sink_%u_%u_%u"))
	// if !self.AddPad(gpad.Pad) {
	// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad %s to SIP IO element", pname))
	// 	return nil
	// }

	// if !gpad.Pad.SetActive(true) {
	// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad %s", pname))
	// 	// TODO: do we need to remove the pad here?
	// 	return nil
	// }

	// return gpad.Pad
}

func (e *IoManagerLivekit) setupCamera(self *gst.Bin) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.Camera != nil {
		return nil
	}

	var err error
	e.Camera, err = gst.NewElementWithProperties("vp8_h264_select", map[string]interface{}{
		"h264-pt": int(97),
	})
	if err != nil {
		self.Error("Failed to create vp8_h264_select element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp8_h264_select element: %v", err))
		return err
	}

	if err := self.Add(e.Camera); err != nil {
		self.Error("Failed to add vp8_h264_select element to SIP IO bin", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add vp8_h264_select element to SIP IO bin: %v", err))
		return err
	}

	if !e.Camera.SyncStateWithParent() {
		self.Error("Failed to sync state of vp8_h264_select element with parent", nil)
		self.Log(CAT, gst.LevelError, "Failed to sync state of vp8_h264_select element with parent")
		return fmt.Errorf("Failed to sync state of vp8_h264_select element with parent")
	}

	gsrc := e.Camera.GetStaticPad("src")
	if gsrc == nil {
		self.Log(CAT, gst.LevelError, "Failed to get src pad from vp8_h264_select element")
		return fmt.Errorf("Failed to get src pad from vp8_h264_select element")
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
	if err := e.setupCamera(self); err != nil {
		return nil
	}

	sink := e.Camera.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to get sink pad from vp8-h264 element")
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

func (e *IoManagerLivekit) ReleasePad(instance *gst.Element, pad *gst.Pad) {
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

	target := gpad.GetTarget()
	if target == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad %s has no target, cannot release", pname))
		return
	}

	var session, ssrc, pt int
	if _, err := fmt.Sscanf(pname, "recv_rtp_sink_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse pad name %s: %v", pname, err))
		return
	}

	switch SessionKind(session) {
	case SessionKindMicrophone:
		e.Audio.ReleaseRequestPad(target)
	case SessionKindCamera:
		e.Camera.ReleaseRequestPad(target)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name %s: %d (%s)", pname, session, SessionKind(session).String()))
		return
	}

	if !pad.SetActive(false) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to deactivate ghost pad %s", pname))
		return
	}
	if !self.RemovePad(pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove ghost pad %s from SIP IO element", pname))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released pad %s for session %d", pname, session))
}
