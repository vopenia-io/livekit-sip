package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

type SelectorToSip struct {
	WebrtcToSelectors map[string]*WebrtcToSelector

	RtpInputSelector *gst.Element
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

	RtcpFunnel     *gst.Element
	WebrtcRtcpSink *gst.Element

	SipRtpAppSink     *app.Sink
	WebrtcRtcpAppSink *app.Sink
}

var _ GstChain = (*SelectorToSip)(nil)

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
		WebrtcToSelectors: make(map[string]*WebrtcToSelector),
		RtpInputSelector:  rtpInputSelector,
		RtcpFunnel:        rtcpFunnel,
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

// Add implements GstChain.
func (gp *SelectorToSip) Add(pipeline *gst.Pipeline) error {
	if err := pipeline.AddMany(
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

		gp.RtcpFunnel,
		gp.WebrtcRtcpSink,
	); err != nil {
		return fmt.Errorf("failed to add SelectorToSip elements to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (gp *SelectorToSip) Link(pipeline *gst.Pipeline) error {
	if err := gst.ElementLinkMany(
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
		return fmt.Errorf("failed to link SelectorToSip video elements: %w", err)
	}

	if err := gst.ElementLinkMany(
		gp.RtcpFunnel,
		gp.WebrtcRtcpSink,
	); err != nil {
		return fmt.Errorf("failed to link SelectorToSip rtcp elements: %w", err)
	}

	return nil
}

// Close implements GstChain.
func (gp *SelectorToSip) Close(pipeline *gst.Pipeline) error {
	var errs []error
	for _, wts := range gp.WebrtcToSelectors {
		if err := wts.Close(pipeline); err != nil {
			errs = append(errs, fmt.Errorf("failed to close webrtc to selector: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors closing SelectorToSip: %v", errs)
	}

	pipeline.RemoveMany(
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

		gp.RtcpFunnel,
		gp.WebrtcRtcpSink,
	)
	return nil
}
