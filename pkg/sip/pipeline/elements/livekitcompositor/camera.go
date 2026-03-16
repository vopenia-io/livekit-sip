package livekitcompositor

import (
	"errors"
	"fmt"
	"math"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/samber/lo"
)

type LivekitCompositorCamera struct {
	FakeVideoSrc   *gst.Element
	FallbackFilter *gst.Element

	PatchBay   *gst.Element
	Compositor *gst.Element
	Filter     *gst.Element

	// composition map[string]string
}

func (e *LivekitCompositor) initCamera(self *gst.Bin) error {
	if e.LivekitCompositorCamera != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing camera compositor")
	e.LivekitCompositorCamera = &LivekitCompositorCamera{}

	var err error

	e.LivekitCompositorCamera.FakeVideoSrc, err = gst.NewElementWithProperties("videotestsrc", map[string]interface{}{
		"pattern": int(2), // black
		"is-live": true,
	})
	if err != nil {
		return err
	}
	e.LivekitCompositorCamera.FallbackFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,format=I420,width=1280,height=720,framerate=30/1"),
	})
	if err != nil {
		return err
	}

	e.LivekitCompositorCamera.PatchBay, err = gst.NewElementWithProperties("livekit_compositor_patchbay", map[string]interface{}{})
	if err != nil {
		return err
	}
	e.LivekitCompositorCamera.Compositor, err = gst.NewElementWithProperties("compositor", map[string]interface{}{
		"ignore-inactive-pads": true,
		"background":           int(1), // black
	})
	if err != nil {
		return err
	}
	e.LivekitCompositorCamera.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,format=I420,width=1280,height=720,framerate=30/1"),
	})
	if err != nil {
		return err
	}

	if err := self.AddMany(
		e.LivekitCompositorCamera.FakeVideoSrc,
		e.LivekitCompositorCamera.FallbackFilter,
		e.LivekitCompositorCamera.PatchBay,
		e.LivekitCompositorCamera.Compositor,
		e.LivekitCompositorCamera.Filter); err != nil {
		return err
	}

	if err := gst.ElementLinkMany(e.LivekitCompositorCamera.FakeVideoSrc, e.LivekitCompositorCamera.FallbackFilter); err != nil {
		return err
	}

	if err := gst.ElementLinkMany(e.LivekitCompositorCamera.Compositor, e.LivekitCompositorCamera.Filter); err != nil {
		return err
	}

	src0 := e.LivekitCompositorCamera.PatchBay.GetRequestPad("src_%u")
	if src0 == nil {
		return fmt.Errorf("failed to request new source pad from patchbay")
	}
	sink0 := e.LivekitCompositorCamera.Compositor.GetRequestPad("sink_0")
	if sink0 == nil {
		return fmt.Errorf("failed to request new sink pad from compositor")
	}
	if ret := src0.Link(sink0); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link source %q and sink %q pads", src0.GetName(), sink0.GetName())
	}
	sink0.SetProperty("xpos", 0)
	sink0.SetProperty("ypos", 0)
	sink0.SetProperty("width", WIDTH)
	sink0.SetProperty("height", HEIGHT)

	fallback0 := e.LivekitCompositorCamera.PatchBay.GetRequestPad("sink_%u")
	if ret := e.LivekitCompositorCamera.FallbackFilter.GetStaticPad("src").Link(fallback0); ret != gst.PadLinkOK {
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

	if !e.LivekitCompositorCamera.FakeVideoSrc.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of fake video source with parent")
	}
	if !e.LivekitCompositorCamera.FallbackFilter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of fallback filter with parent")
	}

	if !e.LivekitCompositorCamera.PatchBay.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of patchbay with parent")
	}
	if !e.LivekitCompositorCamera.Compositor.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of compositor with parent")
	}
	if !e.LivekitCompositorCamera.Filter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of filter with parent")
	}

	if _, err := e.LivekitCompositorCamera.PatchBay.Emit("activate-path", fallback0, src0); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate initial path from fallback filter to compositor: %v", err))
	}

	return nil
}

func (e *LivekitCompositor) requestNewCameraSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initCamera(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize camera compositor: %v", err))
		return nil
	}

	sink := e.LivekitCompositorCamera.PatchBay.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to request new sink pad from patchbay")
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

	eweak := weak.Make(e)
	wself := glib.WeakRefInit(self)
	livekittracks.PadOnTrackSourceInfo(sink, func(sink *gst.Pad, info livekittracks.TrackSourceInfo) {
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
		if idx := lo.IndexOf(e.currentLayout, info.ParticipantSID); idx != -1 {
			self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track source info received for participant %s, which is in the current layout. Reapplying layout.", info.ParticipantSID))
			e.activateCameraPad(self, sink, idx, len(e.currentLayout))
		}
	})

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

	info, Infoerr := livekittracks.PadGetTrackSourceInfo(target)

	e.LivekitCompositorCamera.PatchBay.ReleaseRequestPad(target)

	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for camera sink from bin")
		return
	}

	if Infoerr != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get track source info from released camera pad: %v", Infoerr))
	} else {
		if lo.Contains(e.currentLayout, info.ParticipantSID) {
			self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track source info received for participant %s, which is in the current layout. Reapplying layout.", info.ParticipantSID))
			e.applyCameraLayout(self, e.currentLayout)
		}
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released camera sink pad %s", gpad.GetName()))
}

