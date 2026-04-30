package iolivekit

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

const AudioCaps = "audio/x-raw,format=S16LE,rate=16000,channels=1,layout=interleaved"

type IoManagerLivekit struct {
	inMu  sync.Mutex
	outMu sync.Mutex

	Compositor *gst.Element
	Fallback   *gst.Element

	videoWidth  uint
	videoHeight uint
	nvidia      bool

	RawIn        map[string]*RawInTranscode
	RawInCounter uint

	AudioIn  map[string]*AudioInTranscode
	AudioOut *AudioOutTranscode

	CameraIn  map[string]*CameraInTranscode
	CameraOut *CameraOutTranscode

	ScreenShareIn  map[string]*ScreenShareInTranscode
	ScreenShareOut *ScreenShareOutTranscode
}

type RawInTranscode struct {
	gpad       *gst.GhostPad
	Pcm16Audio *gst.Element
	pad        *gst.Pad
}

type AudioInTranscode struct {
	gpad      *gst.GhostPad
	OpusAudio *gst.Element
	Filter    *gst.Element
	pad       *gst.Pad
}

type AudioOutTranscode struct {
	gpad     *gst.GhostPad
	Queue    *gst.Element
	AudioRtp *gst.Element
	pad      *gst.Pad
}

type CameraInTranscode struct {
	gpad     *gst.GhostPad
	RTPVideo *gst.Element
	Queue    *gst.Element
	pad      *gst.Pad
}

type CameraOutTranscode struct {
	gpad     *gst.GhostPad
	Queue    *gst.Element
	VideoRTP *gst.Element
	pad      *gst.Pad
}

type ScreenShareInTranscode struct {
	gpad     *gst.GhostPad
	RTPVideo *gst.Element
	Queue    *gst.Element
	pad      *gst.Pad
}

type ScreenShareOutTranscode struct {
	gpad     *gst.GhostPad
	Queue    *gst.Element
	VideoRTP *gst.Element
	pad      *gst.Pad
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
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	gst.SignalNew(
		class.Type(),
		"active-speakers-changed",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure, // TrackSourceInfo
	)

	gst.SignalNew(
		class.Type(),
		"has-screenshare",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_BOOLEAN,
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"recv_rtp_sink_%u_%u_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewCapsFromString("application/x-rtp"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"raw_sink_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewCapsFromString("audio/x-raw, format=S16LE"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"send_rtp_src_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewCapsFromString("application/x-rtp"),
	))

	class.InstallProperties(properties)

}

func (e *IoManagerLivekit) InstanceInit(instance *glib.Object) {
	e.RawIn = make(map[string]*RawInTranscode)
	e.AudioIn = make(map[string]*AudioInTranscode)
	e.CameraIn = make(map[string]*CameraInTranscode)
	e.ScreenShareIn = make(map[string]*ScreenShareInTranscode)
	e.videoWidth = 1280
	e.videoHeight = 720
	e.nvidia = false
}

