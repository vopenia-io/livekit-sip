package iomanager

import (
	"fmt"
	"strings"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type IoManagerLivekit struct {
	inMu       sync.Mutex
	outMu      sync.Mutex
	Compositor *gst.Element

	AudioIn  map[string]*AudioInTranscode
	AudioOut *AudioOutTranscode

	CameraIn  map[string]*CameraInTranscode
	CameraOut *CameraOutTranscode
}

type AudioInTranscode struct {
	gpad      *gst.GhostPad
	OpusAudio *gst.Element
	pad       *gst.Pad
}

type AudioOutTranscode struct {
	gpad      *gst.GhostPad
	AudioG711 *gst.Element
	pad       *gst.Pad
}

type CameraInTranscode struct {
	gpad     *gst.GhostPad
	VP8Video *gst.Element
	pad      *gst.Pad
}

type CameraOutTranscode struct {
	gpad      *gst.GhostPad
	VideoH264 *gst.Element
	pad       *gst.Pad
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
	wself := glib.WeakRefInit(self)

	e.AudioIn = make(map[string]*AudioInTranscode)
	e.CameraIn = make(map[string]*CameraInTranscode)

	var err error
	e.Compositor, err = gst.NewElementWithProperties("livekit_compositor", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create livekit_compositor element: %v", err))
		self.Error("Failed to create livekit_compositor element", err)
		return
	}
	if _, err := e.Compositor.Connect("pad-added", func(instance *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e != nil && self != nil && self.Instance() != nil {
			e.compositorPadAdded(self, pad)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to pad-added signal of livekit_compositor: %v", err))
		self.Error("Failed to connect to pad-added signal of livekit_compositor", err)
		return
	}
	if _, err := e.Compositor.Connect("pad-removed", func(instance *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e != nil && self != nil && self.Instance() != nil {
			e.compositorPadRemoved(self, pad)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to pad-removed signal of livekit_compositor: %v", err))
		self.Error("Failed to connect to pad-removed signal of livekit_compositor", err)
		return
	}

	if err := self.Add(e.Compositor); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add livekit_compositor element to SIP IO element: %v", err))
		self.Error("Failed to add livekit_compositor element to SIP IO element", err)
		return
	}

	if _, err := self.Connect("active-speakers-changed", func(instance *gst.Element, structure *gst.Structure) {
		e := eweak.Value()
		if e != nil && e.Compositor != nil {
			if _, err := e.Compositor.Emit("active-speakers-changed", structure); err != nil {
				self := gst.ToGstBin(instance)
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to forward active-speakers-changed signal from SIP IO element to compositor: %v", err))
				self.Error("Failed to forward active-speakers-changed signal from SIP IO element to compositor", err)
			}
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to active-speakers-changed signal: %v", err))
		self.Error("Failed to connect to active-speakers-changed signal", err)
		return
	}
}

func (e *IoManagerLivekit) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

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

		e.AudioIn = make(map[string]*AudioInTranscode)
		e.AudioOut = nil

		e.CameraIn = make(map[string]*CameraInTranscode)
		e.CameraOut = nil
	}
	return ret
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

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		return e.requestNewPadAudioIn(self, templ, name, session, ssrc, pt)
	case livekit.TrackSource_CAMERA:
		return e.requestNewPadCameraIn(self, templ, name, session, ssrc, pt)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name %s: %d (%s)", name, session, livekit.TrackSource(session).String()))
		return nil
	}
}

func (e *IoManagerLivekit) requestNewPadAudioIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.AudioIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name %s already exists", name))
		return nil
	}

	audioIn := &AudioInTranscode{}

	var err error
	audioIn.OpusAudio, err = gst.NewElementWithProperties("opus-audio", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create opus-audio element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to create opus-audio element for pad %s", name), err)
		return nil
	}
	if err := self.Add(audioIn.OpusAudio); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add opus-audio element to SIP IO element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to add opus-audio element to SIP IO element for pad %s", name), err)
		return nil
	}

	audioIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if audioIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad %s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := audioIn.OpusAudio.GetStaticPad("src").Link(audioIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link opus-audio src pad to compositor pad for pad %s: %v", name, ret))
		self.Error(fmt.Sprintf("Failed to link opus-audio src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	audioIn.gpad = gst.NewGhostPadFromTemplate(name, audioIn.OpusAudio.GetStaticPad("sink"), templ)
	if audioIn.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad %s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return nil
	}
	if !audioIn.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad %s", name))
	}
	if !self.AddPad(audioIn.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return nil
	}

	if !audioIn.OpusAudio.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of opus-audio element with parent for pad %s", name))
	}

	e.AudioIn[name] = audioIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new audio input pad %s for session %d", name, session))
	return audioIn.gpad.Pad
}

