package pipeline

import (
	"fmt"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/rtcp"
)

func (gp *GstPipeline) AddWebRTCSourceToSelector(sid string, ssrc uint32) (*WebrtcTrack, error) {
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

	if _, exists := gp.WebrtcToSip.WebrtcTracks[sid]; exists {
		return nil, fmt.Errorf("webrtc source with sid %s already exists", sid)
	}

	gp.log.Infow("Adding WebRTC source to selector", "sid", sid, "ssrc", ssrc)
	webRTCToSelector, err := CastErr[*WebrtcTrack](gp.addChain(buildWebrtcTrack(gp.log.WithComponent("webrtc_to_selector"), gp.WebrtcToSip, ssrc)))
	if err != nil {
		gp.log.Errorw("Error building WebRTC to selector chain", err)
		return nil, err
	}
	gp.log.Infow("Successfully built WebRTC to selector chain", "sid", sid, "ssrc", ssrc)

	gp.WebrtcToSip.WebrtcTracks[sid] = webRTCToSelector

	if err := gp.ensureActiveSource(); err != nil {
		return nil, fmt.Errorf("failed to ensure active source after adding new source: %w", err)
	}

	return webRTCToSelector, nil
}

func injectKeyframe(src *app.Source, ssrc uint32) error {
	pli := &rtcp.PictureLossIndication{
		MediaSSRC:  ssrc, // The track we want a keyframe for
		SenderSSRC: 0,    // Your local SSRC (0 is usually acceptable if unknown)
	}

	buf, err := pli.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal PLI: %w", err)
	}

	buffer := gst.NewBufferFromBytes(buf)
	if buffer == nil {
		return fmt.Errorf("failed to create GST buffer from RTP packet")
	}
	if r := src.PushBuffer(buffer); r != gst.FlowOK {
		return fmt.Errorf("failed to push PLI buffer to source, flow result: %s", r.String())
	}

	return nil
}

func (gp *GstPipeline) SwitchWebRTCSelectorSource(sid string) error {
	gp.mu.Lock()
	defer gp.mu.Unlock()
	return gp.switchWebRTCSelectorSource(sid)
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

	wts, exists := gp.WebrtcToSip.WebrtcTracks[sid]
	if !exists {
		return fmt.Errorf("webrtc source with sid %s does not exist", sid)
	}

	sel := gp.WebrtcToSip.InputSelector
	selPad := wts.RtpPad

	activeProp, err := sel.GetProperty("active-pad")
	if err == nil || activeProp != nil {
		activePad, ok := activeProp.(*gst.Pad)
		if !ok || activePad.GetParentElement() == nil {
			return fmt.Errorf("active pad from selector is invalid")
		}

		if activePad.GetName() == selPad.GetName() {
			gp.log.Debugw("WebRTC selector source is already set to sid", "sid", sid)
			return nil
		}
	}

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

	return gp.switchSelectorPad(wts, selPad)
}

func (gp *GstPipeline) switchSelectorPad(wt *WebrtcTrack, pad *gst.Pad) error {
	if err := validatePad(pad); err != nil {
		return fmt.Errorf("invalid pad provided for selector switch: %w", err)
	}

	// return gp.WebrtcToSip.InputSelector.SetProperty("active-pad", pad)

	done := make(chan struct{})
	timeout := time.After(2 * time.Second)

	// if err := injectKeyframe(gp.RtcpInjectorAppSrc, wt.SSRC); err != nil {
	// 	return fmt.Errorf("failed to request keyframe from webrtc source: %w", err)
	// }

	var err error

	gp.log.Debugw("Waiting for keyframe on WebRTC source", "ssrc", wt.SSRC)
	probe := pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			return gst.PadProbePass
		}

		if !buf.HasFlags(gst.BufferFlagDeltaUnit) {
			if err = gp.WebrtcToSip.InputSelector.SetProperty("active-pad", pad); err != nil {
				gp.log.Errorw("Failed to set active pad on input selector", err)
				err = fmt.Errorf("failed to set active pad on input selector: %w", err)
			}
			close(done)
			return gst.PadProbeRemove
		}
		return gst.PadProbePass
	})

	select {
	case <-timeout:
		pad.RemoveProbe(probe)
		gp.log.Errorw("Timeout waiting for keyframe on WebRTC source", nil, "ssrc", wt.SSRC)
		return fmt.Errorf("timeout waiting for keyframe on webrtc source ssrc: %d", wt.SSRC)
	case <-done:
		if err != nil {
			return err
		}
		gp.log.Infow("Switched WebRTC selector source", "ssrc", wt.SSRC)
	}
	return nil
}

func (gp *GstPipeline) RemoveWebRTCSourceFromSelector(sid string) error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	if gp.Closed() {
		return nil
	}

	webrtcTrack, exists := gp.WebrtcToSip.WebrtcTracks[sid]
	if !exists {
		return fmt.Errorf("webrtc source with sid %s does not exist", sid)
	}

	if err := webrtcTrack.Close(gp.Pipeline); err != nil {
		return fmt.Errorf("failed to close webrtc to selector chain: %w", err)
	}

	delete(gp.WebrtcToSip.WebrtcTracks, sid)

	if err := gp.ensureActiveSource(); err != nil {
		return fmt.Errorf("failed to ensure active source after removing source: %w", err)
	}

	return nil
}

func validatePad(pad *gst.Pad) error {
	if pad == nil {
		return fmt.Errorf("pad is nil")
	}
	parent := pad.GetParentElement()
	if parent == nil {
		return fmt.Errorf("pad's parent element is nil")
	}
	return nil
}

func (gp *GstPipeline) ensureActiveSource() error {
	if gp.Closed() {
		return nil
	}

	sel := gp.WebrtcToSip.InputSelector
	activePad, err := sel.GetProperty("active-pad")
	if err == nil && activePad != nil {
		pad, ok := activePad.(*gst.Pad)
		if ok && validatePad(pad) == nil {
			return nil
		}
	}

	for _, webrtcTrack := range gp.WebrtcToSip.WebrtcTracks {
		selPad := webrtcTrack.RtpPad
		if err := gp.switchSelectorPad(webrtcTrack, selPad); err != nil {
			gp.log.Errorw("Failed to switch selector pad to ssrc", err, "ssrc", webrtcTrack.SSRC)
			continue
		}
		gp.log.Debugw("Switched selector pad to ssrc", "ssrc", webrtcTrack.SSRC)
		return nil
	}

	gp.log.Infow("No available webrtc sources to set as active selector source")
	return nil
}

// func (wts *WebrtcToSelector) RequestKeyframe() error {
// 	structure := gst.NewStructure("GstForceKeyUnit")
// 	structure.SetValue("timestamp", uint64(gst.ClockTimeNone))
// 	structure.SetValue("stream-time", uint64(gst.ClockTimeNone))
// 	structure.SetValue("running-time", uint64(gst.ClockTimeNone))
// 	structure.SetValue("all-headers", true)
// 	structure.SetValue("count", uint32(1))
// 	structure.SetValue("ssrc", uint32(wts.SSRC))

// 	fmt.Printf("Requesting keyframe: %v\n", structure.Values())

// 	event := gst.NewCustomEvent(gst.EventTypeCustomDownstream, structure)

// 	if !wts.RtpBin.SendEvent(event) {
// 		return fmt.Errorf("failed to send force key unit event to webrtc source")
// 	}

// 	return nil
// }
