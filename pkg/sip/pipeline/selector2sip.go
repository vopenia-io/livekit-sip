package pipeline

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

type SelectorToSip struct {
	*basePipeline
	WebrtcToSelectors map[uint64]*WebrtcToSelector
	SrcIDToSessionID  map[string]uint64
	idCounter         atomic.Uint64
	currentActiveSrcID string // Track the currently active source to prevent redundant switches

	RecvRtpBin       *gst.Element
	RtpInputSelector *gst.Element
	RtcpFunnel       *gst.Element
	Vp8Dec           *gst.Element
	VideoConvert     *gst.Element
	VideoScale       *gst.Element
	ResFilter        *gst.Element
	VideoConvert2    *gst.Element
	VideoRate        *gst.Element
	FpsFilter        *gst.Element
	I420Filter       *gst.Element
	X264Enc          *gst.Element
	Parse            *gst.Element
	RtpH264Pay       *gst.Element
	SipRtpSink       *gst.Element
	WebrtcRtcpSink   *gst.Element

	SipRtpAppSink     *app.Sink
	WebrtcRtcpAppSink *app.Sink
}

func buildSelectorToSipChain(sipOutPayloadType int) (*SelectorToSip, error) {
	pipeline, err := newBasePipeline()
	if err != nil {
		return nil, fmt.Errorf("failed to create selector to sip pipeline: %w", err)
	}

	rtpInputSelector, err := gst.NewElementWithProperties("input-selector", map[string]interface{}{
		"name":          "webrtc_rtp_sel",
		"sync-streams":  false,
		"cache-buffers": false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc input selector: %w", err)
	}

	rtcpFunnel, err := gst.NewElementWithProperties("funnel", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp funnel: %w", err)
	}

	recvRtpBin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtpbin: %w", err)
	}

	// rtpFunnel, err := gst.NewElementWithProperties("funnel", map[string]interface{}{})
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create webrtc rtp funnel: %w", err)
	// }

	// vp8depay, err := gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
	// 	"request-keyframe": true,
	// })
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create webrtc vp8 depayloader: %w", err)
	// }

	vp8dec, err := gst.NewElement("vp8dec")
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc vp8 decoder: %w", err)
	}

	vconv, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videoconvert: %w", err)
	}

	// Scale to 720p - videoscale will letterbox automatically when add-borders=true
	vscale, err := gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videoscale: %w", err)
	}

	// Force 1280x720 resolution with PAR 1:1 - this forces letterboxing for non-16:9 content
	resFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc resolution capsfilter: %w", err)
	}

	// videoconvert after scaling to ensure proper format
	vconv2, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videoconvert2: %w", err)
	}

	// Force 24fps output
	vrate, err := gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true, // Only drop frames, don't duplicate
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videorate: %w", err)
	}

	// Force 24fps in caps
	fpsFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc fps capsfilter: %w", err)
	}

	// caps filter: video/x-raw,format=I420
	i420Filter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,format=I420"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc i420 capsfilter: %w", err)
	}

	x264enc, err := gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"bitrate":        int(1500),
		"key-int-max":    int(30),
		"bframes":        int(0),
		"rc-lookahead":   int(0),
		"sliced-threads": true,
		"sync-lookahead": int(0),
		"tune":           0x00000004, // GST_X264_ENC_TUNE_ZERO_LATENCY
		"speed-preset":   7,          // GST_X264_ENC_PRESET_SUPERFAST
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc x264 encoder: %w", err)
	}

	parse, err := gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc h264 parser: %w", err)
	}

	rtpPay, err := gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"pt":              int(sipOutPayloadType),
		"mtu":             int(1200),
		"config-interval": int(1),
		"aggregate-mode":  int(0), // zero-latency
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp h264 payloader: %w", err)
	}

	sipRtpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "sip_rtp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
		"async":        false, // Don't wait for preroll - live pipeline
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc appsink: %w", err)
	}

	webrtcRtcpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "webrtc_rtcp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
		"async":        false, // Don't wait for preroll - live pipeline
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp appsink: %w", err)
	}

	return &SelectorToSip{
		basePipeline:      pipeline,
		WebrtcToSelectors: make(map[uint64]*WebrtcToSelector),
		SrcIDToSessionID:  make(map[string]uint64),
		RtpInputSelector:  rtpInputSelector,
		RtcpFunnel:        rtcpFunnel,
		RecvRtpBin:        recvRtpBin,
		Vp8Dec:            vp8dec,
		VideoConvert:      vconv,
		VideoScale:        vscale,
		ResFilter:         resFilter,
		VideoConvert2:     vconv2,
		VideoRate:         vrate,
		FpsFilter:         fpsFilter,
		I420Filter:        i420Filter,
		X264Enc:           x264enc,
		Parse:             parse,
		RtpH264Pay:        rtpPay,
		SipRtpSink:        sipRtpSink,
		WebrtcRtcpSink:    webrtcRtcpSink,
		SipRtpAppSink:     app.SinkFromElement(sipRtpSink),
		WebrtcRtcpAppSink: app.SinkFromElement(webrtcRtcpSink),
	}, nil
}

