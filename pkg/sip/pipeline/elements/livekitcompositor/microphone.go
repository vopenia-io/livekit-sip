package livekitcompositor

import (
	"fmt"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type LivekitCompositorMicrophone struct {
	AudioTestSrc  *gst.Element
	SilenceFilter *gst.Element
	AudioMixer    *gst.Element
}

func (e *LivekitCompositor) initMicrophone(self *gst.Bin) error {
	if e.LivekitCompositorMicrophone != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing microphone compositor")
	e.LivekitCompositorMicrophone = &LivekitCompositorMicrophone{}

	var err error
	e.LivekitCompositorMicrophone.AudioMixer, err = gst.NewElementWithProperties("audiomixer", map[string]interface{}{
		"force-live":           true,
		"ignore-inactive-pads": true,
	})
	if err != nil {
		return err
	}

	e.LivekitCompositorMicrophone.AudioTestSrc, err = gst.NewElementWithProperties("audiotestsrc", map[string]interface{}{
		"is-live": true,
		"wave":    int(4), // silence
	})
	if err != nil {
		return err
	}

	e.LivekitCompositorMicrophone.SilenceFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("audio/x-raw,channels=1,rate=8000"),
	})
	if err != nil {
		return err
	}

	if err := self.AddMany(e.LivekitCompositorMicrophone.AudioTestSrc, e.LivekitCompositorMicrophone.SilenceFilter, e.LivekitCompositorMicrophone.AudioMixer); err != nil {
		return fmt.Errorf("failed to add elements to bin: %w", err)
	}

	if err := gst.ElementLinkMany(e.LivekitCompositorMicrophone.AudioTestSrc, e.LivekitCompositorMicrophone.SilenceFilter); err != nil {
		return fmt.Errorf("failed to link elements: %w", err)
	}

	if ret := e.LivekitCompositorMicrophone.SilenceFilter.GetStaticPad("src").Link(e.LivekitCompositorMicrophone.AudioMixer.GetRequestPad("sink_%u")); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link silence filter to audiomixer: %v", ret)
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

	if !e.LivekitCompositorMicrophone.AudioTestSrc.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of audio test src with parent")
	}
	if !e.LivekitCompositorMicrophone.SilenceFilter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of silence filter with parent")
	}
	if !e.LivekitCompositorMicrophone.AudioMixer.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of audiomixer with parent")
	}
	
	done := make(chan struct{})
	silenceSrc := e.LivekitCompositorMicrophone.SilenceFilter.GetStaticPad("src")
	silenceSrc.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		close(done)
		return gst.PadProbeRemove
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		self.Log(CAT, gst.LevelWarning, "Timed out waiting for first silence buffer")
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
