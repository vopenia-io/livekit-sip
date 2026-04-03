package iosip

import (
	"fmt"
	"strings"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type SipAudioInTranscode struct {
	gpad    *gst.GhostPad
	Decoder *gst.Element // g711dtmf-audio or dtmf-audio
	pad     *gst.Pad     // compositor sink pad
}

type SipAudioOutTranscode struct {
	gpad      *gst.GhostPad
	AudioOpus *gst.Element
	pad       *gst.Pad
}

type SipDtmfInTranscode struct {
	gpad         *gst.GhostPad
	RtpDtmfDepay *gst.Element
	FakeSink     *gst.Element
}

type SipCameraInTranscode struct {
	gpad      *gst.GhostPad
	H264Video *gst.Element
	pad       *gst.Pad
}

type SipCameraOutTranscode struct {
	gpad     *gst.GhostPad
	VideoVP8 *gst.Element
	pad      *gst.Pad
}

type SipScreenshareInTranscode struct {
	gpad      *gst.GhostPad
	H264Video *gst.Element
	pad       *gst.Pad
}

type SipScreenshareOutTranscode struct {
	gpad     *gst.GhostPad
	VideoVP8 *gst.Element
	pad      *gst.Pad
}

type IoManagerSip struct {
	inMu       sync.Mutex
	outMu      sync.Mutex
	Compositor *gst.Element

	AudioIn  map[string]*SipAudioInTranscode
	AudioOut *SipAudioOutTranscode

	DtmfIn map[string]*SipDtmfInTranscode

	CameraIn  map[string]*SipCameraInTranscode
	CameraOut *SipCameraOutTranscode

	ScreenshareIn  map[string]*SipScreenshareInTranscode
	ScreenshareOut *SipScreenshareOutTranscode
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
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
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

func (e *IoManagerSip) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	eweak := weak.Make(e)
	wself := glib.WeakRefInit(self)

	e.AudioIn = make(map[string]*SipAudioInTranscode)
	e.DtmfIn = make(map[string]*SipDtmfInTranscode)
	e.CameraIn = make(map[string]*SipCameraInTranscode)
	e.ScreenshareIn = make(map[string]*SipScreenshareInTranscode)

	var err error
	e.Compositor, err = gst.NewElementWithProperties("sip_compositor", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create sip_compositor element: %v", err))
		self.Error("Failed to create sip_compositor element", err)
		return
	}
	if _, err := e.Compositor.Connect("pad-added", func(instance *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e != nil && self != nil && self.Instance() != nil {
			e.compositorPadAdded(self, pad)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to pad-added signal of sip_compositor: %v", err))
		self.Error("Failed to connect to pad-added signal of sip_compositor", err)
		return
	}
	if _, err := e.Compositor.Connect("pad-removed", func(instance *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e != nil && self != nil && self.Instance() != nil {
			e.compositorPadRemoved(self, pad)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to pad-removed signal of sip_compositor: %v", err))
		self.Error("Failed to connect to pad-removed signal of sip_compositor", err)
		return
	}

	if err := self.Add(e.Compositor); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add sip_compositor element to SIP IO element: %v", err))
		self.Error("Failed to add sip_compositor element to SIP IO element", err)
		return
	}
}

func (e *IoManagerSip) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	if transition == gst.StateChangeReadyToNull {
		// Release all input pads and their transcode elements before transitioning children
		sinks, err := self.GetSinkPads()
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get sink pads: %v", err))
		} else {
			for _, sink := range sinks {
				e.ReleasePad(instance, sink)
			}
		}

		e.outMu.Lock()
		if e.AudioOut != nil {
			e.AudioOut.AudioOpus.SetState(gst.StateNull)
			self.Remove(e.AudioOut.AudioOpus)
			self.RemovePad(e.AudioOut.gpad.Pad)
			e.AudioOut = nil
		}

		if e.CameraOut != nil {
			e.CameraOut.VideoVP8.SetState(gst.StateNull)
			self.Remove(e.CameraOut.VideoVP8)
			self.RemovePad(e.CameraOut.gpad.Pad)
			e.CameraOut = nil
		}
		e.outMu.Unlock()
	}

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.inMu.Lock()
		e.outMu.Lock()
		defer e.inMu.Unlock()
		defer e.outMu.Unlock()

		e.Compositor = nil
		e.AudioIn = make(map[string]*SipAudioInTranscode)
		e.AudioOut = nil
		e.DtmfIn = make(map[string]*SipDtmfInTranscode)
		e.CameraIn = make(map[string]*SipCameraInTranscode)
		e.CameraOut = nil
		e.ScreenshareIn = make(map[string]*SipScreenshareInTranscode)
		e.ScreenshareOut = nil
	}
	return ret
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

	if session < 0 || session > int(livekit.TrackSource_SCREEN_SHARE_AUDIO) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid session kind in pad name %s: %d (%s)", name, session, livekit.TrackSource(session).String()))
		return nil
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		return e.requestNewPadAudioIn(self, templ, name, session, ssrc, pt)
	case livekit.TrackSource_CAMERA:
		return e.requestNewPadCameraIn(self, templ, name, session, ssrc, pt)
	case livekit.TrackSource_SCREEN_SHARE:
		return e.requestNewPadScreenshareIn(self, templ, name, session, ssrc, pt)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name %s: %d (%s)", name, session, livekit.TrackSource(session).String()))
		return nil
	}
}