func (e *IoManagerLivekit) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	eweak := weak.Make(e)
	wself := glib.WeakRefInit(self)

	var err error
	e.Compositor, err = gst.NewElementWithProperties("livekit_compositor", map[string]interface{}{
		"video-width":  e.videoWidth,
		"video-height": e.videoHeight,
		"nvidia":       e.nvidia,
	})
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

	e.Fallback, err = gst.NewElementWithProperties("trackfallback", map[string]interface{}{
		"video-width":  e.videoWidth,
		"video-height": e.videoHeight,
		"nvidia":       e.nvidia,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create trackfallback element: %v", err))
		self.Error("Failed to create trackfallback element", err)
		return
	}
	if _, err := e.Fallback.Connect("pad-added", func(instance *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e != nil && self != nil && self.Instance() != nil {
			e.fallbackPadAdded(self, pad)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to pad-added signal of track_fallback: %v", err))
		self.Error("Failed to connect to pad-added signal of track_fallback", err)
		return
	}
	if _, err := e.Fallback.Connect("pad-removed", func(instance *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e != nil && self != nil && self.Instance() != nil {
			e.fallbackPadRemoved(self, pad)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to pad-removed signal of track_fallback: %v", err))
		self.Error("Failed to connect to pad-removed signal of track_fallback", err)
		return
	}

	if err := self.AddMany(e.Compositor, e.Fallback); err != nil {
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

func (e *IoManagerLivekit) Finalize(instance *gst.Element) {
	e.inMu.Lock()
	e.outMu.Lock()
	defer e.inMu.Unlock()
	defer e.outMu.Unlock()

	e.Compositor = nil
	e.RawIn = nil
	e.AudioIn = nil
	e.AudioOut = nil
	e.CameraIn = nil
	e.CameraOut = nil
	e.ScreenShareIn = nil
	e.ScreenShareOut = nil
}

func (e *IoManagerLivekit) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad template is nil for pad %s", name))
		return nil
	}

	switch templ.Name() {
	case "raw_sink_%u":
		return e.requestNewPadRawIn(self, templ)
	case "recv_rtp_sink_%u_%u_%u":
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
		case livekit.TrackSource_SCREEN_SHARE:
			return e.requestNewPadScreenShareIn(self, templ, name, session, ssrc, pt)
		default:
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name %s: %d (%s)", name, session, livekit.TrackSource(session).String()))
			return nil
		}
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown pad template %s for pad %s", templ.Name(), name))
		return nil
	}
}

func (e *IoManagerLivekit) requestNewPadRawIn(self *gst.Bin, templ *gst.PadTemplate) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	id := e.RawInCounter
	e.RawInCounter++

	name := fmt.Sprintf("raw_sink_%d", id)

	rawIn := &RawInTranscode{}

	var err error
	rawIn.Pcm16Audio, err = gst.NewElementWithProperties("pcm16-audio", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create pcm16-audio element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to create pcm16-audio element for pad %s", name), err)
		return nil
	}
	if err := self.Add(rawIn.Pcm16Audio); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add pcm16-audio element to SIP IO element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to add pcm16-audio element to SIP IO element for pad %s", name), err)
		return nil
	}

	rawIn.pad = e.Compositor.GetRequestPad("raw_sink_%u")
	if rawIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad %s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := rawIn.Pcm16Audio.GetStaticPad("src").Link(rawIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link pcm16-audio src pad to compositor pad for pad %s: %v", name, ret))
		self.Error(fmt.Sprintf("Failed to link pcm16-audio src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	rawIn.gpad = gst.NewGhostPadFromTemplate(name, rawIn.Pcm16Audio.GetStaticPad("sink"), templ)
	if rawIn.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad %s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return nil
	}
	if !rawIn.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad %s", name))
	}
	if !self.AddPad(rawIn.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return nil
	}

	if !rawIn.Pcm16Audio.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of pcm16-audio element with parent for pad %s", name))
	}

	e.RawIn[name] = rawIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new raw audio input pad %s", name))
	return rawIn.gpad.Pad
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
	audioIn.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(AudioCaps),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to create capsfilter element for pad %s", name), err)
		return nil
	}

	if err := self.AddMany(audioIn.OpusAudio, audioIn.Filter); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add opus-audio element to SIP IO element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to add opus-audio element to SIP IO element for pad %s", name), err)
		return nil
	}

	if err := audioIn.OpusAudio.Link(audioIn.Filter); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link opus-audio element to capsfilter for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to link opus-audio element to capsfilter for pad %s", name), err)
		return nil
	}

	audioIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if audioIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad %s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := audioIn.Filter.GetStaticPad("src").Link(audioIn.pad); ret != gst.PadLinkOK {
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
	if !audioIn.Filter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of capsfilter element with parent for pad %s", name))
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
	properties := gst.NewStructure("properties")
	if err := errors.Join(
		properties.SetUint("*.video-width", e.videoWidth),
		properties.SetUint("*.video-height", e.videoHeight),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties for factorybin element for camera input pad: %v", err))
		self.Error("Failed to set properties for factorybin element for camera input pad", err)
		return nil
	}
	cameraIn.RTPVideo, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"nv-vp8-video",
			"vp8-video",
		}),
		"child-properties": properties,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to create factorybin element for pad %s", name), err)
		return nil
	}
	cameraIn.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": 3,
		"max-size-bytes":   0,
		"max-size-time":    0,
		"leaky":            2, // downstream
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to create queue element for pad %s", name), err)
		return nil
	}

	if err := self.AddMany(cameraIn.RTPVideo, cameraIn.Queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add factorybin and queue elements to SIP IO element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to add factorybin and queue elements to SIP IO element for pad %s", name), err)
		return nil
	}

	if err := cameraIn.RTPVideo.Link(cameraIn.Queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link factorybin element to queue for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to link factorybin element to queue for pad %s", name), err)
		return nil
	}

	cameraIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if cameraIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad %s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := cameraIn.Queue.GetStaticPad("src").Link(cameraIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue src pad to compositor pad for pad %s: %v", name, ret))
		self.Error(fmt.Sprintf("Failed to link queue src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	cameraIn.gpad = gst.NewGhostPadFromTemplate(name, cameraIn.RTPVideo.GetStaticPad("sink"), templ)
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

	if !cameraIn.RTPVideo.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of factorybin element with parent for pad %s", name))
	}
	if !cameraIn.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of queue element with parent for pad %s", name))
	}

	e.CameraIn[name] = cameraIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new camera input pad %s for session %d", name, session))
	return cameraIn.gpad.Pad
}

