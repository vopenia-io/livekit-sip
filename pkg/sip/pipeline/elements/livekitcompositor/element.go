package livekitcompositor

import (
	"fmt"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor/patchbay"
)

var CAT = gst.NewDebugCategory(
	"livekit_compositor",
	gst.DebugColorFgYellow,
	"livekit_compositor Element",
)

func init() {
	patchbay.CAT = CAT
}

type ParticipantInfo struct {
	SID        string
	Name       string
	AudioLevel float32
}

type LivekitCompositor struct {
	mu sync.Mutex

	*LivekitCompositorMicrophone
	*LivekitCompositorCamera

	participants map[string]ParticipantInfo

	currentLayout []string

	ready bool
}

func (e *LivekitCompositor) New() glib.GoObjectSubclass {
	return &LivekitCompositor{}
}

func (e *LivekitCompositor) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"LiveKit Compositor",
		"Transform",
		"Element to composite multiple LiveKit tracks into a single video stream",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	gst.SignalNew(
		class.Type(),
		"active-speakers-changed",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure,
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

	class.InstallProperties(properties)
}

func (e *LivekitCompositor) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)

	e.participants = make(map[string]ParticipantInfo)

	eweak := weak.Make(e)
	if _, err := self.Connect("active-speakers-changed", func(instance *gst.Element, structure *gst.Structure) {
		ptr := eweak.Value()
		if ptr == nil {
			return
		}
		ptr.onActiveSpeakersChanged(instance, structure)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect active-speakers-changed signal: %v", err))
		self.Error("Failed to connect active-speakers-changed signal", err)
	}
}

func (e *LivekitCompositor) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	if transition == gst.StateChangeNullToReady {
		e.mu.Lock()
		e.ready = true
		e.mu.Unlock()
	}

	if transition == gst.StateChangeReadyToNull {
		e.mu.Lock()
		e.ready = false
		e.mu.Unlock()

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
		e.mu.Unlock()
	}

	ret := self.ParentChangeState(transition)

	if transition == gst.StateChangeReadyToNull {
		e.mu.Lock()
		defer e.mu.Unlock()
	}

	return ret
}

func (e *LivekitCompositor) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
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

func (e *LivekitCompositor) requestNewSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
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
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown track source in pad name: %s", name))
		return nil
	}

	if pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create pad for name: %s", name))
		return nil
	}

	wself := glib.WeakRefInit(self)
	eweak := weak.Make(e)

	livekittracks.PadOnTrackSourceInfo(pad, func(pad *gst.Pad, info livekittracks.TrackSourceInfo) {
		e := eweak.Value()
		if e == nil {
			return
		}
		self := gst.ToGstBin(wself.Get())
		if self == nil || self.Instance() == nil {
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.participants[info.ParticipantSID] = ParticipantInfo{
			SID:  info.ParticipantSID,
			Name: info.ParticipantName,
		}
	})

	return pad
}

func (e *LivekitCompositor) ReleasePad(instance *gst.Element, pad *gst.Pad) {
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
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown track source in released pad name: %s", gpad.GetName()))
		return
	}
}