func (e *LivekitCompositor) activateCameraPad(self *gst.Bin, sinkPad *gst.Pad /* on patchbay */, idx int, nTrack int) bool {
	destPad := e.LivekitCompositorCamera.Compositor.GetStaticPad(fmt.Sprintf("sink_%d", idx+1))
	if destPad == nil {
		destPad = e.LivekitCompositorCamera.Compositor.GetRequestPad(fmt.Sprintf("sink_%d", idx+1))
		if destPad == nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get or request sink pad for layout position %d", idx))
			self.Error("Failed to get or request sink pad for camera layout", fmt.Errorf("failed to get or request sink pad for camera layout position %d", idx))
			return false
		}
		if err := destPad.SetProperty("sizing-policy", int(1) /* keep-aspect-ratio */); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set sizing policy on compositor sink pad for layout position %d: %v", idx, err))
		}
		srcPad := e.LivekitCompositorCamera.PatchBay.GetRequestPad("src_%u")
		if srcPad == nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to request new source pad for layout position %d", idx))
			self.Error("Failed to request new source pad for camera layout", fmt.Errorf("failed to request new source pad for camera layout position %d", idx))
			return false
		}
		if ret := srcPad.Link(destPad); ret != gst.PadLinkOK {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link source pad %s to compositor sink pad %s for layout position %d", srcPad.GetName(), destPad.GetName(), idx))
			self.Error("Failed to link camera patchbay source pad to compositor sink pad for layout", fmt.Errorf("failed to link camera patchbay source pad %s to compositor sink pad %s for layout position %d", srcPad.GetName(), destPad.GetName(), idx))
			return false
		}
	}
	srcPad := destPad.GetPeer()
	if srcPad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Compositor sink pad %s for layout position %d is not linked to any source pad", destPad.GetName(), idx))
		self.Error("Compositor sink pad for camera layout is not linked to any source pad", fmt.Errorf("compositor sink pad %s for layout position %d is not linked to any source pad", destPad.GetName(), idx))
		return false
	}
	if _, err := e.LivekitCompositorCamera.PatchBay.Emit("activate-path", sinkPad, srcPad); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate path from camera patchbay pad %s to compositor sink pad %s for layout position %d: %v", sinkPad.GetName(), destPad.GetName(), idx, err))
		return true
	}
	if err := cameraPadSetPosSize(destPad, idx, nTrack); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set position and size for compositor sink pad %s for layout position %d: %v", destPad.GetName(), idx, err))
	}

	return true
}

func (e *LivekitCompositor) cleanupCameraTrack(idx int) {
	destPad := e.LivekitCompositorCamera.Compositor.GetStaticPad(fmt.Sprintf("sink_%d", idx+1))
	if destPad == nil {
		return
	}
	srcPad := destPad.GetPeer()
	e.LivekitCompositorCamera.Compositor.ReleaseRequestPad(destPad)
	if srcPad != nil {
		e.LivekitCompositorCamera.PatchBay.ReleaseRequestPad(srcPad)
	}
}

func (e *LivekitCompositor) applyCameraLayout(self *gst.Bin, layout []string) {
	if e.LivekitCompositorCamera == nil {
		return
	}

	if len(layout) < len(e.currentLayout) {
		for i := len(layout); i < len(e.currentLayout); i++ {
			e.cleanupCameraTrack(i)
		}
	}

	for i, participantSID := range layout {
		sinkPad := e.findCameraPatchBayPadForParticipant(self, participantSID)
		if sinkPad == nil {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("No camera pad found for participant SID %s in layout position %d, skipping", participantSID, i))
			e.cleanupCameraTrack(i)
			continue
		}
		if !e.activateCameraPad(self, sinkPad, i, len(layout)) {
			return
		}

		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Activated camera path for participant SID %s at layout position %d", participantSID, i))
	}
}

func cameraPadSetPosSize(pad *gst.Pad, idx int, nTrack int) error {
	cols := int(math.Ceil(math.Sqrt(float64(nTrack))))
	rows := int(math.Ceil(float64(nTrack) / float64(cols)))

	width := WIDTH / cols
	height := HEIGHT / rows

	x := (idx % cols) * width
	y := (idx / cols) * height

	err := errors.Join(
		pad.SetProperty("xpos", x),
		pad.SetProperty("ypos", y),
		pad.SetProperty("width", width),
		pad.SetProperty("height", height),
	)

	if err != nil {
		return fmt.Errorf("failed to set position and size for camera pad: %w", err)
	}
	return nil
}

func (e *LivekitCompositor) findCameraPatchBayPadForParticipant(self *gst.Bin, participantSID string) *gst.Pad {
	if _, ok := e.participants[participantSID]; !ok {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Participant with SID %s not found when looking for camera patchbay pad", participantSID))
		return nil
	}

	sinks, err := e.LivekitCompositorCamera.PatchBay.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sink pads from patchbay: %v", err))
		return nil
	}

	for _, sink := range sinks {
		if sink.GetName() == "sink_0" {
			continue // skip the fallback pad
		}
		info, err := livekittracks.PadGetTrackSourceInfo(sink)
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get track source info from pad %s: %v", sink.GetName(), err))
			continue
		}

		if info.Source != livekit.TrackSource_CAMERA {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad %s is not a camera source, skipping", sink.GetName()))
			continue
		}

		if info.ParticipantSID == participantSID {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Found camera patchbay pad %s for participant SID %s", sink.GetName(), participantSID))
			return sink
		}
	}
	self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No camera patchbay pad found for participant SID %s", participantSID))
	return nil
}

func (e *LivekitCompositor) cleanupCamera(self *gst.Bin) {
	if e.LivekitCompositorCamera == nil {
		return
	}

	sink0 := e.LivekitCompositorCamera.PatchBay.GetStaticPad("sink_0")
	if sink0 != nil {
		e.LivekitCompositorCamera.PatchBay.ReleaseRequestPad(sink0)
	}
}