func (e *IoManagerLivekit) requestNewPadScreenShareIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.ScreenShareIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name %s already exists", name))
		return nil
	}

	screenShareIn := &ScreenShareInTranscode{}

	var err error
	properties := gst.NewStructure("properties")
	if err := errors.Join(
		properties.SetUint("*.video-width", e.videoWidth),
		properties.SetUint("*.video-height", e.videoHeight),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties for factorybin element for screen share input pad: %v", err))
		self.Error("Failed to set properties for factorybin element for screen share input pad", err)
		return nil
	}
	screenShareIn.RTPVideo, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"nv-vp8-video",
			"vp8-video",
		}),
		"child-properties": properties,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to create factorybin element for pad %s", name), err)
		return nil
	}
	screenShareIn.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": 3,
		"max-size-bytes":   0,
		"max-size-time":    0,
		"leaky":            2, // downstream
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to create queue element for pad %s", name), err)
		return nil
	}

	if err := self.AddMany(screenShareIn.RTPVideo, screenShareIn.Queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add factorybin and queue elements to SIP IO element for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to add factorybin and queue elements to SIP IO element for pad %s", name), err)
		return nil
	}

	if err := screenShareIn.RTPVideo.Link(screenShareIn.Queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link factorybin element to queue for pad %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to link factorybin element to queue for pad %s", name), err)
		return nil
	}

	screenShareIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if screenShareIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad %s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := screenShareIn.Queue.GetStaticPad("src").Link(screenShareIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue src pad to compositor pad for pad %s: %v", name, ret))
		self.Error(fmt.Sprintf("Failed to link queue src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	screenShareIn.gpad = gst.NewGhostPadFromTemplate(name, screenShareIn.RTPVideo.GetStaticPad("sink"), templ)
	if screenShareIn.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad %s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return nil
	}
	if !screenShareIn.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad %s", name))
	}
	if !self.AddPad(screenShareIn.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return nil
	}

	if !screenShareIn.RTPVideo.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of factorybin element with parent for pad %s", name))
	}
	if !screenShareIn.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of queue element with parent for pad %s", name))
	}

	e.ScreenShareIn[name] = screenShareIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new screen share input pad %s for session %d", name, session))
	return screenShareIn.gpad.Pad
}