func (e *IoManagerSip) requestNewPadAudioIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.AudioIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name %s already exists", name))
		return nil
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadNoTargetFromTemplate(name, class.GetPadTemplate("recv_rtp_sink_%u_%u_%u"))
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad %s", name))
		return nil
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad %s", name))
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name))
		return nil
	}

	weakE := weak.Make(e)
	gpad.AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ptr := weakE.Value()
		if ptr == nil {
			return gst.PadProbeRemove
		}
		return ptr.linkNewPadAudio(pad, info, name, session, ssrc, pt)
	})

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new audio input pad %s for session %d", name, session))
	return gpad.Pad
}

func (e *IoManagerSip) linkNewPadAudio(pad *gst.Pad, info *gst.PadProbeInfo, name string, session int, ssrc int, pt int) gst.PadProbeReturn {
	event := info.GetEvent()
	if event == nil || event.Type() != gst.EventTypeCaps {
		return gst.PadProbeOK
	}

	gpad := pad.AsGhostPad()
	self := gst.ToGstBin(gpad.GetParent())
	if self == nil || self.Instance() == nil {
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
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link new audio pad in SIP IO element: %v", err))
		self.Error("Failed to link new audio pad in SIP IO element", err)
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

	switch strings.ToLower(enc) {
	case "pcmu", "pcma":
		if err := e.linkNewPadAudioMicrophone(self, pad, name, caps, session, ssrc, pt); err != nil {
			err = fmt.Errorf("Failed to link new audio pad for microphone input: %w", err)
			return gst.PadProbeRemove
		}
	case "telephone-event":
		if err := e.linkNewPadAudioDtmf(self, pad, name, caps, session, ssrc, pt); err != nil {
			err = fmt.Errorf("Failed to link new audio pad for DTMF input: %w", err)
			return gst.PadProbeRemove
		}
	default:
		err = fmt.Errorf("Unsupported encoding: %s", enc)
		return gst.PadProbeRemove
	}
	return gst.PadProbeRemove
}

func (e *IoManagerSip) linkNewPadAudioMicrophone(self *gst.Bin, pad *gst.Pad, name string, caps *gst.Caps, session int, ssrc int, pt int) error {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	gpad := pad.AsGhostPad()

	_, exists := e.AudioIn[name]
	if exists {
		return fmt.Errorf("Audio input pad %s already exists in map", name)
	}

	audioIn := &SipAudioInTranscode{}
	audioIn.gpad = gpad

	var err error
	audioIn.Decoder, err = gst.NewElementWithProperties("g711dtmf-audio", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("Failed to create g711dtmf-audio element for pad %s: %w", name, err)
	}
	if err := self.Add(audioIn.Decoder); err != nil {
		return fmt.Errorf("Failed to add g711dtmf-audio element to SIP IO element for pad %s: %w", name, err)
	}

	audioIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if audioIn.pad == nil {
		return fmt.Errorf("Failed to get request pad from compositor for pad %s", name)
	}

	if ret := audioIn.Decoder.GetStaticPad("src").Link(audioIn.pad); ret != gst.PadLinkOK {
		return fmt.Errorf("Failed to link g711dtmf-audio src pad to compositor pad for pad %s: %v", name, ret)
	}

	if !gpad.SetTarget(audioIn.Decoder.GetStaticPad("sink")) {
		return fmt.Errorf("Failed to set target pad for ghost pad %s", name)
	}

	if !audioIn.Decoder.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of g711dtmf-audio element with parent for pad %s", name))
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully linked audio pad %s with g711dtmf-audio decoder", name))

	return nil
}

func (e *IoManagerSip) linkNewPadAudioDtmf(self *gst.Bin, pad *gst.Pad, name string, caps *gst.Caps, session int, ssrc int, pt int) error {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	gpad := pad.AsGhostPad()

	_, exists := e.DtmfIn[name]
	if exists {
		return fmt.Errorf("Audio input pad %s already exists in map", name)
	}

	dtmfIn := &SipDtmfInTranscode{}
	dtmfIn.gpad = gpad

	var err error
	dtmfIn.RtpDtmfDepay, err = gst.NewElementWithProperties("rtpdtmfdepay", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("Failed to create rtpdtmfdepay element for pad %s: %w", name, err)
	}

	dtmfIn.FakeSink, err = gst.NewElementWithProperties("fakesink", map[string]interface{}{
		"sync": false,
	})
	if err != nil {
		return fmt.Errorf("Failed to create fakesink element for pad %s: %w", name, err)
	}

	if err := self.AddMany(dtmfIn.RtpDtmfDepay, dtmfIn.FakeSink); err != nil {
		return fmt.Errorf("Failed to add rtpdtmfdepay element to SIP IO element for pad %s: %w", name, err)
	}

	if ret := dtmfIn.RtpDtmfDepay.GetStaticPad("src").Link(dtmfIn.FakeSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		return fmt.Errorf("Failed to link rtpdtmfdepay src pad to fakesink pad for pad %s: %v", name, ret)
	}

	if !gpad.SetTarget(dtmfIn.RtpDtmfDepay.GetStaticPad("sink")) {
		return fmt.Errorf("Failed to set target pad for ghost pad %s", name)
	}

	if !dtmfIn.RtpDtmfDepay.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of rtpdtmfdepay element with parent for pad %s", name))
	}

	if !dtmfIn.FakeSink.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of fakesink element with parent for pad %s", name))
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully linked audio pad %s with rtpdtmfdepay element", name))

	return nil
}

