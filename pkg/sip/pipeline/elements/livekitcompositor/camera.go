package livekitcompositor

import (
	"errors"
	"fmt"
	"math"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/samber/lo"
)

type LivekitCompositorCameraFallback interface {
	Create(e *LivekitCompositor, self *gst.Bin) (*gst.Pad, error)
	Sync(e *LivekitCompositor, self *gst.Bin)
	Cleanup(e *LivekitCompositor, self *gst.Bin)
}

type LivekitCompositorCamera struct {
	Fallback *VideoFallback

	Compositor *gst.Element
	Filter     *gst.Element

	Format string
}

func (e *LivekitCompositor) initCamera(self *gst.Bin) error {
	if e.LivekitCompositorCamera != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Initializing camera compositor with driver %s", lo.Ternary(e.nvidia, "nvidia", "cpu")))
	e.LivekitCompositorCamera = &LivekitCompositorCamera{}
	if e.nvidia {
		e.LivekitCompositorCamera.Format = "video/x-raw(memory:CUDAMemory)"
	} else {
		e.LivekitCompositorCamera.Format = "video/x-raw"
	}
	e.LivekitCompositorCamera.Fallback = &VideoFallback{}

	fallbackPad, err := e.LivekitCompositorCamera.Fallback.Create(e, self)
	if err != nil {
		return err
	}

	if e.nvidia {
		e.LivekitCompositorCamera.Compositor, err = gst.NewElementWithProperties("cudacompositor", map[string]interface{}{
			"force-live":           true,
			"ignore-inactive-pads": true,
		})
	} else {
		e.LivekitCompositorCamera.Compositor, err = gst.NewElementWithProperties("compositor", map[string]interface{}{
			"force-live":           true,
			"ignore-inactive-pads": true,
			"background":           int(1), // black
		})
	}
	if err != nil {
		return err
	}

	e.LivekitCompositorCamera.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("%s,width=%d,height=%d,framerate=24/1", e.LivekitCompositorCamera.Format, e.videoWidth, e.videoHeight)),
	})
	if err != nil {
		return err
	}

	if err := self.AddMany(
		e.LivekitCompositorCamera.Compositor,
		e.LivekitCompositorCamera.Filter); err != nil {
		return err
	}

	if err := gst.ElementLinkMany(e.LivekitCompositorCamera.Compositor, e.LivekitCompositorCamera.Filter); err != nil {
		return err
	}

	sink0 := e.LivekitCompositorCamera.Compositor.GetRequestPad("sink_0")
	if sink0 == nil {
		return fmt.Errorf("failed to request new sink pad from compositor")
	}
	if err := errors.Join(
		sink0.SetProperty("xpos", 0),
		sink0.SetProperty("ypos", 0),
		sink0.SetProperty("width", int(e.videoWidth)),
		sink0.SetProperty("height", int(e.videoHeight)),
		sink0.SetProperty("max-last-buffer-repeat", uint64(math.MaxUint64)),
		sink0.SetProperty("repeat-after-eos", true),
	); err != nil {
		return fmt.Errorf("failed to set position and size for compositor sink pad for fallback video: %w", err)
	}

	if ret := fallbackPad.Link(sink0); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link fallback filter to patchbay: %v", ret)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_CAMERA), e.LivekitCompositorCamera.Filter.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for camera source")
	}
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for camera source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for camera source to bin")
	}

	if err := e.LivekitCompositorCamera.Fallback.Sync(); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync fallback elements with parent: %v", err))
	}

	if !e.LivekitCompositorCamera.Compositor.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of compositor with parent")
	}
	if !e.LivekitCompositorCamera.Filter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of filter with parent")
	}

	return nil
}

func (e *LivekitCompositor) requestNewCameraSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initCamera(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize camera compositor: %v", err))
		return nil
	}

	var session, ssrc, pt int
	if _, err := fmt.Sscanf(name, "sink_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid pad name: %s", name))
		return nil
	}

	if e.LivekitCompositorCamera.Compositor.GetStaticPad(fmt.Sprintf("sink_%d", ssrc)) != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Sink pad for SSRC %d already exists, cannot create another one", ssrc))
		return nil
	}

	sink := e.LivekitCompositorCamera.Compositor.GetRequestPad(fmt.Sprintf("sink_%d", ssrc))
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to request new sink pad from patchbay")
		return nil
	}

	if err := errors.Join(
		sink.SetProperty("xpos", int(0)),
		sink.SetProperty("ypos", int(0)),
		sink.SetProperty("width", int(e.videoWidth)),
		sink.SetProperty("height", int(e.videoHeight)),
		sink.SetProperty("max-last-buffer-repeat", uint64(math.MaxUint64)),
		sink.SetProperty("repeat-after-eos", true),
		sink.SetProperty("sizing-policy", int(1)), // keep-aspect-ratio
		sink.SetProperty("alpha", float64(0)),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set position and size for camera sink pad: %v", err))
		return nil
	}

	gpad := gst.NewGhostPadFromTemplate(name, sink, templ)
	if gpad == nil {
		self.Log(CAT, gst.LevelError, "Failed to create ghost pad for camera sink")
		return nil
	}

	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad for camera sink")
		return nil
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, "Failed to add ghost pad for camera sink to bin")
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Created new camera sink pad %s", gpad.GetName()))

	return gpad.Pad
}

