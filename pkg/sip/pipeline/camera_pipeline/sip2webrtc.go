package camera_pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/event"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

func NewSipToWebrtcChain(log logger.Logger, parent *CameraPipeline) *SipToWebrtc {
	return &SipToWebrtc{
		log:      log,
		pipeline: parent,
	}
}

type SipToWebrtc struct {
	pipeline *CameraPipeline
	log      logger.Logger

	RtpBin *gst.Element

	SipRtpIn *gst.Element // sip
	// rptbin
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
	WebrtcRtpOut  *gst.Element // webrtc

	SipRtcpIn *gst.Element // sip
	// rptbin
	SipRtcpOut *gst.Element // webrtc
}

var _ pipeline.GstChain[*CameraPipeline] = (*SipToWebrtc)(nil)

func (stw *SipToWebrtc) Create() error {
	var err error
	stw.RtpBin, err = gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name": "sip_rtp_bin",
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtpbin: %w", err)
	}

	stw.SipRtpIn, err = gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":         "sip_rtp_in",
		"do-timestamp": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtp sourcereader: %w", err)
	}

	stw.Depay, err = gst.NewElementWithProperties("rtph264depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtp depayloader: %w", err)
	}

	stw.Parse, err = gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP h264 parser: %w", err)
	}

	stw.Decoder, err = gst.NewElementWithProperties("avdec_h264", map[string]interface{}{
		"max-threads": int(4),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP h264 decoder: %w", err)
	}

	stw.VideoConvert, err = gst.NewElement("videoconvert")
	if err != nil {
		return fmt.Errorf("failed to create SIP videoconvert: %w", err)
	}

	stw.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP videoscale: %w", err)
	}

	// Force 1280x720 resolution with PAR 1:1 - this forces letterboxing for non-16:9 content
	stw.ResFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP resolution capsfilter: %w", err)
	}

	// videoconvert after scaling to ensure proper format
	stw.VideoConvert2, err = gst.NewElement("videoconvert")
	if err != nil {
		return fmt.Errorf("failed to create SIP videoconvert2: %w", err)
	}

	// Force 24fps output
	stw.VideoRate, err = gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true, // Only drop frames, don't duplicate
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP videorate: %w", err)
	}

	// Force 24fps in caps
	stw.FpsFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP fps capsfilter: %w", err)
	}

	stw.Vp8Enc, err = gst.NewElementWithProperties("vp8enc", map[string]interface{}{
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
		return fmt.Errorf("failed to create SIP vp8 encoder: %w", err)
	}

	stw.RtpVp8Pay, err = gst.NewElementWithProperties("rtpvp8pay", map[string]interface{}{
		"pt":              int(96),
		"mtu":             int(1200),
		"picture-id-mode": int(2), // 15-bit in your launch string
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtp vp8 payloader: %w", err)
	}

	stw.WebrtcRtpOut, err = gst.NewElementWithProperties("sinkwriter", map[string]interface{}{
		"name": "webrtc_rtp_out",
		"caps": gst.NewCapsFromString("application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96"),
	})
	if err != nil {
		return fmt.Errorf("failed to create WebRTC rtp sinkwriter: %w", err)
	}

	stw.SipRtcpIn, err = gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":         "sip_rtcp_in",
		"do-timestamp": true,
		"caps":         gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtcp sourcereader: %w", err)
	}

	stw.SipRtcpOut, err = gst.NewElementWithProperties("sinkwriter", map[string]interface{}{
		"name": "sip_rtcp_out",
		"caps": gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtcp sinkwriter: %w", err)
	}

	return nil
}

func (stw *SipToWebrtc) Add() error {
	if err := stw.pipeline.Pipeline().AddMany(
		stw.RtpBin,
		stw.SipRtpIn,
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
		stw.WebrtcRtpOut,
		stw.SipRtcpIn,
		stw.SipRtcpOut,
	); err != nil {
		return fmt.Errorf("failed to add SIP to WebRTC elements to pipeline: %w", err)
	}
	return nil
}

func (stw *SipToWebrtc) Link() error {
	// link rtp path
	if err := pipeline.LinkPad(
		stw.SipRtpIn.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtp src to rtpbin: %w", err)
	}

	// if _, err := stw.RtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
	if _, err := stw.RtpBin.Connect("pad-added", event.RegisterCallback(context.TODO(), stw.pipeline.loop, func(rtpbin *gst.Element, pad *gst.Pad) {
		stw.log.Debugw("RTPBIN PAD ADDED", "pad", pad.GetName())
		padName := pad.GetName()
		if !strings.HasPrefix(padName, "recv_rtp_src_0_") {
			return
		}
		var ssrc, payloadType uint32
		if _, err := fmt.Sscanf(padName, "recv_rtp_src_0_%d_%d", &ssrc, &payloadType); err != nil {
			stw.log.Warnw("Invalid RTP pad format", err, "pad", padName)
			return
		}
		stw.log.Infow("RTP pad added", "pad", padName, "ssrc", ssrc, "payloadType", payloadType)
		if err := pipeline.LinkPad(
			pad,
			stw.Depay.GetStaticPad("sink"),
		); err != nil {
			stw.log.Errorw("Failed to link rtpbin pad to depayloader", err)
			return
		}
		stw.log.Infow("Linked RTP pad", "pad", padName)
	})); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if err := gst.ElementLinkMany(
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
		stw.WebrtcRtpOut,
	); err != nil {
		return fmt.Errorf("failed to link sip to webrtc rtp path: %w", err)
	}

	// link rtcp path
	if err := pipeline.LinkPad(
		stw.SipRtcpIn.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtcp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtcp src to rtpbin: %w", err)
	}

	if err := pipeline.LinkPad(
		stw.RtpBin.GetRequestPad("send_rtcp_src_0"),
		stw.SipRtcpOut.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link rtpbin rtcp src to sip rtcp sink: %w", err)
	}

	return nil
}

func (stw *SipToWebrtc) Close() error {
	if err := stw.pipeline.Pipeline().RemoveMany(
		stw.RtpBin,
		stw.SipRtpIn,
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
		stw.WebrtcRtpOut,
		stw.SipRtcpIn,
		stw.SipRtcpOut,
	); err != nil {
		return fmt.Errorf("failed to remove SIP to WebRTC elements from pipeline: %w", err)
	}
	return nil
}