func (e *IoManagerSip) requestNewPadCameraIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.CameraIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name %s already exists", name))
		return nil
	}

	cameraIn := &SipCameraInTranscode{}

	var err error
	cameraIn.H264Video, err = gst.NewElementWithProperties("h264-video", map[string]interface{}{}) // TODO: change back to h264 after testing
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264-video element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to create h264-video element for pad %s", name), err)
		return nil
	}
	if err := self.Add(cameraIn.H264Video); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add h264-video element to SIP IO element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to add h264-video element to SIP IO element for pad %s", name), err)
		return nil
	}

	cameraIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if cameraIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad %s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := cameraIn.H264Video.GetStaticPad("src").Link(cameraIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link h264-video src pad to compositor pad for pad %s: %v", name, ret))
		self.Error(fmt.Sprintf("Failed to link h264-video src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	cameraIn.gpad = gst.NewGhostPadFromTemplate(name, cameraIn.H264Video.GetStaticPad("sink"), templ)
	if cameraIn.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad %s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return nil
	}
	if !cameraIn.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad %s", name))
	}
	if !self.AddPad(cameraIn.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return nil
	}

	if !cameraIn.H264Video.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of h264-video element with parent for pad %s", name))
	}

	e.CameraIn[name] = cameraIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new camera input pad %s for session %d", name, session))
	return cameraIn.gpad.Pad
}