func (e *IoManagerLivekit) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)

	pname := pad.GetName()

	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad %s is not a ghost pad, cannot release", pname))
		return
	}

	templ := gpad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Ghost pad %s has no template, cannot release", pname))
		return
	}

	switch templ.Name() {
	case "raw_sink_%u":
		e.releasePadRawIn(self, gpad, pname)
	case "recv_rtp_sink_%u_%u_%u":
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
			e.releasePadScreenShareIn(self, gpad, pname, session, ssrc, pt)
		default:
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name %s: %d (%s)", pname, session, livekit.TrackSource(session).String()))
			return
		}
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown pad template %s for pad %s", templ.Name(), pname))
		return
	}

	if !gpad.SetActive(false) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to deactivate ghost pad %s", pname))
		return
	}
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove ghost pad %s from io_manager_livekit", pname))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released pad %s", pname))
}

func (e *IoManagerLivekit) releasePadRawIn(self *gst.Bin, gpad *gst.GhostPad, pname string) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	rawIn, exists := e.RawIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No raw input pad found with name %s", pname))
		return
	}

	if err := rawIn.Pcm16Audio.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set pcm16-audio element to NULL state for pad %s: %v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(rawIn.pad)

	if err := self.Remove(rawIn.Pcm16Audio); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove pcm16-audio element from SIP IO element for pad %s: %v", pname, err))
	}

	delete(e.RawIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released raw input pad %s", pname))
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
	if err := audioIn.Filter.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set capsfilter element to NULL state for pad %s: %v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(audioIn.pad)

	if err := self.RemoveMany(audioIn.OpusAudio, audioIn.Filter); err != nil {
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

	if err := cameraIn.RTPVideo.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad %s: %v", pname, err))
	}
	if err := cameraIn.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad %s: %v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(cameraIn.pad)

	if err := self.RemoveMany(cameraIn.RTPVideo, cameraIn.Queue); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove factorybin and queue element from SIP IO element for pad %s: %v", pname, err))
	}

	delete(e.CameraIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released camera input pad %s for session %d", pname, session))
}

func (e *IoManagerLivekit) releasePadScreenShareIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	screenShareIn, exists := e.ScreenShareIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No screen share input pad found with name %s", pname))
		return
	}

	if err := screenShareIn.RTPVideo.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad %s: %v", pname, err))
	}
	if err := screenShareIn.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad %s: %v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(screenShareIn.pad)

	if err := self.RemoveMany(screenShareIn.RTPVideo, screenShareIn.Queue); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove factorybin and queue element from SIP IO element for pad %s: %v", pname, err))
	}

	delete(e.ScreenShareIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released screen share input pad %s for session %d", pname, session))
}

func (e *IoManagerLivekit) compositorPadAdded(self *gst.Bin, pad *gst.Pad) {
	templ := pad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad %s has no template, cannot determine type", pad.GetName()))
		return
	}

	if templ.Name() != "src_%u" {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pad.GetName(), "src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse compositor pad name %s: %v", pad.GetName(), err))
		return
	}

	sink := e.Fallback.GetRequestPad(fmt.Sprintf("sink_%d", session))
	if sink == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from fallback for compositor pad %s", pad.GetName()))
		return
	}

	if ret := pad.Link(sink); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link compositor pad %s to fallback sink pad: %v", pad.GetName(), ret))
		return
	}

	if livekit.TrackSource(session) == livekit.TrackSource_SCREEN_SHARE {
		if _, err := self.Emit("has-screenshare", true); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit has-screenshare signal for compositor pad %s: %v", pad.GetName(), err))
		}
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linked compositor pad %s to fallback sink pad", pad.GetName()))
}

func (e *IoManagerLivekit) compositorPadRemoved(self *gst.Bin, pad *gst.Pad) {
	templ := pad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad %s has no template, cannot determine type", pad.GetName()))
		return
	}

	if templ.Name() != "src_%u" {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pad.GetName(), "src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse compositor pad name %s: %v", pad.GetName(), err))
		return
	}

	sink := e.Fallback.GetStaticPad(fmt.Sprintf("sink_%d", session))
	if sink == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get static pad from fallback for compositor pad %s", pad.GetName()))
		return
	}

	e.Fallback.ReleaseRequestPad(sink)

	if livekit.TrackSource(session) == livekit.TrackSource_SCREEN_SHARE {
		if _, err := self.Emit("has-screenshare", false); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit has-screenshare signal for compositor pad %s: %v", pad.GetName(), err))
		}
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released fallback sink pad for compositor pad %s", pad.GetName()))
}