func (sts *SelectorToSip) Link() error {
	sts.mu.Lock()
	defer sts.mu.Unlock()

	if sts.Closed() {
		return fmt.Errorf("pipeline is closed")
	}

	if err := addlinkChain(sts.Pipeline, sts.RecvRtpBin); err != nil {
		return fmt.Errorf("failed to link rtpbin to sip chain: %w", err)
	}

	if err := addlinkChain(sts.Pipeline,
		sts.RtpInputSelector,
		sts.Vp8Dec,
		sts.VideoConvert,
		sts.VideoScale,
		sts.ResFilter,
		sts.VideoConvert2,
		sts.VideoRate,
		sts.FpsFilter,
		sts.I420Filter,
		sts.X264Enc,
		sts.Parse,
		sts.RtpH264Pay,
		sts.SipRtpSink,
	); err != nil {
		return fmt.Errorf("failed to link selector to sip chain: %w", err)
	}

	if err := addlinkChain(sts.Pipeline,
		sts.RtcpFunnel,
		sts.WebrtcRtcpSink,
	); err != nil {
		return fmt.Errorf("failed to link selector to sip chain: %w", err)
	}

	if _, err := sts.RecvRtpBin.Connect("pad-added", func(rtpBin *gst.Element, pad *gst.Pad) {
		padName := pad.GetName()
		fmt.Printf("[%s] ====== WEBRTC RTPBIN PAD ADDED: %s ======\n", time.Now().Format("15:04:05.000"), padName)

		// Only process recv_rtp_src pads (RTP output from rtpbin)
		// Ignore sink pads and RTCP pads
		if !strings.HasPrefix(padName, "recv_rtp_src_") {
			fmt.Printf("[%s] Ignoring non-RTP-source pad: %s\n", time.Now().Format("15:04:05.000"), padName)
			return
		}

		var sessionID, ssrc, pt uint64
		n, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &sessionID, &ssrc, &pt)
		if err != nil || n != 3 {
			fmt.Printf("[%s] ERROR: Failed to parse rtpbin pad name %s: %v (matched %d fields)\n", time.Now().Format("15:04:05.000"), padName, err, n)
			return
		}
		fmt.Printf("[%s] Parsed: sessionID=%d, ssrc=%d, pt=%d from pad %s\n", time.Now().Format("15:04:05.000"), sessionID, ssrc, pt, padName)

		webrtcToSelector, exists := sts.WebrtcToSelectors[sessionID]
		if !exists {
			fmt.Printf("[%s] ERROR: No WebRTC source found for session ID %d\n", time.Now().Format("15:04:05.000"), sessionID)
			fmt.Printf("[%s] Available sessions: ", time.Now().Format("15:04:05.000"))
			for sid := range sts.WebrtcToSelectors {
				fmt.Printf("%d ", sid)
			}
			fmt.Println()
			return
		}

		depayPad := webrtcToSelector.RtpVp8Depay.GetStaticPad("sink")
		fmt.Printf("[%s] Linking rtpbin pad %s to depay pad %s\n", time.Now().Format("15:04:05.000"), padName, depayPad.GetName())

		if err := linkPad(pad, depayPad); err != nil {
			fmt.Printf("[%s] ERROR: Failed to link pads: %v\n", time.Now().Format("15:04:05.000"), err)
			return
		}

		fmt.Printf("[%s] ✓ Successfully linked session %d (SSRC %d) to VP8 depayloader\n", time.Now().Format("15:04:05.000"), sessionID, ssrc)

		// if err := linkPad(
		// 	webrtcToSelector.RtpQueue.GetStaticPad("src"),
		// 	sts.RtpInputSelector.GetRequestPad(fmt.Sprintf("sink_%d", sessionID)),
		// ); err != nil {
		// 	fmt.Printf("failed to link webrtc rtp src to appsink: %v\n", err)
		// 	return
		// }
		// if err := linkPad(
		// 	sts.RtpBin.GetRequestPad(fmt.Sprintf("send_rtcp_src_%d", sessionID)),
		// 	sts.RtcpFunnel.GetRequestPad(fmt.Sprintf("sink_%d", sessionID)),
		// ); err != nil {
		// 	fmt.Printf("failed to link rtpbin rtcp src to sip sink: %v\n", err)
		// 	return
		// }
	}); err != nil {
		return fmt.Errorf("failed to link selector to sip chain: %w", err)
	}

	// if _, err := sts.RtpInputSelector.Connect("notify::active-pad", func(sel *gst.Element, pspec *glib.ParamSpec) {
	// 	activePadP, err := sel.GetProperty("active-pad")
	// 	if err != nil {
	// 		fmt.Printf("failed to get active pad from input-selector: %v\n", err)
	// 		return
	// 	}
	// 	activePad, ok := activePadP.(*gst.Pad)
	// 	if !ok {
	// 		fmt.Println("input-selector active-pad is not a pad")
	// 		return
	// 	}
	// 	fmt.Println("Input-selector switched to pad:", activePad.GetName())
	// }); err != nil {
	// 	return fmt.Errorf("failed to connect to input-selector notify::active-pad: %w", err)
	// }

	// if _, err := sts.RtpBin.Connect("pad-added", func(rtpBin *gst.Element, pad *gst.Pad) {
	// 	fmt.Printf("WEBRTC RTPBIN PAD ADDED: %s\n", pad.GetName())
	// 	if pad.GetName() == "send_rtp_src_0" {
	// 		return
	// 	}
	// 	if err := linkPad(
	// 		pad,
	// 		sts.RtpFunnel.GetRequestPad("sink_%u"),
	// 	); err != nil {
	// 		fmt.Printf("failed to link webrtc rtp src to appsink: %v\n", err)
	// 		return
	// 	}
	// }); err != nil {
	// 	return fmt.Errorf("failed to link selector to sip chain: %w", err)
	// }

	// if err := linkPad(
	// 	sts.RtpInputSelector.GetStaticPad("src"),
	// 	sts.RtpBin.GetRequestPad("recv_rtp_sink_0"),
	// ); err != nil {
	// 	return fmt.Errorf("failed to link webrtc input-selector to rtpbin recv pad: %w", err)
	// }

	// if err := linkPad(
	// 	sts.RtcpFunnel.GetStaticPad("src"),
	// 	sts.RtpBin.GetRequestPad("recv_rtcp_sink_0"),
	// ); err != nil {
	// 	return fmt.Errorf("failed to link webrtc rtcp src to rtpbin recv pad: %w", err)
	// }

	// if err := linkPad(
	// 	sts.RtpBin.GetRequestPad("send_rtcp_src_0"),
	// 	sts.WebrtcRtcpSink.GetStaticPad("sink"),
	// ); err != nil {
	// 	return fmt.Errorf("failed to link rtpbin rtcp src to sip sink: %w", err)
	// }

	return nil
}

