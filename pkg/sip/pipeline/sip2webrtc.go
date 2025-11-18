package pipeline

import (
	"fmt"
	"strings"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

type SipToWebRTC struct {
	// raw elements
	RtpSrc       *gst.Element
	RtcpSrc      *gst.Element
	RtpBin       *gst.Element
	Depay        *gst.Element
	Parse        *gst.Element
	Decoder      *gst.Element
	VideoConvert *gst.Element
	VideoScale   *gst.Element
	VideoRate    *gst.Element
	Vp8Enc       *gst.Element
	RtpVp8Pay    *gst.Element
	RtcpSink     *gst.Element
	RtpSink      *gst.Element

	RtpAppSrc   *app.Source
	RtpAppSink  *app.Sink
	RtcpAppSrc  *app.Source
	RtcpAppSink *app.Sink
}

func buildSipToWebRTCChain(sipPayloadType int) (*SipToWebRTC, error) {
	// appsrc with most settings done as properties
	capsStr := fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
		sipPayloadType,
	)

	rtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
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

	rtpBin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name": "sip_rtp_bin",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtpbin: %w", err)
	}

	rtcpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
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

	// jb, err := gst.NewElementWithProperties("rtpjitterbuffer", map[string]interface{}{
	// 	"name":              "sip_jitterbuffer",
	// 	"latency":           uint(100),
	// 	"do-lost":           true,
	// 	"do-retransmission": false,
	// 	"drop-on-latency":   false,
	// })
	// if err != nil {
	// 	return nil, nil, err
	// }

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
		"add-borders": false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videoscale: %w", err)
	}

	vrate, err := gst.NewElement("videorate")
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videorate: %w", err)
	}

	vp8enc, err := gst.NewElementWithProperties("vp8enc", map[string]interface{}{
		"deadline":            int(1),
		"target-bitrate":      int(3_000_000),
		"cpu-used":            int(2),
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

	rtpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "webrtc_rtp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP appsink: %w", err)
	}

	rtcpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "webrtc_rtcp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtcp appsink: %w", err)
	}

	return &SipToWebRTC{
		RtpSrc:       rtpSrc,
		RtcpSrc:      rtcpSrc,
		RtpBin:       rtpBin,
		Depay:        depay,
		Parse:        parse,
		Decoder:      dec,
		VideoConvert: vconv,
		VideoScale:   vscale,
		VideoRate:    vrate,
		Vp8Enc:       vp8enc,
		RtpVp8Pay:    rtpVp8Pay,
		RtcpSink:     rtcpSink,
		RtpSink:      rtpSink,
		RtpAppSrc:    app.SrcFromElement(rtpSrc),
		RtpAppSink:   app.SinkFromElement(rtpSink),
		RtcpAppSrc:   app.SrcFromElement(rtcpSrc),
		RtcpAppSink:  app.SinkFromElement(rtcpSink),
	}, nil
}

func (stw *SipToWebRTC) Link(gp *GstPipeline) error {
	if err := gp.addlinkChain(stw.RtpSrc); err != nil {
		return fmt.Errorf("failed to link sip to webrtc chain: %w", err)
	}

	if err := gp.addlinkChain(stw.RtpBin); err != nil {
		return fmt.Errorf("failed to link sip to webrtc chain: %w", err)
	}

	if err := gp.addlinkChain(stw.RtcpSrc); err != nil {
		return fmt.Errorf("failed to link sip to webrtc chain: %w", err)
	}

	if err := gp.addlinkChain(GstPipelineChain{
		stw.Depay,
		stw.Parse,
		stw.Decoder,
		stw.VideoConvert,
		stw.VideoScale,
		stw.VideoRate,
		stw.Vp8Enc,
		stw.RtpVp8Pay,
		stw.RtpSink,
	}...); err != nil {
		return fmt.Errorf("failed to link sip to webrtc chain: %w", err)
	}

	if err := gp.addlinkChain(stw.RtcpSink); err != nil {
		return fmt.Errorf("failed to link sip to webrtc chain: %w", err)
	}

	if err := linkPad(
		stw.RtpAppSrc.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link sip src to rtpbin recv pad: %w", err)
	}

	if err := linkPad(
		stw.RtcpAppSrc.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtcp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtcp src to rtpbin recv pad: %w", err)
	}

	stw.RtpBin.Connect("pad-added", func(rtpBin *gst.Element, pad *gst.Pad) {
		if !strings.HasPrefix(pad.GetName(), "recv_rtp_src_0_") {
			return
		}
		if err := linkPad(
			pad,
			stw.Depay.GetStaticPad("sink"),
		); err != nil {
			fmt.Printf("failed to link rtpbin src to depay sink: %v\n", err)
		}
	})

	if err := linkPad(
		stw.RtpBin.GetRequestPad("send_rtcp_src_0"),
		stw.RtcpSink.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link rtpbin rtcp src to sip sink: %w", err)
	}

	return nil
}