func (e *IoManagerSip) requestNewPadScreenshareIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.ScreenshareIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name %s already exists", name))
		return nil
	}

	screenshareIn := &SipScreenshareInTranscode{}

	var err error
	screenshareIn.H264Video, err = gst.NewElementWithProperties("h264-video", map[string]interface{}{}) // TODO: change back to h264 after testing
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264-video element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to create h264-video element for pad %s", name), err)
		return nil
	}
	if err := self.Add(screenshareIn.H264Video); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add h264-video element to SIP IO element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to add h264-video element to SIP IO element for pad %s", name), err)
		return nil
	}

	screenshareIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if screenshareIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad %s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := screenshareIn.H264Video.GetStaticPad("src").Link(screenshareIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link h264-video src pad to compositor pad for pad %s: %v", name, ret))
		self.Error(fmt.Sprintf("Failed to link h264-video src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	screenshareIn.gpad = gst.NewGhostPadFromTemplate(name, screenshareIn.H264Video.GetStaticPad("sink"), templ)
	if screenshareIn.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad %s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return nil
	}
	if !screenshareIn.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad %s", name))
	}
	if !self.AddPad(screenshareIn.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return nil
	}

	if !screenshareIn.H264Video.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of h264-video element with parent for pad %s", name))
	}

	e.ScreenshareIn[name] = screenshareIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new screenshare input pad %s for session %d", name, session))
	return screenshareIn.gpad.Pad
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

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		e.releasePadAudioIn(self, gpad, pname, session, ssrc, pt)
	case livekit.TrackSource_CAMERA:
		e.releasePadCameraIn(self, gpad, pname, session, ssrc, pt)
	case livekit.TrackSource_SCREEN_SHARE:
		e.releasePadScreenshareIn(self, gpad, pname, session, ssrc, pt)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name %s: %d (%s)", pname, session, livekit.TrackSource(session).String()))
		return
	}

	if !gpad.SetActive(false) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to deactivate ghost pad %s", pname))
		return
	}
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove ghost pad %s from SIP IO element", pname))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released pad %s for session %d", pname, session))
}

func (e *IoManagerSip) releasePadAudioIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	audioIn, exists := e.AudioIn[pname]
	if !exists {
		if dtmfIn, exists := e.DtmfIn[pname]; exists {
			e.releasePadAudioDtmf(self, dtmfIn, pname)
			return
		}
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No audio input pad found with name %s", pname))
		return
	}

	if audioIn.Decoder != nil {
		if err := audioIn.Decoder.SetState(gst.StateNull); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set decoder element to NULL state for pad %s: %v", pname, err))
		}

		if audioIn.pad != nil {
			e.Compositor.ReleaseRequestPad(audioIn.pad)
		}

		if err := self.Remove(audioIn.Decoder); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove decoder element from SIP IO element for pad %s: %v", pname, err))
		}
	}

	delete(e.AudioIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released audio input pad %s for session %d", pname, session))
}

func (e *IoManagerSip) releasePadAudioDtmf(self *gst.Bin, dtmfIn *SipDtmfInTranscode, pname string) {
	for _, element := range []*gst.Element{dtmfIn.RtpDtmfDepay, dtmfIn.FakeSink} {
		if element != nil {
			if err := element.SetState(gst.StateNull); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set element %s to NULL state for pad %s: %v", element.GetName(), pname, err))
			}
			if err := self.Remove(element); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove element %s from SIP IO element for pad %s: %v", element.GetName(), pname, err))
			}
		}
	}

	delete(e.DtmfIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released DTMF input pad %s", pname))
}

func (e *IoManagerSip) releasePadCameraIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	cameraIn, exists := e.CameraIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No camera input pad found with name %s", pname))
		return
	}

	if err := cameraIn.H264Video.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set h264-video element to NULL state for pad %s: %v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(cameraIn.pad)

	if err := self.Remove(cameraIn.H264Video); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove h264-video element from SIP IO element for pad %s: %v", pname, err))
	}

	delete(e.CameraIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released camera input pad %s for session %d", pname, session))
}

