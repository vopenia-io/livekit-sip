package camera_pipeline

import (
	"fmt"
	"strings"
	"sync"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

const VP8CAPS = "application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96,rtcp-fb-nack-pli=true"

var webrtcCaps = map[uint]string{
	96: VP8CAPS,
}

type WebrtcToSip struct {
	log          logger.Logger
	mu           sync.Mutex
	WebrtcTracks map[uint32]*WebrtcTrack

	RtpBin *gst.Element

	RtpFunnel *gst.Element
	//rtpbin
	InputSelector *gst.Element
	// Vp8Depay      *gst.Element
	Vp8Dec        *gst.Element
	VideoConvert  *gst.Element
	VideoScale    *gst.Element
	ResFilter     *gst.Element
	VideoConvert2 *gst.Element
	VideoRate     *gst.Element
	FpsFilter     *gst.Element
	I420Filter    *gst.Element
	X264Enc       *gst.Element
	Parse         *gst.Element
	RtpH264Pay    *gst.Element
	SipRtpSink    *gst.Element

	RtcpFunnel *gst.Element
	//rtpbin
	WebrtcRtcpSink *gst.Element

	SipRtpAppSink     *app.Sink
	WebrtcRtcpAppSink *app.Sink
}

var _ pipeline.GstChain = (*WebrtcToSip)(nil)

func buildSelectorToSipChain(log logger.Logger, sipOutPayloadType int) (*WebrtcToSip, error) {
	rtpbin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"autoremove":         true,
		"do-lost":            true,
		"do-sync-event":      true,
		"drop-on-latency":    false,
		"latency":            uint64(0),
		"rtcp-sync-interval": uint64(1000000000), // 1s
		"rtp-profile":        int(3),             // RTP_PROFILE_AVPF
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtpbin: %w", err)
	}

	rtpFunnel, err := gst.NewElementWithProperties("rtpfunnel", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp funnel: %w", err)
	}

	inputSelector, err := gst.NewElementWithProperties("input-selector", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc input selector: %w", err)
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
		"async":        false, // Don't wait for preroll - live pipeline
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc appsink: %w", err)
	}

	rtcpFunnel, err := gst.NewElementWithProperties("funnel", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp funnel: %w", err)
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

	return &WebrtcToSip{
		log:          log,
		WebrtcTracks: make(map[uint32]*WebrtcTrack),

		RtpBin: rtpbin,

		RtpFunnel: rtpFunnel,
		// rtpbin
		InputSelector: inputSelector,
		// Vp8Depay:      vp8depay,
		Vp8Dec:        vp8dec,
		VideoConvert:  vconv,
		VideoScale:    vscale,
		ResFilter:     resFilter,
		VideoConvert2: vconv2,
		VideoRate:     vrate,
		FpsFilter:     fpsFilter,
		I420Filter:    i420Filter,
		X264Enc:       x264enc,
		Parse:         parse,
		RtpH264Pay:    rtpPay,
		SipRtpSink:    sipRtpSink,

		RtcpFunnel: rtcpFunnel,
		// rtpbin
		WebrtcRtcpSink: webrtcRtcpSink,

		SipRtpAppSink:     app.SinkFromElement(sipRtpSink),
		WebrtcRtcpAppSink: app.SinkFromElement(webrtcRtcpSink),
	}, nil
}

// Add implements GstChain.
func (wts *WebrtcToSip) Add(pipeline *gst.Pipeline) error {
	if err := pipeline.AddMany(
		wts.RtpBin,

		wts.RtpFunnel,
		// rtpbin
		wts.InputSelector,
		// wts.Vp8Depay,
		wts.Vp8Dec,
		wts.VideoConvert,
		wts.VideoScale,
		wts.ResFilter,
		wts.VideoConvert2,
		wts.VideoRate,
		wts.FpsFilter,
		wts.I420Filter,
		wts.X264Enc,
		wts.Parse,
		wts.RtpH264Pay,
		wts.SipRtpSink,

		wts.RtcpFunnel,
		// rtpbin
		wts.WebrtcRtcpSink,
	); err != nil {
		return fmt.Errorf("failed to add SelectorToSip elements to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (wts *WebrtcToSip) Link(p *gst.Pipeline) error {
	if _, err := wts.RtpBin.Connect("request-pt-map", func(self *gst.Element, session uint, pt uint) *gst.Caps {
		caps, ok := webrtcCaps[pt]
		if !ok {
			return nil
		}
		wts.log.Debugw("RTPBIN requested PT map", "pt", pt, "caps", caps)
		return gst.NewCapsFromString(caps)
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin request-pt-map signal: %w", err)
	}

	if err := pipeline.LinkPad(
		wts.RtpFunnel.GetStaticPad("src"),
		wts.RtpBin.GetRequestPad("recv_rtp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link WebrtcToSip rtp funnel to rtpbin: %w", err)
	}

	if _, err := wts.RtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		wts.log.Debugw("RTPBIN PAD ADDED", "pad", pad.GetName())
		padName := pad.GetName()
		if !strings.HasPrefix(padName, "recv_rtp_src_") {
			return
		}
		var sessionID, ssrc, payloadType uint32
		if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &sessionID, &ssrc, &payloadType); err != nil {
			wts.log.Warnw("Invalid RTP pad format", err, "pad", padName)
			return
		}
		wts.log.Infow("RTP pad added", "pad", padName, "sessionID", sessionID, "ssrc", ssrc, "payloadType", payloadType)

		wts.mu.Lock()
		defer wts.mu.Unlock()

		track, ok := wts.WebrtcTracks[ssrc]
		if !ok {
			wts.log.Warnw("No WebRTC track found for SSRC", nil, "ssrc", ssrc)
			return
		}

		if err := track.LinkParent(wts, pad); err != nil {
			wts.log.Errorw("Failed to link RTP pad to WebRTC track", err, "pad", padName, "ssrc", ssrc)
			return
		}
		wts.log.Infow("Linked RTP pad", "pad", padName)
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if err := gst.ElementLinkMany(
		wts.InputSelector,
		// wts.Vp8Depay,
		wts.Vp8Dec,
		wts.VideoConvert,
		wts.VideoScale,
		wts.ResFilter,
		wts.VideoConvert2,
		wts.VideoRate,
		wts.FpsFilter,
		wts.I420Filter,
		wts.X264Enc,
		wts.Parse,
		wts.RtpH264Pay,
		wts.SipRtpSink,
	); err != nil {
		return fmt.Errorf("failed to link SelectorToSip video elements: %w", err)
	}

	if err := pipeline.LinkPad(
		wts.RtcpFunnel.GetStaticPad("src"),
		wts.RtpBin.GetRequestPad("recv_rtcp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link SelectorToSip rtcp funnel to rtpbin: %w", err)
	}

	if err := pipeline.LinkPad(
		wts.RtpBin.GetRequestPad("send_rtcp_src_0"),
		wts.WebrtcRtcpSink.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link SelectorToSip rtcp rtpbin to webrtc rtcp sink: %w", err)
	}

	return nil
}

// Close implements GstChain.
func (wts *WebrtcToSip) Close(pipeline *gst.Pipeline) error {
	var errs []error
	for _, track := range wts.WebrtcTracks {
		if err := track.Close(pipeline); err != nil {
			errs = append(errs, fmt.Errorf("failed to close webrtc to selector: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors closing SelectorToSip: %v", errs)
	}

	pipeline.RemoveMany(
		wts.RtpBin,

		wts.RtpFunnel,
		// rtpbin
		wts.InputSelector,
		// wts.Vp8Depay,
		wts.Vp8Dec,
		wts.VideoConvert,
		wts.VideoScale,
		wts.ResFilter,
		wts.VideoConvert2,
		wts.VideoRate,
		wts.FpsFilter,
		wts.I420Filter,
		wts.X264Enc,
		wts.Parse,
		wts.RtpH264Pay,
		wts.SipRtpSink,

		wts.RtcpFunnel,
		// rtpbin
		wts.WebrtcRtcpSink,
	)
	return nil
}
