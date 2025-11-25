package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

type SelectorToSip struct {
	WebrtcToSelectors map[uint64]*WebrtcToSelector
	SrcIDToSessionID  map[string]uint64
	idCounter         atomic.Uint64

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

func (gp *SelectorToSip) Link(pipeline *gst.Pipeline) error {

	if err := addlinkChain(pipeline, gp.RecvRtpBin); err != nil {
		return fmt.Errorf("failed to link rtpbin to sip chain: %w", err)
	}

	if err := addlinkChain(pipeline,
		gp.RtpInputSelector,
		gp.Vp8Dec,
		gp.VideoConvert,
		gp.VideoScale,
		gp.ResFilter,
		gp.VideoConvert2,
		gp.VideoRate,
		gp.FpsFilter,
		gp.I420Filter,
		gp.X264Enc,
		gp.Parse,
		gp.RtpH264Pay,
		gp.SipRtpSink,
	); err != nil {
		return fmt.Errorf("failed to link selector to sip chain: %w", err)
	}

	if err := addlinkChain(pipeline,
		gp.RtcpFunnel,
		gp.WebrtcRtcpSink,
	); err != nil {
		return fmt.Errorf("failed to link selector to sip chain: %w", err)
	}

	if _, err := gp.RecvRtpBin.Connect("pad-added", func(rtpBin *gst.Element, pad *gst.Pad) {
		fmt.Printf("WEBRTC RTPBIN PAD ADDED: %s\n", pad.GetName())
		padName := pad.GetName()
		if !strings.HasPrefix(padName, "recv_rtp_src_") {
			return
		}

		var sessionID, ssrc, pt uint64
		if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &sessionID, &ssrc, &pt); err != nil {
			fmt.Printf("failed to parse session id from rtpbin pad name %s: %v\n", padName, err)
			return
		}
		fmt.Printf("Parsed sessionID=%d, ssrc=%d, pt=%d from pad %s\n", sessionID, ssrc, pt, padName)
		webrtcToSelector, exists := gp.WebrtcToSelectors[sessionID]
		if !exists {
			fmt.Printf("no webrtc to selector found for session id %d\n", sessionID)
			return
		}

		if err := linkPad(
			pad,
			webrtcToSelector.RtpVp8Depay.GetStaticPad("sink"),
		); err != nil {
			fmt.Printf("failed to link webrtc rtp src to vp8 depay: %v\n", err)
			return
		}
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

func (gp *GstPipeline) AddWebRTCSourceToSelector(srcID string) (*WebrtcToSelector, error) {
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

	sessionID := gp.idCounter.Add(1)
	gp.SrcIDToSessionID[srcID] = sessionID

	if _, exists := gp.WebrtcToSelectors[sessionID]; exists {
		return nil, fmt.Errorf("webrtc source with id %d already exists", sessionID)
	}

	webRTCToSelector, err := buildWebRTCToSelectorChain(sessionID)
	if err != nil {
		return nil, err
	}

	if err := webRTCToSelector.link(gp.Pipeline, gp.SelectorToSip); err != nil {
		return nil, err
	}

	gp.WebrtcToSelectors[sessionID] = webRTCToSelector

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

func (gp *GstPipeline) switchWebRTCSelectorSource(srcID string) error {
	if gp.Closed() {
		return fmt.Errorf("pipeline is closed")
	}

	state := gp.Pipeline.GetCurrentState()
	switch state {
	case gst.StatePlaying, gst.StatePaused, gst.StateReady:
	default:
		return fmt.Errorf("pipeline must be in playing, paused, or ready state to switch source, current state: %s", state.String())
	}

	sessionID, ok := gp.SrcIDToSessionID[srcID]
	if !ok {
		return fmt.Errorf("no session id found for source id %s", srcID)
	}

	webrtcToSelector, exists := gp.WebrtcToSelectors[sessionID]
	if !exists {
		return fmt.Errorf("webrtc source with id %s does not exist", srcID)
	}

	sel := gp.RtpInputSelector
	selPad := webrtcToSelector.WebrtcRtpSelectorPad

	done := make(chan struct{})
	go selPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBlock, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			fmt.Println("Buffer is nil, continuing to wait for keyframe")
			return gst.PadProbeOK
		}

		if !buf.HasFlags(gst.BufferFlagDeltaUnit) {
			fmt.Println("Got a keyframe!")
			sel.SetProperty("active-pad", selPad)
			close(done)
			return gst.PadProbeRemove
		}
		fmt.Println("Not a keyframe, waiting...")
		return gst.PadProbeOK
	})

	sinkPad := webrtcToSelector.RtpVp8Depay.GetStaticPad("sink")
	if !sinkPad.IsLinked() {
		return fmt.Errorf("webrtc rtp depayloader sink pad is not linked")
	}
	rtpBinSrcPad := sinkPad.GetPeer()
	defer rtpBinSrcPad.Unref()
	structure := gst.NewStructure("GstForceKeyUnit")
	structure.SetValue("all-headers", true)
	event := gst.NewCustomEvent(gst.EventTypeCustomUpstream, structure)
	fmt.Println("Sending force key unit event to webrtc source")
	if !rtpBinSrcPad.SendEvent(event) {
		fmt.Println("Failed to send force key unit event to webrtc source")
		return errors.New("failed to send force key unit event to webrtc source")
	} else {
		fmt.Println("Sent force key unit event to webrtc source")
	}

	<-done

	// if err := sel.SetProperty("active-pad", selPad); err != nil {
	// 	return fmt.Errorf("failed to set active pad on selector: %w", err)
	// }

	return nil
}

func (gp *GstPipeline) RemoveWebRTCSourceFromSelector(srcID string) error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	if gp.Closed() {
		return nil
	}

	sessionID, ok := gp.SrcIDToSessionID[srcID]
	if !ok {
		return fmt.Errorf("no session id found for source id %s", srcID)
	}

	webrtcToSelector, exists := gp.WebrtcToSelectors[sessionID]
	if !exists {
		return fmt.Errorf("webrtc source with id %s does not exist", srcID)
	}

	if err := webrtcToSelector.Close(gp.Pipeline); err != nil {
		return fmt.Errorf("failed to close webrtc to selector chain: %w", err)
	}

	delete(gp.WebrtcToSelectors, sessionID)
	delete(gp.SrcIDToSessionID, srcID)

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
		selPad := webrtcToSelector.WebrtcRtpSelectorPad
		if selPad != nil {
			if err := sel.SetProperty("active-pad", selPad); err != nil {
				return fmt.Errorf("failed to set active pad on selector: %w", err)
			}
			return nil
		}
	}

	return nil
}