func (e *IoManagerLivekit) fallbackPadAdded(self *gst.Bin, pad *gst.Pad) {
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
		e.padAddedScreenShareOut(self, pad, pname)
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
	audioOut.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": uint(0),
		"max-size-bytes":   uint(0),
		"max-size-time":    uint(2_000_000_000), // 2 seconds
		"leaky":            int(2),              // downstream
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element for audio output pad: %v", err))
		self.Error("Failed to create queue element for audio output pad", err)
		return
	}

	audioOut.AudioRtp, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"audio-pcmu",
			"audio-pcma",
		}),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for audio output pad: %v", err))
		self.Error("Failed to create factorybin element for audio output pad", err)
		return
	}
	if err := self.AddMany(audioOut.Queue, audioOut.AudioRtp); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add factorybin element to SIP IO element for audio output pad: %v", err))
		self.Error("Failed to add factorybin element to SIP IO element for audio output pad", err)
		return
	}

	if err := audioOut.Queue.Link(audioOut.AudioRtp); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue element to factorybin element for audio output pad: %v", err))
		self.Error("Failed to link queue element to factorybin element for audio output pad", err)
		return
	}

	audioOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := audioOut.pad.Link(audioOut.Queue.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link audio output pad to factorybin sink pad: %v", ret))
		self.Error("Failed to link audio output pad to factorybin sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	audioOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_MICROPHONE), audioOut.AudioRtp.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
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

	if !audioOut.AudioRtp.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of factorybin element with parent")
	}
	if !audioOut.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of queue element with parent")
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

	cameraOut.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": 3,
		"max-size-bytes":   0,
		"max-size-time":    0,
		"leaky":            2, // downstream
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element for camera output pad: %v", err))
		self.Error("Failed to create queue element for camera output pad", err)
		return
	}

	properties := gst.NewStructure("properties")
	if err := errors.Join(
		properties.SetUint("*.video-width", e.videoWidth),
		properties.SetUint("*.video-height", e.videoHeight),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties for factorybin element for camera output pad: %v", err))
		self.Error("Failed to set properties for factorybin element for camera output pad", err)
		return
	}
	cameraOut.VideoRTP, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"nv-video-h264",
			"video-h264",
		}),
		"child-properties": properties,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for camera output pad: %v", err))
		self.Error("Failed to create factorybin element for camera output pad", err)
		return
	}

	if err := self.AddMany(cameraOut.Queue, cameraOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add queue and factorybin elements to SIP IO element for camera output pad: %v", err))
		self.Error("Failed to add queue and factorybin elements to SIP IO element for camera output pad", err)
		return
	}

	if err := cameraOut.Queue.Link(cameraOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue element to factorybin element for camera output pad: %v", err))
		self.Error("Failed to link queue element to factorybin element for camera output pad", err)
		return
	}

	cameraOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := cameraOut.pad.Link(cameraOut.Queue.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link camera output pad to queue sink pad: %v", ret))
		self.Error("Failed to link camera output pad to queue sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	cameraOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_CAMERA), cameraOut.VideoRTP.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
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

	if !cameraOut.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of queue element with parent")
	}
	if !cameraOut.VideoRTP.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of factorybin element with parent")
	}

	e.CameraOut = cameraOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added camera output pad %s", pad.GetName()))
}