func (e *IoManagerSip) releasePadScreenshareIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	screenshareIn, exists := e.ScreenshareIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No screenshare input pad found with name %s", pname))
		return
	}

	if err := screenshareIn.H264Video.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set h264-video element to NULL state for pad %s: %v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(screenshareIn.pad)

	if err := self.Remove(screenshareIn.H264Video); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove h264-video element from SIP IO element for pad %s: %v", pname, err))
	}

	delete(e.ScreenshareIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released screenshare input pad %s for session %d", pname, session))
}

func (e *IoManagerSip) compositorPadAdded(self *gst.Bin, pad *gst.Pad) {
	pname := pad.GetName()

	if !strings.HasPrefix(pname, "src_") {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pname, "src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse compositor pad name %s: %v", pname, err))
		return
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		e.padAddedAudioOut(self, pad, pname)
	case livekit.TrackSource_CAMERA:
		e.padAddedCameraOut(self, pad, pname)
	case livekit.TrackSource_SCREEN_SHARE:
		e.padAddedScreenshareOut(self, pad, pname)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in compositor pad name %s: %d (%s)", pname, session, livekit.TrackSource(session).String()))
	}
}

func (e *IoManagerSip) padAddedAudioOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.AudioOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Audio output pad already exists, cannot add new pad %s", pad.GetName()))
		return
	}

	audioOut := &SipAudioOutTranscode{}

	var err error
	audioOut.AudioOpus, err = gst.NewElementWithProperties("audio-opus", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audio-opus element for audio output pad: %v", err))
		self.Error("Failed to create audio-opus element for audio output pad", err)
		return
	}
	if err := self.Add(audioOut.AudioOpus); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add audio-opus element to SIP IO element for audio output pad: %v", err))
		self.Error("Failed to add audio-opus element to SIP IO element for audio output pad", err)
		return
	}

	audioOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := audioOut.pad.Link(audioOut.AudioOpus.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link audio output pad to audio-opus sink pad: %v", ret))
		self.Error("Failed to link audio output pad to audio-opus sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	audioOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_MICROPHONE), audioOut.AudioOpus.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
	if audioOut.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for audio output pad %s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for audio output pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return
	}
	if !audioOut.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for audio output pad %s", name))
	}
	if !self.AddPad(audioOut.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for audio output pad %s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for audio output pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return
	}

	if !audioOut.AudioOpus.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of audio-opus element with parent")
	}

	e.AudioOut = audioOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added audio output pad %s", pad.GetName()))
}

func (e *IoManagerSip) padAddedCameraOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.CameraOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Camera output pad already exists, cannot add new pad %s", pad.GetName()))
		return
	}

	cameraOut := &SipCameraOutTranscode{}

	var err error
	cameraOut.VideoVP8, err = gst.NewElementWithProperties("video-vp8", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create video-vp8 element for camera output pad: %v", err))
		self.Error("Failed to create video-vp8 element for camera output pad", err)
		return
	}
	if err := self.Add(cameraOut.VideoVP8); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add video-vp8 element to SIP IO element for camera output pad: %v", err))
		self.Error("Failed to add video-vp8 element to SIP IO element for camera output pad", err)
		return
	}

	cameraOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := cameraOut.pad.Link(cameraOut.VideoVP8.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link camera output pad to video-vp8 sink pad: %v", ret))
		self.Error("Failed to link camera output pad to video-vp8 sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	cameraOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_CAMERA), cameraOut.VideoVP8.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
	if cameraOut.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for camera output pad %s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for camera output pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return
	}
	if !cameraOut.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for camera output pad %s", name))
	}
	if !self.AddPad(cameraOut.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for camera output pad %s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for camera output pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return
	}

	if !cameraOut.VideoVP8.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of video-vp8 element with parent")
	}

	e.CameraOut = cameraOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added camera output pad %s", pad.GetName()))
}

