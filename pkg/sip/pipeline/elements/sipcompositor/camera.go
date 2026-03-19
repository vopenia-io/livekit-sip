package sipcompositor

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type SipCompositorCamera struct {
	Identity *gst.Element
}

func (e *SipCompositor) initCamera(self *gst.Bin) error {
	if e.SipCompositorCamera != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing camera passthrough")
	e.SipCompositorCamera = &SipCompositorCamera{}

	var err error
	e.SipCompositorCamera.Identity, err = gst.NewElementWithProperties("identity", map[string]interface{}{})
	if err != nil {
		return err
	}

	if err := self.Add(e.SipCompositorCamera.Identity); err != nil {
		return fmt.Errorf("failed to add identity to bin: %w", err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_CAMERA), e.SipCompositorCamera.Identity.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for camera source")
	}
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for camera source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for camera source to bin")
	}

	if !e.SipCompositorCamera.Identity.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of identity with parent")
	}

	return nil
}

func (e *SipCompositor) requestNewCameraSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initCamera(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize camera passthrough: %v", err))
		return nil
	}

	gpad := gst.NewGhostPadFromTemplate(name, e.SipCompositorCamera.Identity.GetStaticPad("sink"), templ)
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

func (e *SipCompositor) releaseCameraSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	if e.SipCompositorCamera == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release camera sink pad but camera is not initialized")
		return
	}

	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for camera sink from bin")
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released camera sink pad %s", gpad.GetName()))
}

func (e *SipCompositor) cleanupCamera(self *gst.Bin) {
	return
}
