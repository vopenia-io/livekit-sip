package sipcompositor

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type SipCompositorScreenshare struct {
	Itentity *gst.Element
}

func (e *SipCompositor) initScreenshare(self *gst.Bin) error {
	if e.SipCompositorScreenshare != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing screenshare passthrough")
	e.SipCompositorScreenshare = &SipCompositorScreenshare{}

	var err error
	e.SipCompositorScreenshare.Itentity, err = gst.NewElementWithProperties("identity", map[string]interface{}{})
	if err != nil {
		return err
	}

	if err := self.Add(e.SipCompositorScreenshare.Itentity); err != nil {
		return fmt.Errorf("failed to add identity to bin: %w", err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_SCREEN_SHARE), e.SipCompositorScreenshare.Itentity.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for screenshare source")
	}
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for screenshare source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for screenshare source to bin")
	}

	if !e.SipCompositorScreenshare.Itentity.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of identity with parent")
	}

	return nil
}

func (e *SipCompositor) requestNewScreenshareSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initScreenshare(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize screenshare passthrough: %v", err))
		return nil
	}

	gpad := gst.NewGhostPadFromTemplate(name, e.SipCompositorScreenshare.Itentity.GetStaticPad("sink"), templ)
	if gpad == nil {
		self.Log(CAT, gst.LevelError, "Failed to create ghost pad for screenshare sink")
		return nil
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad for screenshare sink")
		return nil
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, "Failed to add ghost pad for screenshare sink to bin")
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Created new screenshare sink pad %s", gpad.GetName()))

	return gpad.Pad
}

func (e *SipCompositor) releaseScreenshareSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	if e.SipCompositorScreenshare == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release screenshare sink pad but screenshare is not initialized")
		return
	}

	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for screenshare sink from bin")
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released screenshare sink pad %s", gpad.GetName()))
}

func (e *SipCompositor) cleanupScreenshare(self *gst.Bin) {
	return
}