func (sts *SelectorToSip) AddWebRTCSourceToSelector(srcID string) (*WebrtcToSelector, error) {
	sts.mu.Lock()
	defer sts.mu.Unlock()

	if sts.Closed() {
		return nil, fmt.Errorf("pipeline is closed")
	}

	state := sts.Pipeline.GetCurrentState()

	switch state {
	case gst.StatePlaying, gst.StateReady, gst.StatePaused:
	default:
		return nil, fmt.Errorf("pipeline must be in playing, ready, or paused state to add source, current state: %s", state.String())
	}

	sessionID := sts.idCounter.Add(1)
	sts.SrcIDToSessionID[srcID] = sessionID

	if _, exists := sts.WebrtcToSelectors[sessionID]; exists {
		return nil, fmt.Errorf("webrtc source with id %d already exists", sessionID)
	}

	webRTCToSelector, err := buildWebRTCToSelectorChain(sessionID)
	if err != nil {
		return nil, err
	}

	if err := webRTCToSelector.link(sts); err != nil {
		return nil, err
	}

	sts.WebrtcToSelectors[sessionID] = webRTCToSelector

	if _, err := sts.ensureActiveSource(); err != nil {
		return nil, fmt.Errorf("failed to ensure active source after adding new source: %w", err)
	}

	return webRTCToSelector, nil
}