func (e *IoManagerSip) padAddedScreenshareOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.ScreenshareOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Screenshare output pad already exists, cannot add new pad %s", pad.GetName()))
		return
	}

	screenshareOut := &SipScreenshareOutTranscode{}

	var err error
	screenshareOut.VideoVP8, err = gst.NewElementWithProperties("video-vp8", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create video-vp8 element for screenshare output pad: %v", err))
		self.Error("Failed to create video-vp8 element for screenshare output pad", err)
		return
	}
	if err := self.Add(screenshareOut.VideoVP8); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add video-vp8 element to SIP IO element for screenshare output pad: %v", err))
		self.Error("Failed to add video-vp8 element to SIP IO element for screenshare output pad", err)
		return
	}

	screenshareOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := screenshareOut.pad.Link(screenshareOut.VideoVP8.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link screenshare output pad to video-vp8 sink pad: %v", ret))
		self.Error("Failed to link screenshare output pad to video-vp8 sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	screenshareOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_SCREEN_SHARE), screenshareOut.VideoVP8.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
	if screenshareOut.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for screenshare output pad %s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for screenshare output pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return
	}
	if !screenshareOut.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for screenshare output pad %s", name))
	}
	if !self.AddPad(screenshareOut.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for screenshare output pad %s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for screenshare output pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return
	}

	if !screenshareOut.VideoVP8.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of video-vp8 element with parent")
	}

	e.ScreenshareOut = screenshareOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added screenshare output pad %s", pad.GetName()))
}

func (e *IoManagerSip) compositorPadRemoved(self *gst.Bin, pad *gst.Pad) {
	pname := pad.GetName()

	if !strings.HasPrefix(pname, "src_") {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pname, "src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse compositor pad name %s: %v", pname, err))
		return
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		e.padRemovedAudioOut(self, pad, pname)
	case livekit.TrackSource_CAMERA:
		e.padRemovedCameraOut(self, pad, pname)
	case livekit.TrackSource_SCREEN_SHARE:
		e.padRemovedScreenshareOut(self, pad, pname)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in compositor pad name %s: %d (%s)", pname, session, livekit.TrackSource(session).String()))
	}
}

func (e *IoManagerSip) padRemovedAudioOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.AudioOut == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No audio output pad exists, cannot remove pad %s", pad.GetName()))
		return
	}

	if err := e.AudioOut.AudioOpus.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set audio-opus element to NULL state for pad %s: %v", name, err))
	}

	if err := self.Remove(e.AudioOut.AudioOpus); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove audio-opus element from SIP IO element for pad %s: %v", name, err))
	}

	if !self.RemovePad(e.AudioOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for audio output pad %s", name))
	}

	e.AudioOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed audio output pad %s", pad.GetName()))
}

func (e *IoManagerSip) padRemovedCameraOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.CameraOut == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No camera output pad exists, cannot remove pad %s", pad.GetName()))
		return
	}

	if err := e.CameraOut.VideoVP8.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set video-vp8 element to NULL state for pad %s: %v", name, err))
	}

	if err := self.Remove(e.CameraOut.VideoVP8); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove video-vp8 element from SIP IO element for pad %s: %v", name, err))
	}

	if !self.RemovePad(e.CameraOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for camera output pad %s", name))
	}

	e.CameraOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed camera output pad %s", pad.GetName()))
}

func (e *IoManagerSip) padRemovedScreenshareOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.ScreenshareOut == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No screenshare output pad exists, cannot remove pad %s", pad.GetName()))
		return
	}

	if err := e.ScreenshareOut.VideoVP8.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set video-vp8 element to NULL state for pad %s: %v", name, err))
	}

	if err := self.Remove(e.ScreenshareOut.VideoVP8); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove video-vp8 element from SIP IO element for pad %s: %v", name, err))
	}

	if !self.RemovePad(e.ScreenshareOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for screenshare output pad %s", name))
	}

	e.ScreenshareOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed screenshare output pad %s", pad.GetName()))
}
