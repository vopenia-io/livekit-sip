package pipeline

import (
	"fmt"
	"time"

	"github.com/go-gst/go-gst/gst"
)

func (gp *GstPipeline) AddWebRTCSourceToSelector(sid string) (*WebrtcToSelector, error) {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	if gp.Closed() {
		return nil, fmt.Errorf("pipeline is closed")
	}

	state := gp.Pipeline.GetCurrentState()

	switch state {
	case gst.StatePlaying, gst.StateReady, gst.StatePaused:
	default:
		return nil, fmt.Errorf("pipeline must be in playing, ready, or paused state to add source, current state: %s", state.String())
	}

	if _, exists := gp.WebrtcToSelectors[sid]; exists {
		return nil, fmt.Errorf("webrtc source with sid %s already exists", sid)
	}

	fmt.Println("Adding WebRTC source to selector with sid:", sid)
	webRTCToSelector, err := CastErr[*WebrtcToSelector](gp.addChain(buildWebRTCToSelectorChain(gp.SelectorToSip, sid)))
	if err != nil {
		fmt.Println("Error building WebRTC to selector chain:", err)
		return nil, err
	}
	fmt.Println("Successfully built WebRTC to selector chain for sid:", sid)

	gp.WebrtcToSelectors[sid] = webRTCToSelector

	if err := gp.ensureActiveSource(); err != nil {
		return nil, fmt.Errorf("failed to ensure active source after adding new source: %w", err)
	}

	return webRTCToSelector, nil
}

func (gp *GstPipeline) SwitchWebRTCSelectorSource(srcID string) error {
	gp.mu.Lock()
	defer gp.mu.Unlock()
	return gp.switchWebRTCSelectorSource(srcID)
}

func (gp *GstPipeline) switchWebRTCSelectorSource(sid string) error {
	if gp.Closed() {
		return fmt.Errorf("pipeline is closed")
	}

	state := gp.Pipeline.GetCurrentState()
	switch state {
	case gst.StatePlaying, gst.StatePaused, gst.StateReady:
	default:
		return fmt.Errorf("pipeline must be in playing, paused, or ready state to switch source, current state: %s", state.String())
	}

	webrtcToSelector, exists := gp.WebrtcToSelectors[sid]
	if !exists {
		return fmt.Errorf("webrtc source with sid %s does not exist", sid)
	}

	sel := gp.RtpInputSelector
	selPad := webrtcToSelector.RtpPad

	activeProp, err := sel.GetProperty("active-pad")
	if err != nil || activeProp == nil {
		return fmt.Errorf("failed to get active pad from selector: %w", err)
	}
	activePad, ok := activeProp.(*gst.Pad)
	if !ok || activePad.GetParentElement() == nil {
		return fmt.Errorf("active pad from selector is invalid")
	}

	done := make(chan struct{})
	timeout := time.After(2 * time.Second)
	probe := selPad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			// fmt.Println("Buffer is nil, continuing to wait for keyframe")
			return gst.PadProbePass
		}

		if !buf.HasFlags(gst.BufferFlagDeltaUnit) {
			// fmt.Println("Got a keyframe!")
			sel.SetProperty("active-pad", selPad)
			close(done)
			return gst.PadProbeRemove
		}
		// fmt.Println("Not a keyframe, waiting...")
		return gst.PadProbePass
	})

	// sinkPad := webrtcToSelector.RtpVp8Depay.GetStaticPad("sink")
	// if !sinkPad.IsLinked() {
	// 	return fmt.Errorf("webrtc rtp depayloader sink pad is not linked")
	// }
	// rtpBinSrcPad := sinkPad.GetPeer()
	// defer rtpBinSrcPad.Unref()
	// structure := gst.NewStructure("GstForceKeyUnit")
	// structure.SetValue("all-headers", true)
	// event := gst.NewCustomEvent(gst.EventTypeCustomUpstream, structure)
	// fmt.Println("Sending force key unit event to webrtc source")
	// if !rtpBinSrcPad.SendEvent(event) {
	// 	fmt.Println("Failed to send force key unit event to webrtc source")
	// 	return errors.New("failed to send force key unit event to webrtc source")
	// } else {
	// 	fmt.Println("Sent force key unit event to webrtc source")
	// }

	select {
	case <-timeout:
		selPad.RemoveProbe(probe)
		return fmt.Errorf("timeout waiting for keyframe on WebRTC source sid: %s", sid)
	case <-done:
	}

	// if activePad.GetName() == selPad.GetName() {
	// 	println("WebRTC selector source is already set to sid:", sid)
	// 	return nil
	// }

	if err := sel.SetProperty("active-pad", selPad); err != nil {
		return fmt.Errorf("failed to set active pad on selector: %w", err)
	}

	return nil
}

func (gp *GstPipeline) RemoveWebRTCSourceFromSelector(sid string) error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	if gp.Closed() {
		return nil
	}

	webrtcToSelector, exists := gp.WebrtcToSelectors[sid]
	if !exists {
		return fmt.Errorf("webrtc source with sid %s does not exist", sid)
	}

	if err := webrtcToSelector.Close(gp.Pipeline); err != nil {
		return fmt.Errorf("failed to close webrtc to selector chain: %w", err)
	}

	delete(gp.WebrtcToSelectors, sid)

	if err := gp.ensureActiveSource(); err != nil {
		return fmt.Errorf("failed to ensure active source after removing source: %w", err)
	}

	return nil
}

func (gp *GstPipeline) ensureActiveSource() error {
	if gp.Closed() {
		return nil
	}

	sel := gp.RtpInputSelector
	activePad, err := sel.GetProperty("active-pad")
	if err != nil {
		return fmt.Errorf("failed to get active pad from selector: %w", err)
	}
	if activePad != nil {
		pad, ok := activePad.(*gst.Pad)
		if ok && pad.GetParentElement() != nil {
			return nil
		}
	}

	for _, webrtcToSelector := range gp.WebrtcToSelectors {
		selPad := webrtcToSelector.RtpPad
		if selPad != nil {
			if err := sel.SetProperty("active-pad", selPad); err != nil {
				return fmt.Errorf("failed to set active pad on selector: %w", err)
			}
			return nil
		}
	}

	return nil
}
