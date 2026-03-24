package livekitcompositor

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type LivekitCompositorMicrophone struct {
	AudioMixer *gst.Element
}

func (e *LivekitCompositor) initMicrophone(self *gst.Bin) error {
	if e.LivekitCompositorMicrophone != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing microphone compositor")
	e.LivekitCompositorMicrophone = &LivekitCompositorMicrophone{}

	var err error
	e.LivekitCompositorMicrophone.AudioMixer, err = gst.NewElementWithProperties("audiomixer", map[string]interface{}{})
	if err != nil {
		return err
	}

	if err := self.Add(e.LivekitCompositorMicrophone.AudioMixer); err != nil {
		return fmt.Errorf("failed to add audiomixer to bin: %w", err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE), e.LivekitCompositorMicrophone.AudioMixer.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for microphone source")
	}
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for microphone source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for microphone source to bin")
	}

	if !e.LivekitCompositorMicrophone.AudioMixer.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of audiomixer with parent")
	}

	return nil
}

func (e *LivekitCompositor) requestNewMicrophoneSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initMicrophone(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize microphone compositor: %v", err))
		return nil
	}

	sink := e.LivekitCompositorMicrophone.AudioMixer.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to request new sink pad from audiomixer")
		return nil
	}

	gpad := gst.NewGhostPadFromTemplate(name, sink, templ)
	if gpad == nil {
		self.Log(CAT, gst.LevelError, "Failed to create ghost pad for microphone sink")
		return nil
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad for microphone sink")
		return nil
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, "Failed to add ghost pad for microphone sink to bin")
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Created new microphone sink pad %s", gpad.GetName()))

	return gpad.Pad
}

func (e *LivekitCompositor) requestNewRawSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	return e.requestNewMicrophoneSinkPad(self, templ, name) // may need to differentiate in the future
}

func (e *LivekitCompositor) releaseMicrophoneSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	if e.LivekitCompositorMicrophone == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release microphone sink pad but microphone compositor is not initialized")
		return
	}

	target := gpad.GetTarget()
	if target == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release microphone sink pad but it has no target")
		return
	}

	e.LivekitCompositorMicrophone.AudioMixer.ReleaseRequestPad(target)
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for microphone sink from bin")
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released microphone sink pad %s", gpad.GetName()))
}

func (e *LivekitCompositor) releaseRawSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	e.releaseMicrophoneSinkPad(self, gpad) // may need to differentiate in the future
}

func (e *LivekitCompositor) applyMicrophoneLayout(self *gst.Bin, layout []string) {
	return
}

func (e *LivekitCompositor) cleanupMicrophone(self *gst.Bin) {
	return
}
