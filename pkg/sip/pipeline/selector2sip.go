package pipeline

import (
	"fmt"
	"strings"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

type SelectorToSip struct {
	*basePipeline
	WebrtcToSelectors map[string]*WebrtcToSelector

	RtpInputSelector *gst.Element
	RtcpFunnel       *gst.Element
	Vp8Depay         *gst.Element
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
	RtpBin           *gst.Element
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

	rtpBin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name": "webrtc_rtp_bin",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtpbin: %w", err)
	}

	vp8depay, err := gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc vp8 depayloader: %w", err)
	}

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
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp appsink: %w", err)
	}

	return &SelectorToSip{
		basePipeline:      pipeline,
		WebrtcToSelectors: make(map[string]*WebrtcToSelector),
		RtpInputSelector:  rtpInputSelector,
		RtcpFunnel:        rtcpFunnel,
		RtpBin:            rtpBin,
		Vp8Depay:          vp8depay,
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
	if err := addlinkChain(sts.Pipeline, sts.RtpInputSelector); err != nil {
		return fmt.Errorf("failed to link selector to sip chain: %w", err)
	}

	if err := addlinkChain(sts.Pipeline, sts.RtcpFunnel); err != nil {
		return fmt.Errorf("failed to link funnel to sip chain: %w", err)
	}

	if err := addlinkChain(sts.Pipeline, sts.RtpBin); err != nil {
		return fmt.Errorf("failed to link rtpbin to sip chain: %w", err)
	}

	if err := addlinkChain(sts.Pipeline,
		sts.Vp8Depay,
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

	if err := addlinkChain(sts.Pipeline, sts.WebrtcRtcpSink); err != nil {
		return fmt.Errorf("failed to link selector to sip chain: %w", err)
	}

	if _, err := sts.RtpBin.Connect("pad-added", func(rtpBin *gst.Element, pad *gst.Pad) {
		fmt.Printf("WEBRTC RTPBIN PAD ADDED: %s\n", pad.GetName())
		if !strings.HasPrefix(pad.GetName(), "recv_rtp_src_0_") {
			return
		}
		if err := linkPad(
			pad,
			sts.Vp8Depay.GetStaticPad("sink"),
		); err != nil {
			fmt.Printf("failed to link webrtc rtp src to appsink: %v\n", err)
		}
	}); err != nil {
		return fmt.Errorf("failed to link selector to sip chain: %w", err)
	}

	if err := linkPad(
		sts.RtpInputSelector.GetStaticPad("src"),
		sts.RtpBin.GetRequestPad("recv_rtp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc input-selector to rtpbin recv pad: %w", err)
	}

	if err := linkPad(
		sts.RtcpFunnel.GetStaticPad("src"),
		sts.RtpBin.GetRequestPad("recv_rtcp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp src to rtpbin recv pad: %w", err)
	}

	if err := linkPad(
		sts.RtpBin.GetRequestPad("send_rtcp_src_0"),
		sts.WebrtcRtcpSink.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link rtpbin rtcp src to sip sink: %w", err)
	}

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

	if _, exists := sts.WebrtcToSelectors[srcID]; exists {
		return nil, fmt.Errorf("webrtc source with id %s already exists", srcID)
	}

	webRTCToSelector, err := buildWebRTCToSelectorChain(srcID)
	if err != nil {
		return nil, err
	}

	if err := webRTCToSelector.link(sts.Pipeline, sts.RtpInputSelector, sts.RtcpFunnel); err != nil {
		return nil, err
	}

	sts.WebrtcToSelectors[srcID] = webRTCToSelector

	if err := sts.ensureActiveSource(); err != nil {
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

	state := sts.Pipeline.GetCurrentState()
	switch state {
	case gst.StatePlaying, gst.StatePaused, gst.StateReady:
	default:
		return fmt.Errorf("pipeline must be in playing, paused, or ready state to switch source, current state: %s", state.String())
	}

	webrtcToSelector, exists := sts.WebrtcToSelectors[srcID]
	if !exists {
		return fmt.Errorf("webrtc source with id %s does not exist", srcID)
	}

	sel := sts.RtpInputSelector
	selPad := webrtcToSelector.WebrtcRtpSelectorPad

	if err := sel.SetProperty("active-pad", selPad); err != nil {
		return fmt.Errorf("failed to set active pad on selector: %w", err)
	}

	return nil
}

func (sts *SelectorToSip) RemoveWebRTCSourceFromSelector(srcID string) error {
	sts.mu.Lock()
	defer sts.mu.Unlock()

	if sts.Closed() {
		return nil
	}

	webrtcToSelector, exists := sts.WebrtcToSelectors[srcID]
	if !exists {
		return fmt.Errorf("webrtc source with id %s does not exist", srcID)
	}

	if err := webrtcToSelector.Close(sts.Pipeline); err != nil {
		return fmt.Errorf("failed to close webrtc to selector chain: %w", err)
	}

	delete(sts.WebrtcToSelectors, srcID)

	if err := sts.ensureActiveSource(); err != nil {
		return fmt.Errorf("failed to ensure active source after removing source: %w", err)
	}

	return nil
}

func (sts *SelectorToSip) ensureActiveSource() error {
	if sts.Closed() {
		return nil
	}

	sel := sts.RtpInputSelector
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

	for _, webrtcToSelector := range sts.WebrtcToSelectors {
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
