package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

type SipToWebrtc struct {
	// raw elements
	SipRtpSrc     *gst.Element
	SipRtcpSrc    *gst.Element
	RtpBin        *gst.Element
	Depay         *gst.Element
	Parse         *gst.Element
	Decoder       *gst.Element
	VideoConvert  *gst.Element
	VideoScale    *gst.Element
	ResFilter     *gst.Element
	VideoConvert2 *gst.Element
	VideoRate     *gst.Element
	FpsFilter     *gst.Element
	Vp8Enc        *gst.Element
	RtpVp8Pay     *gst.Element
	SipRtcpSink   *gst.Element
	WebrtcRtpSink *gst.Element

	SipRtpAppSrc     *app.Source
	WebrtcRtpAppSink *app.Sink
	SipRtcpAppSrc    *app.Source
	SipRtcpAppSink   *app.Sink
}

func buildSipToWebRTCChain(sipPayloadType int) (*SipToWebrtc, error) {
	// appsrc with most settings done as properties
	capsStr := fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
		sipPayloadType,
	)

	sipRtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         "sip_rtp_in",
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(3), // GST_FORMAT_TIME; using the same numeric value as your launch string
		"max-bytes":    uint64(5_000_000),
		"block":        false,
		"caps":         gst.NewCapsFromString(capsStr),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP appsrc: %w", err)
	}

	sipRtcpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         "sip_rtcp_in",
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(3),
		"max-bytes":    uint64(200_000),
		"block":        false,
		"caps":         gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtcp appsrc: %w", err)
	}

	rtpBin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name": "sip_rtp_bin",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtpbin: %w", err)
	}

	depay, err := gst.NewElementWithProperties("rtph264depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtp depayloader: %w", err)
	}

	parse, err := gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP h264 parser: %w", err)
	}

	dec, err := gst.NewElementWithProperties("avdec_h264", map[string]interface{}{
		"max-threads": int(4),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP h264 decoder: %w", err)
	}

	vconv, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videoconvert: %w", err)
	}

	vscale, err := gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videoscale: %w", err)
	}

	// Force 1280x720 resolution with PAR 1:1 - this forces letterboxing for non-16:9 content
	resFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP resolution capsfilter: %w", err)
	}

	// videoconvert after scaling to ensure proper format
	vconv2, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videoconvert2: %w", err)
	}

	// Force 24fps output
	vrate, err := gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true, // Only drop frames, don't duplicate
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videorate: %w", err)
	}

	// Force 24fps in caps
	fpsFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP fps capsfilter: %w", err)
	}

	vp8enc, err := gst.NewElementWithProperties("vp8enc", map[string]interface{}{
		"deadline":            int(1),
		"target-bitrate":      int(1_500_000),
		"cpu-used":            int(4),
		"keyframe-max-dist":   int(30),
		"lag-in-frames":       int(0),
		"threads":             int(4),
		"buffer-initial-size": int(100),
		"buffer-optimal-size": int(120),
		"buffer-size":         int(150),
		"min-quantizer":       int(4),
		"max-quantizer":       int(40),
		"cq-level":            int(13),
		"error-resilient":     int(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP vp8 encoder: %w", err)
	}

	rtpVp8Pay, err := gst.NewElementWithProperties("rtpvp8pay", map[string]interface{}{
		"pt":              int(96),
		"mtu":             int(1200),
		"picture-id-mode": int(2), // 15-bit in your launch string
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtp vp8 payloader: %w", err)
	}

	webrtcRtpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "webrtc_rtp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP appsink: %w", err)
	}

	sipRtcpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "sip_rtcp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtcp appsink: %w", err)
	}

	return &SipToWebrtc{
		SipRtpSrc:        sipRtpSrc,
		SipRtcpSrc:       sipRtcpSrc,
		RtpBin:           rtpBin,
		Depay:            depay,
		Parse:            parse,
		Decoder:          dec,
		VideoConvert:     vconv,
		VideoScale:       vscale,
		ResFilter:        resFilter,
		VideoConvert2:    vconv2,
		VideoRate:        vrate,
		FpsFilter:        fpsFilter,
		Vp8Enc:           vp8enc,
		RtpVp8Pay:        rtpVp8Pay,
		SipRtcpSink:      sipRtcpSink,
		WebrtcRtpSink:    webrtcRtpSink,
		SipRtpAppSrc:     app.SrcFromElement(sipRtpSrc),
		WebrtcRtpAppSink: app.SinkFromElement(webrtcRtpSink),
		SipRtcpAppSrc:    app.SrcFromElement(sipRtcpSrc),
		SipRtcpAppSink:   app.SinkFromElement(sipRtcpSink),
	}, nil
}

func (stw *SipToWebrtc) Link(pipeline *gst.Pipeline) error {
	if err := addlinkChain(pipeline, stw.SipRtpSrc); err != nil {
		return fmt.Errorf("failed to link sip to webrtc chain: %w", err)
	}

	if err := addlinkChain(pipeline, stw.SipRtcpSrc); err != nil {
		return fmt.Errorf("failed to link sip to webrtc chain: %w", err)
	}

	if err := addlinkChain(pipeline, stw.RtpBin); err != nil {
		return fmt.Errorf("failed to link sip to webrtc chain: %w", err)
	}

	if err := addlinkChain(pipeline, GstPipelineChain{
		stw.Depay,
		stw.Parse,
		stw.Decoder,
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
		stw.Vp8Enc,
		stw.RtpVp8Pay,
		stw.WebrtcRtpSink,
	}...); err != nil {
		return fmt.Errorf("failed to link sip to webrtc chain: %w", err)
	}

	if err := addlinkChain(pipeline, stw.SipRtcpSink); err != nil {
		return fmt.Errorf("failed to link sip to webrtc chain: %w", err)
	}

	if _, err := stw.RtpBin.Connect("pad-added", func(rtpBin *gst.Element, pad *gst.Pad) {
		fmt.Printf("SIP RTPBIN PAD ADDED: %s\n", pad.GetName())
		if pad.GetName() == "send_rtp_src_0" {
			return
		}
		if err := linkPad(
			pad,
			stw.Depay.GetStaticPad("sink"),
		); err != nil {
			fmt.Printf("failed to link rtpbin src to depay sink: %v\n", err)
		}
	}); err != nil {
		return fmt.Errorf("failed to link rtpbin recv pad to depay: %w", err)
	}

	if err := linkPad(
		stw.SipRtpAppSrc.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link sip src to rtpbin recv pad: %w", err)
	}

	if err := linkPad(
		stw.SipRtcpAppSrc.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtcp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtcp src to rtpbin recv pad: %w", err)
	}

	// if err := linkPad(
	// 	stw.RtpBin.GetRequestPad("send_rtp_src_0"),
	// 	stw.RtpVp8Pay.GetStaticPad("sink"),
	// ); err != nil {
	// 	return fmt.Errorf("failed to link rtpbin rtp src to rtp vp8 pay sink: %w", err)
	// }

	if err := linkPad(
		stw.RtpBin.GetRequestPad("send_rtcp_src_0"),
		stw.SipRtcpSink.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link rtpbin rtcp src to sip sink: %w", err)
	}

	return nil
}