func (e *IoManagerLivekit) requestNewPadCameraIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.CameraIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name %s already exists", name))
		return nil
	}

	cameraIn := &CameraInTranscode{}

	var err error
	cameraIn.VP8Video, err = gst.NewElementWithProperties("vp8-video", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp8-video element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to create vp8-video element for pad %s", name), err)
		return nil
	}
	if err := self.Add(cameraIn.VP8Video); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add vp8-video element to SIP IO element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to add vp8-video element to SIP IO element for pad %s", name), err)
		return nil
	}

	cameraIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if cameraIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad %s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := cameraIn.VP8Video.GetStaticPad("src").Link(cameraIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link vp8-video src pad to compositor pad for pad %s: %v", name, ret))
		self.Error(fmt.Sprintf("Failed to link vp8-video src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	cameraIn.gpad = gst.NewGhostPadFromTemplate(name, cameraIn.VP8Video.GetStaticPad("sink"), templ)
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

	if !cameraIn.VP8Video.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of vp8-video element with parent for pad %s", name))
	}

	e.CameraIn[name] = cameraIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new camera input pad %s for session %d", name, session))
	return cameraIn.gpad.Pad
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

func (e *IoManagerLivekit) releasePadAudioIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	audioIn, exists := e.AudioIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No audio input pad found with name %s", pname))
		return
	}

	if err := audioIn.OpusAudio.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set opus-audio element to NULL state for pad %s: %v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(audioIn.pad)

	if err := self.Remove(audioIn.OpusAudio); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove opus-audio element from SIP IO element for pad %s: %v", pname, err))
	}

	delete(e.AudioIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released audio input pad %s for session %d", pname, session))
}

func (e *IoManagerLivekit) releasePadCameraIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	cameraIn, exists := e.CameraIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No camera input pad found with name %s", pname))
		return
	}

	if err := cameraIn.VP8Video.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set vp8-video element to NULL state for pad %s: %v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(cameraIn.pad)

	if err := self.Remove(cameraIn.VP8Video); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove vp8-video element from SIP IO element for pad %s: %v", pname, err))
	}

	delete(e.CameraIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released camera input pad %s for session %d", pname, session))
}

func (e *IoManagerLivekit) compositorPadAdded(self *gst.Bin, pad *gst.Pad) {
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
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in compositor pad name %s: %d (%s)", pname, session, livekit.TrackSource(session).String()))
	}
}

func (e *IoManagerLivekit) padAddedAudioOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.AudioOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Audio output pad already exists, cannot add new pad %s", pad.GetName()))
		return
	}

	audioOut := &AudioOutTranscode{}

	var err error
	audioOut.AudioG711, err = gst.NewElementWithProperties("audio-g711", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audio-g711 element for audio output pad: %v", err))
		self.Error("Failed to create audio-g711 element for audio output pad", err)
		return
	}
	if err := self.Add(audioOut.AudioG711); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add audio-g711 element to SIP IO element for audio output pad: %v", err))
		self.Error("Failed to add audio-g711 element to SIP IO element for audio output pad", err)
		return
	}

	audioOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := audioOut.pad.Link(audioOut.AudioG711.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link audio output pad to audio-g711 sink pad: %v", ret))
		self.Error("Failed to link audio output pad to audio-g711 sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	audioOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_MICROPHONE), audioOut.AudioG711.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
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

	if !audioOut.AudioG711.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of audio-g711 element with parent")
	}

	e.AudioOut = audioOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added audio output pad %s", pad.GetName()))
}

func (e *IoManagerLivekit) padAddedCameraOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.CameraOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Camera output pad already exists, cannot add new pad %s", pad.GetName()))
		return
	}

	cameraOut := &CameraOutTranscode{}

	var err error
	cameraOut.VideoH264, err = gst.NewElementWithProperties("video-h264", map[string]interface{}{
		"pt": 97,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create video-h264 element for camera output pad: %v", err))
		self.Error("Failed to create video-h264 element for camera output pad", err)
		return
	}
	if err := self.Add(cameraOut.VideoH264); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add video-h264 element to SIP IO element for camera output pad: %v", err))
		self.Error("Failed to add video-h264 element to SIP IO element for camera output pad", err)
		return
	}

	cameraOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := cameraOut.pad.Link(cameraOut.VideoH264.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link camera output pad to video-h264 sink pad: %v", ret))
		self.Error("Failed to link camera output pad to video-h264 sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	cameraOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_CAMERA), cameraOut.VideoH264.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
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

	if !cameraOut.VideoH264.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of video-h264 element with parent")
	}

	e.CameraOut = cameraOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added camera output pad %s", pad.GetName()))
}

func (e *IoManagerLivekit) compositorPadRemoved(self *gst.Bin, pad *gst.Pad) {
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
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in compositor pad name %s: %d (%s)", pname, session, livekit.TrackSource(session).String()))
	}
}

func (e *IoManagerLivekit) padRemovedAudioOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.AudioOut == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No audio output pad exists, cannot remove pad %s", pad.GetName()))
		return
	}

	if err := e.AudioOut.AudioG711.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set audio-g711 element to NULL state for pad %s: %v", name, err))
	}

	if err := self.Remove(e.AudioOut.AudioG711); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove audio-g711 element from SIP IO element for pad %s: %v", name, err))
	}

	if !self.RemovePad(e.AudioOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for audio output pad %s", name))
	}

	e.AudioOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed audio output pad %s", pad.GetName()))
}

func (e *IoManagerLivekit) padRemovedCameraOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.CameraOut == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No camera output pad exists, cannot remove pad %s", pad.GetName()))
		return
	}

	if err := e.CameraOut.VideoH264.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set video-h264 element to NULL state for pad %s: %v", name, err))
	}

	if err := self.Remove(e.CameraOut.VideoH264); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove video-h264 element from SIP IO element for pad %s: %v", name, err))
	}

	if !self.RemovePad(e.CameraOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for camera output pad %s", name))
	}

	e.CameraOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed camera output pad %s", pad.GetName()))
}