func (e *LivekitCompositor) releaseCameraSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	if e.LivekitCompositorCamera == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release camera sink pad but camera compositor is not initialized")
		return
	}

	target := gpad.GetTarget()
	if target == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release camera sink pad but it has no target")
		return
	}

	if info, err := livekittracks.PadGetTrackSourceInfo(target); err == nil {
		_, ok := e.tracks[info.Source][info.ParticipantSID]
		if ok {
			delete(e.tracks[info.Source], info.ParticipantSID)
		}
	}

	e.LivekitCompositorCamera.Compositor.ReleaseRequestPad(target)

	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for camera sink from bin")
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released camera sink pad %s", gpad.GetName()))
}

func (e *LivekitCompositor) findPadForParticipant(self *gst.Bin, sid string, kind livekit.TrackSource) (*gst.Pad, livekittracks.TrackSourceInfo, bool) {
	info, ok := e.tracks[kind][sid]
	if !ok {
		return nil, livekittracks.TrackSourceInfo{}, false
	}

	pname := fmt.Sprintf("sink_%d_%d_%d", kind, info.SSRC, info.PT)
	pad := self.GetStaticPad(pname)
	if pad == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No pad found for participant SID %s and track source %s (expected pad name: %s)", sid, kind.String(), pname))
		return nil, info, false
	}

	return pad, info, true
}

func (e *LivekitCompositor) applyCameraLayout(self *gst.Bin, layout []string) {
	if e.LivekitCompositorCamera == nil || len(layout) == 0 {
		return
	}

	type PadInfo struct {
		pad  *gst.Pad
		info livekittracks.TrackSourceInfo
	}

	for _, participantSID := range e.currentLayout {
		pad, _, ok := e.findPadForParticipant(self, participantSID, livekit.TrackSource_CAMERA)
		if !ok {
			continue
		}

		if !lo.Contains(layout, participantSID) {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Participant SID %s is in current layout but not in new layout, hiding camera track", participantSID))
			e.hideCameraTrack(self, pad)
			continue
		}
	}

	for i, participantSID := range layout {
		pad, _, ok := e.findPadForParticipant(self, participantSID, livekit.TrackSource_CAMERA)
		if !ok {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("No camera pad found for participant SID %s, skipping layout for this participant", participantSID))
			continue
		}

		if err := e.cameraPadSetPosSize(pad, i, len(layout)); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set position and size for camera pad for participant SID %s: %v", participantSID, err))
			continue
		}

		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Activated camera path for participant SID %s at layout position %d", participantSID, i))
	}
}

func (e *LivekitCompositor) hideCameraTrack(self *gst.Bin, pad *gst.Pad) {
	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to cast pad %s to ghost pad when hiding camera track", pad.GetName()))
		return
	}
	target := gpad.GetTarget()
	if target == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get target pad for ghost pad %s when hiding camera track", gpad.GetName()))
		return
	}

	if err := target.SetProperty("alpha", float64(0)); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set alpha property to 0 for pad %s when hiding camera track: %v", target.GetName(), err))
	}
}

func cameraComputeSize(videoWidth, videoHeight int, idx int, nTrack int) (width, height, x, y int) {
	cols := int(math.Ceil(math.Sqrt(float64(nTrack))))
	rows := int(math.Ceil(float64(nTrack) / float64(cols)))

	width = int(videoWidth) / cols
	height = int(videoHeight) / rows

	x = (idx % cols) * width
	y = (idx / cols) * height

	return
}

func (e *LivekitCompositor) cameraPadSetPosSize(pad *gst.Pad, idx int, nTrack int) error {
	gpad := pad.AsGhostPad()
	if gpad == nil {
		return fmt.Errorf("failed to cast pad %s to ghost pad when setting position and size for camera track", pad.GetName())
	}
	target := gpad.GetTarget()
	if target == nil {
		return fmt.Errorf("failed to get target pad for ghost pad %s when setting position and size for camera track", gpad.GetName())
	}

	width, height, x, y := cameraComputeSize(int(e.videoWidth), int(e.videoHeight), idx, nTrack)

	err := errors.Join(
		target.SetProperty("xpos", x),
		target.SetProperty("ypos", y),
		target.SetProperty("width", int(width)),
		target.SetProperty("height", int(height)),
		target.SetProperty("alpha", float64(1)),
	)

	if err != nil {
		return fmt.Errorf("failed to set position and size for camera pad: %w", err)
	}
	return nil
}

func (e *LivekitCompositor) cleanupCamera(self *gst.Bin) {
	if e.LivekitCompositorCamera == nil {
		return
	}
}
