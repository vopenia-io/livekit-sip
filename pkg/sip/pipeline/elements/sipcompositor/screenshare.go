package sipcompositor

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type SipCompositorScreenshare struct {
	Format string
	Filter *gst.Element
}

func (e *SipCompositor) initScreenshare(self *gst.Bin) error {
	if e.SipCompositorScreenshare != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing screenshare passthrough")
	e.SipCompositorScreenshare = &SipCompositorScreenshare{}
	if e.nvidia {
		e.SipCompositorScreenshare.Format = "video/x-raw(memory:CUDAMemory)"
	} else {
		e.SipCompositorScreenshare.Format = "video/x-raw"
	}

	var err error
	e.SipCompositorScreenshare.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("%s, width=(int)%d, height=(int)%d", e.SipCompositorScreenshare.Format, e.videoWidth, e.videoHeight)),
	})
	if err != nil {
		return err
	}

	if err := self.Add(e.SipCompositorScreenshare.Filter); err != nil {
		return fmt.Errorf("failed to add capsfilter to bin: %w", err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_SCREEN_SHARE), e.SipCompositorScreenshare.Filter.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for screenshare source")
	}
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for screenshare source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for screenshare source to bin")
	}

	if !e.SipCompositorScreenshare.Filter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of capsfilter with parent")
	}

	return nil
}

func (e *SipCompositor) requestNewScreenshareSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initScreenshare(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize screenshare passthrough: %v", err))
		return nil
	}

	gpad := gst.NewGhostPadFromTemplate(name, e.SipCompositorScreenshare.Filter.GetStaticPad("sink"), templ)
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
