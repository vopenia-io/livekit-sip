package livekitcompositor

import (
	"fmt"
	"time"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type LivekitCompositorMicrophone struct {
	Fallback     *AudioFallback
	fallbackSink *gst.Pad
	AudioMixer   *gst.Element
}

func (e *LivekitCompositor) initMicrophone(self *gst.Bin) error {
	if e.LivekitCompositorMicrophone != nil {
		e.startMicrophoneFallback(self)
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

	if err := self.Add(e.LivekitCompositorMicrophone.AudioMixer); err != nil {
		return fmt.Errorf("failed to add microphone audiomixer to bin: %w", err)
	}

	eweak := weak.Make(e)
	wself := glib.WeakRefInit(self)
	if _, err := e.LivekitCompositorMicrophone.AudioMixer.Connect("pad-removed", func(_ *gst.Element, pad *gst.Pad) {
		self := gst.ToGstBin(wself.Get())
		e := eweak.Value()
		if self == nil || self.Instance() == nil || e == nil {
			return
		}
		if pad.GetDirection() != gst.PadDirectionSink {
			return
		}
		go e.cleanupMicrophone(self)
		time.Sleep(1 * time.Millisecond)
	}); err != nil {
		return fmt.Errorf("failed to connect pad-removed signal: %w", err)
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
		self.Log(CAT, gst.LevelWarning, "Failed to sync microphone audiomixer state with parent")
	}

	e.startMicrophoneFallback(self)

	return nil
}

func (e *LivekitCompositor) cleanupMicrophone(self *gst.Bin) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.LivekitCompositorMicrophone == nil {
		return
	}

	sinks, err := e.LivekitCompositorMicrophone.AudioMixer.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get sink pads while handling pad-removed signal: %v", err))
		return
	}
	if len(sinks) != 0 {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Not cleaning up microphone compositor because there are still %d sink pads", len(sinks)))
		return
	}

	if err := e.LivekitCompositorMicrophone.AudioMixer.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set microphone audiomixer state to null during cleanup: %v", err))
	}
	if err := self.Remove(e.LivekitCompositorMicrophone.AudioMixer); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove microphone audiomixer from bin during cleanup: %v", err))
	}

	e.LivekitCompositorMicrophone = nil
	self.Log(CAT, gst.LevelInfo, "Cleaned up microphone compositor")
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

func (e *LivekitCompositor) startMicrophoneFallback(self *gst.Bin) {
	if e.LivekitCompositorMicrophone == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to start microphone fallback but microphone compositor is not initialized")
		return
	}

	if e.LivekitCompositorMicrophone.Fallback != nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to start microphone fallback but fallback is already active")
		return
	}

	fallback := &AudioFallback{}

	src, err := fallback.Create(e, self)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create microphone fallback: %v", err))
		self.Error("Failed to create microphone fallback", err)
		return
	}

	sink := e.LivekitCompositorMicrophone.AudioMixer.GetRequestPad("sink_%u")
	if ret := src.Link(sink); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, "Failed to link microphone fallback to audiomixer")
		self.Error("Failed to link microphone fallback to audiomixer", fmt.Errorf("pad link result: %v", ret))
		return
	}

	if err := fallback.Sync(); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync microphone fallback state with parent: %v", err))
	}

	e.LivekitCompositorMicrophone.Fallback = fallback
	e.fallbackSink = sink

	done := make(chan struct{})
	e.LivekitCompositorMicrophone.AudioMixer.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		close(done)
		return gst.PadProbeRemove
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		self.Log(CAT, gst.LevelWarning, "Timed out waiting for first buffer")
	}

	self.Log(CAT, gst.LevelInfo, "Started microphone fallback")
}

func (e *LivekitCompositor) stopMicrophoneFallback(self *gst.Bin) {
	if e.LivekitCompositorMicrophone == nil || e.LivekitCompositorMicrophone.Fallback == nil {
		return
	}

	if err := e.LivekitCompositorMicrophone.Fallback.Remove(e, self); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove microphone fallback: %v", err))
	}

	if e.fallbackSink != nil && e.LivekitCompositorMicrophone.AudioMixer != nil {
		e.LivekitCompositorMicrophone.AudioMixer.ReleaseRequestPad(e.fallbackSink)
		e.fallbackSink = nil
	}

	e.LivekitCompositorMicrophone.Fallback = nil
	self.Log(CAT, gst.LevelInfo, "Stopped microphone fallback")
}