func (sts *SelectorToSip) SwitchWebRTCSelectorSource(srcID string) error {
	sts.mu.Lock()
	defer sts.mu.Unlock()
	return sts.switchWebRTCSelectorSource(srcID)
}

func (sts *SelectorToSip) switchWebRTCSelectorSource(srcID string) error {
	if sts.Closed() {
		return fmt.Errorf("pipeline is closed")
	}

	// Check if we're already on this source to avoid redundant switches
	if sts.currentActiveSrcID == srcID {
		fmt.Printf("[%s] ⏭️  Skipping switch to %s - already active\n", time.Now().Format("15:04:05.000"), srcID)
		return nil
	}

	state := sts.Pipeline.GetCurrentState()
	switch state {
	case gst.StatePlaying, gst.StatePaused, gst.StateReady:
	default:
		return fmt.Errorf("pipeline must be in playing, paused, or ready state to switch source, current state: %s", state.String())
	}

	sessionID, ok := sts.SrcIDToSessionID[srcID]
	if !ok {
		fmt.Printf("[%s] ERROR: No session ID found for source %s\n", time.Now().Format("15:04:05.000"), srcID)
		fmt.Printf("[%s] Available source mappings:\n", time.Now().Format("15:04:05.000"))
		for sid, sessID := range sts.SrcIDToSessionID {
			fmt.Printf("  %s → session %d\n", sid, sessID)
		}
		return fmt.Errorf("no session id found for source id %s", srcID)
	}

	webrtcToSelector, exists := sts.WebrtcToSelectors[sessionID]
	if !exists {
		fmt.Printf("[%s] ERROR: WebRTC source with session ID %d does not exist\n", time.Now().Format("15:04:05.000"), sessionID)
		return fmt.Errorf("webrtc source with id %s does not exist", srcID)
	}

	fmt.Printf("[%s] === Switching active speaker to source %s (session %d) ===\n", time.Now().Format("15:04:05.000"), srcID, sessionID)

	sel := sts.RtpInputSelector
	selPad := webrtcToSelector.WebrtcRtpSelectorPad

	// Install non-blocking probe that switches on next keyframe
	// This allows the pipeline to continue flowing while waiting for keyframe
	go selPad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			return gst.PadProbePass
		}

		// Check if this is a keyframe (non-delta frame)
		if !buf.HasFlags(gst.BufferFlagDeltaUnit) {
			fmt.Printf("[%s] Got keyframe from source %s (session %d), switching selector\n", time.Now().Format("15:04:05.000"), srcID, sessionID)
			if err := sel.SetProperty("active-pad", selPad); err != nil {
				fmt.Printf("[%s] ERROR: Failed to set active pad: %v\n", time.Now().Format("15:04:05.000"), err)
			} else {
				fmt.Printf("[%s] Successfully switched to source %s (session %d)\n", time.Now().Format("15:04:05.000"), srcID, sessionID)
				sts.currentActiveSrcID = srcID // Update current active source
			}
			return gst.PadProbeRemove
		}
		return gst.PadProbePass
	})

	fmt.Printf("[%s] Installed keyframe probe for source %s (session %d), waiting for keyframe...\n", time.Now().Format("15:04:05.000"), srcID, sessionID)
	// NOTE: Keyframe request should be sent via LiveKit SDK before calling this function
	// The probe will automatically switch when a keyframe arrives

	return nil
}

