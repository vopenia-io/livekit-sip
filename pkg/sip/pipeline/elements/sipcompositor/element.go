package sipcompositor

import (
	"fmt"
	"sync"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

var CAT = gst.NewDebugCategory(
	"sip_compositor",
	gst.DebugColorFgYellow,
	"sip_compositor Element",
)

type SipCompositor struct {
	mu sync.Mutex

	*SipCompositorMicrophone
	*SipCompositorCamera
	*SipCompositorScreenshare
}

func (e *SipCompositor) New() glib.GoObjectSubclass {
	return &SipCompositor{}
}

func (e *SipCompositor) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"SIP Compositor",
		"Transform",
		"Element to composite SIP audio tracks and pass through a single video track",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_%u_%u_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewAnyCaps(),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewAnyCaps(),
	))
}

func (e *SipCompositor) InstanceInit(instance *glib.Object) {
}

func (e *SipCompositor) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	if transition == gst.StateChangeReadyToNull {
		sinks, err := self.GetSinkPads()
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get sink pads: %v", err))
		} else {
			for _, sink := range sinks {
				e.ReleasePad(instance, sink)
			}
		}

		e.mu.Lock()
		e.cleanupMicrophone(self)
		e.cleanupCamera(self)
		e.cleanupScreenshare(self)
		e.mu.Unlock()
	}

	ret := self.ParentChangeState(transition)

	if transition == gst.StateChangeReadyToNull {
		e.SipCompositorCamera = nil
		e.SipCompositorMicrophone = nil
		e.SipCompositorScreenshare = nil
	}

	return ret
}

func (e *SipCompositor) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	if templ == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown pad template for requested pad: %s", name))
		return nil
	}

	if templ.Name() != "sink_%u_%u_%u" {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No handler for requested pad template: %s", templ.Name()))
		return nil
	}

	return e.requestNewSinkPad(self, templ, name)
}

func (e *SipCompositor) requestNewSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if name == "" {
		self.Log(CAT, gst.LevelWarning, "Requested pad with empty name")
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	var session, ssrc, payload int
	if _, err := fmt.Sscanf(name, "sink_%d_%d_%d", &session, &ssrc, &payload); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid pad name: %s", name))
		return nil
	}

	var pad *gst.Pad
	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_MICROPHONE:
		pad = e.requestNewMicrophoneSinkPad(self, templ, name)
	case livekit.TrackSource_CAMERA:
		pad = e.requestNewCameraSinkPad(self, templ, name)
	case livekit.TrackSource_SCREEN_SHARE:
		pad = e.requestNewScreenshareSinkPad(self, templ, name)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown track source in pad name: %s", name))
		return nil
	}

	if pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create pad for name: %s", name))
		return nil
	}

	return pad
}

func (e *SipCompositor) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)

	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Released pad is not a ghost pad: %s", pad.GetName()))
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	var session, ssrc, pt int
	if _, err := fmt.Sscanf(gpad.GetName(), "sink_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid pad name on release: %s", gpad.GetName()))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_MICROPHONE:
		e.releaseMicrophoneSinkPad(self, gpad)
	case livekit.TrackSource_CAMERA:
		e.releaseCameraSinkPad(self, gpad)
	case livekit.TrackSource_SCREEN_SHARE:
		e.releaseScreenshareSinkPad(self, gpad)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown track source in released pad name: %s", gpad.GetName()))
		return
	}
}