func (e *IoManagerLivekit) padAddedScreenShareOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.ScreenShareOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Screen share output pad already exists, cannot add new pad %s", pad.GetName()))
		return
	}

	screenShareOut := &ScreenShareOutTranscode{}

	var err error
	screenShareOut.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": 3,
		"max-size-bytes":   0,
		"max-size-time":    0,
		"leaky":            2, // downstream
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element for screen share output pad: %v", err))
		self.Error("Failed to create queue element for screen share output pad", err)
		return
	}

	properties := gst.NewStructure("properties")
	if err := errors.Join(
		properties.SetUint("*.video-width", e.videoWidth),
		properties.SetUint("*.video-height", e.videoHeight),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties for factorybin element for screen share output pad: %v", err))
		self.Error("Failed to set properties for factorybin element for screen share output pad", err)
		return
	}
	screenShareOut.VideoRTP, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"nv-video-h264",
			"video-h264",
		}),
		"child-properties": properties,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for screen share output pad: %v", err))
		self.Error("Failed to create factorybin element for screen share output pad", err)
		return
	}
	if err := self.AddMany(screenShareOut.Queue, screenShareOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to SIP IO element for screen share output pad: %v", err))
		self.Error("Failed to add elements to SIP IO element for screen share output pad", err)
		return
	}

	if err := screenShareOut.Queue.Link(screenShareOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue element to factorybin element for screen share output pad: %v", err))
		self.Error("Failed to link queue element to factorybin element for screen share output pad", err)
		return
	}

	screenShareOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := screenShareOut.pad.Link(screenShareOut.Queue.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link screen share output pad to queue sink pad: %v", ret))
		self.Error("Failed to link screen share output pad to queue sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	screenShareOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_SCREEN_SHARE), screenShareOut.VideoRTP.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
	if screenShareOut.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for screen share output pad %s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for screen share output pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return
	}
	if !screenShareOut.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for screen share output pad %s", name))
	}
	if !self.AddPad(screenShareOut.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for screen share output pad %s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for screen share output pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return
	}

	if !screenShareOut.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of queue element with parent")
	}
	if !screenShareOut.VideoRTP.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of factorybin element with parent")
	}

	e.ScreenShareOut = screenShareOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added screen share output pad %s", pad.GetName()))
}

func (e *IoManagerLivekit) fallbackPadRemoved(self *gst.Bin, pad *gst.Pad) {
	pname := pad.GetName()

	if !strings.HasPrefix(pname, "src_") {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pname, "src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse fallback pad name %s: %v", pname, err))
		return
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		e.padRemovedAudioOut(self, pad, pname)
	case livekit.TrackSource_CAMERA:
		e.padRemovedCameraOut(self, pad, pname)
	case livekit.TrackSource_SCREEN_SHARE:
		e.padRemovedScreenShareOut(self, pad, pname)
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

	if err := e.AudioOut.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad %s: %v", name, err))
	}

	if err := e.AudioOut.AudioRtp.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad %s: %v", name, err))
	}

	if err := self.RemoveMany(e.AudioOut.Queue, e.AudioOut.AudioRtp); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove elements from SIP IO element for pad %s: %v", name, err))
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

	if err := e.CameraOut.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad %s: %v", name, err))
	}
	if err := e.CameraOut.VideoRTP.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad %s: %v", name, err))
	}

	if err := self.RemoveMany(e.CameraOut.Queue, e.CameraOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove elements from SIP IO element for pad %s: %v", name, err))
	}

	if !self.RemovePad(e.CameraOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for camera output pad %s", name))
	}

	e.CameraOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed camera output pad %s", pad.GetName()))
}

func (e *IoManagerLivekit) padRemovedScreenShareOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.ScreenShareOut == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No screen share output pad exists, cannot remove pad %s", pad.GetName()))
		return
	}

	if err := e.ScreenShareOut.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad %s: %v", name, err))
	}
	if err := e.ScreenShareOut.VideoRTP.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad %s: %v", name, err))
	}

	if err := self.RemoveMany(e.ScreenShareOut.Queue, e.ScreenShareOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove elements from SIP IO element for pad %s: %v", name, err))
	}

	if !self.RemovePad(e.ScreenShareOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for screen share output pad %s", name))
	}

	e.ScreenShareOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed screen share output pad %s", pad.GetName()))
}