func (sts *SelectorToSip) RemoveWebRTCSourceFromSelector(srcID string) error {
	sts.mu.Lock()
	defer sts.mu.Unlock()

	if sts.Closed() {
		return nil
	}

	sessionID, ok := sts.SrcIDToSessionID[srcID]
	if !ok {
		return fmt.Errorf("no session id found for source id %s", srcID)
	}

	webrtcToSelector, exists := sts.WebrtcToSelectors[sessionID]
	if !exists {
		return fmt.Errorf("webrtc source with id %s does not exist", srcID)
	}

	fmt.Printf("[%s] === Removing WebRTC source: %s (session %d) ===\n", time.Now().Format("15:04:05.000"), srcID, sessionID)

	// Check if we're removing the currently active source
	sel := sts.RtpInputSelector
	activePadObj, err := sel.GetProperty("active-pad")
	isActiveSource := false
	if err == nil && activePadObj != nil {
		if activePad, ok := activePadObj.(*gst.Pad); ok {
			// Compare the active pad with the pad being removed
			if activePad == webrtcToSelector.WebrtcRtpSelectorPad {
				isActiveSource = true
				fmt.Printf("[%s] ⚠️  Removing ACTIVE source %s (session %d), need to switch to another participant\n", time.Now().Format("15:04:05.000"), srcID, sessionID)
			}
		}
	}

	// If removing active source, switch to another one BEFORE removing
	if isActiveSource && len(sts.WebrtcToSelectors) > 1 {
		fmt.Printf("[%s] Switching to another source before removal...\n", time.Now().Format("15:04:05.000"))
		// Find another source to switch to
		for otherSrcID, otherSessionID := range sts.SrcIDToSessionID {
			if otherSessionID != sessionID {
				fmt.Printf("[%s] Switching to source %s (session %d)\n", time.Now().Format("15:04:05.000"), otherSrcID, otherSessionID)
				// Switch immediately without waiting for keyframe since we're about to lose video anyway
				if otherSelector, ok := sts.WebrtcToSelectors[otherSessionID]; ok {
					if err := sel.SetProperty("active-pad", otherSelector.WebrtcRtpSelectorPad); err != nil {
						fmt.Printf("[%s] ERROR: Failed to switch to alternate source: %v\n", time.Now().Format("15:04:05.000"), err)
					} else {
						fmt.Printf("[%s] ✓ Switched to alternate source %s (session %d)\n", time.Now().Format("15:04:05.000"), otherSrcID, otherSessionID)
						sts.currentActiveSrcID = otherSrcID // Update current active source
					}
				}
				break
			}
		}
	}

	if err := webrtcToSelector.Close(sts.Pipeline); err != nil {
		return fmt.Errorf("failed to close webrtc to selector chain: %w", err)
	}

	delete(sts.WebrtcToSelectors, sessionID)
	delete(sts.SrcIDToSessionID, srcID)

	fmt.Printf("[%s] ✓ Removed source %s (session %d), %d sources remaining\n", time.Now().Format("15:04:05.000"), srcID, sessionID, len(sts.WebrtcToSelectors))

	// Ensure there's still an active source
	newActiveSrcID, err := sts.ensureActiveSource()
	if err != nil {
		return fmt.Errorf("failed to ensure active source after removing source: %w", err)
	}

	// If we switched to a new source, we need to trigger it (caller should request keyframe)
	if newActiveSrcID != "" {
		fmt.Printf("[%s] 📢 Switched to new active source: %s (caller should request keyframe)\n", time.Now().Format("15:04:05.000"), newActiveSrcID)
	}

	return nil
}

func (sts *SelectorToSip) ensureActiveSource() (string, error) {
	if sts.Closed() {
		return "", nil
	}

	sel := sts.RtpInputSelector
	activePadObj, err := sel.GetProperty("active-pad")
	if err != nil {
		return "", fmt.Errorf("failed to get active pad from selector: %w", err)
	}

	// Check if current active pad is valid
	hasValidActivePad := false
	if activePadObj != nil {
		if pad, ok := activePadObj.(*gst.Pad); ok && pad != nil {
			// Check if this pad still belongs to an existing source
			for _, webrtcToSelector := range sts.WebrtcToSelectors {
				if pad == webrtcToSelector.WebrtcRtpSelectorPad {
					hasValidActivePad = true
					fmt.Printf("[%s] Current active pad is valid (session %d)\n", time.Now().Format("15:04:05.000"), webrtcToSelector.ID)
					break
				}
			}
		}
	}

	if hasValidActivePad {
		return "", nil // No change needed
	}

	// No valid active pad, pick the first available source
	fmt.Printf("[%s] No valid active source, selecting from %d available sources\n", time.Now().Format("15:04:05.000"), len(sts.WebrtcToSelectors))
	for srcID, sessionID := range sts.SrcIDToSessionID {
		webrtcToSelector, ok := sts.WebrtcToSelectors[sessionID]
		if !ok {
			continue
		}
		selPad := webrtcToSelector.WebrtcRtpSelectorPad
		if selPad != nil {
			startTime := time.Now()
			fmt.Printf("[%s] Auto-selecting source %s (session %d) as active\n", startTime.Format("15:04:05.000"), srcID, sessionID)

			// Use the SAME mechanism as manual switching - wait for keyframe before switching
			// This ensures smooth transitions just like when switching between 2 participants
			fmt.Printf("[%s] Installing keyframe probe for auto-selected source %s (session %d), waiting for keyframe...\n", time.Now().Format("15:04:05.000"), srcID, sessionID)

			frameCount := 0
			switched := false

			// Add timeout: if no keyframe arrives within 1 second, switch anyway
			go func() {
				time.Sleep(1 * time.Second)
				if !switched {
					elapsed := time.Since(startTime).Milliseconds()
					fmt.Printf("[%s] ⚠️  Timeout: No keyframe received after %dms, switching anyway to avoid black screen\n",
						time.Now().Format("15:04:05.000"), elapsed)
					if err := sel.SetProperty("active-pad", selPad); err != nil {
						fmt.Printf("[%s] ERROR: Failed to set active pad on selector: %v\n", time.Now().Format("15:04:05.000"), err)
					} else {
						fmt.Printf("[%s] ✓ Active source set to %s (session %d) (timeout fallback)\n", time.Now().Format("15:04:05.000"), srcID, sessionID)
						sts.currentActiveSrcID = srcID // Update current active source
					}
					switched = true
				}
			}()

			go selPad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
				buf := info.GetBuffer()
				if buf == nil {
					return gst.PadProbePass
				}

				frameCount++
				isKeyframe := !buf.HasFlags(gst.BufferFlagDeltaUnit)

				// Log every frame for first 10 frames or until keyframe
				if frameCount <= 10 || isKeyframe {
					elapsed := time.Since(startTime).Milliseconds()
					fmt.Printf("[%s] Frame %d from %s: keyframe=%v (elapsed: %dms)\n",
						time.Now().Format("15:04:05.000"), frameCount, srcID, isKeyframe, elapsed)
				}

				// Check if this is a keyframe (non-delta frame)
				if isKeyframe && !switched {
					switched = true
					elapsed := time.Since(startTime).Milliseconds()
					fmt.Printf("[%s] ✓ Keyframe received from auto-selected source %s (session %d) after %dms and %d frames, switching selector\n",
						time.Now().Format("15:04:05.000"), srcID, sessionID, elapsed, frameCount)
					if err := sel.SetProperty("active-pad", selPad); err != nil {
						fmt.Printf("[%s] ERROR: Failed to set active pad on selector: %v\n", time.Now().Format("15:04:05.000"), err)
					} else {
						fmt.Printf("[%s] ✓ Active source set to %s (session %d)\n", time.Now().Format("15:04:05.000"), srcID, sessionID)
						sts.currentActiveSrcID = srcID // Update current active source
					}
					return gst.PadProbeRemove
				}
				return gst.PadProbePass
			})

			return srcID, nil // Return the newly selected source ID
		}
	}

	if len(sts.WebrtcToSelectors) > 0 {
		fmt.Printf("[%s] ⚠️  Warning: No valid selector pads found among %d sources\n", time.Now().Format("15:04:05.000"), len(sts.WebrtcToSelectors))
	}

	return "", nil
}
