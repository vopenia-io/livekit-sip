package pipeline

import (
	"fmt"
	"strings"
	"weak"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

func NewWebrtcIo(log logger.Logger, parent *Pipeline) *WebrtcIo {
	return &WebrtcIo{
		log:      log.WithComponent("webrtc_io"),
		pipeline: parent,
	}
}

type WebrtcIo struct {
	pipeline *Pipeline
	log      logger.Logger

	LkRoom *gst.Element

	WebrtcRtpBin *gst.Element
}

var _ GstChain = (*WebrtcIo)(nil)

const VP8CAPS = "application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96"

var webrtcCaps = map[uint]string{
	96: VP8CAPS,
}

// Create implements [GstChain].
func (wio *WebrtcIo) Create() error {
	var err error
	wio.WebrtcRtpBin, err = gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name":        "webrtc_rtp_bin",
		"rtp-profile": int(3),
	})
	if err != nil {
		return fmt.Errorf("failed to create WebRTC rtpbin: %w", err)
	}

	wio.LkRoom, err = gst.NewElementWithProperties("lkroom", map[string]interface{}{
		"name":      "webrtc_lkroom",
		"auto-join": false,
	})
	if err != nil {
		return fmt.Errorf("failed to create WebRTC lkroom: %w", err)
	}

	return nil
}

// Add implements [GstChain].
func (wio *WebrtcIo) Add() error {
	if err := wio.pipeline.Pipeline().AddMany(
		wio.WebrtcRtpBin,
		wio.LkRoom,
	); err != nil {
		return fmt.Errorf("failed to add webrtc io to pipeline: %w", err)
	}
	return nil
}

func (wio *WebrtcIo) binPadAddedRecvRtpSrc(rtpbin *gst.Element, pad *gst.Pad) {
	wio.log.Debugw("RTPBIN PAD ADDED", "pad", pad.GetName())
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "recv_rtp_src_") {
		return
	}
	var session, ssrc, payloadType uint32
	if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &session, &ssrc, &payloadType); err != nil {
		wio.log.Warnw("Invalid RTP pad format", err, "pad", padName)
		return
	}
	wio.log.Infow("RTP pad added", "pad", padName, "ssrc", ssrc, "payloadType", payloadType)
	if err := LinkPad(
		pad,
		wio.pipeline.WebrtcToSip.RtpOpusDepay.GetStaticPad("sink"),
	); err != nil {
		wio.log.Errorw("Failed to link rtpbin pad to depayloader", err)
		return
	}

	if err := LinkPad(
		wio.pipeline.WebrtcToSip.RtpPcmuPay.GetStaticPad("src"),
		wio.pipeline.SipIo.SipRtpBin.GetRequestPad("send_rtp_sink_0"),
	); err != nil {
		wio.log.Errorw("Failed to link rtp payloader to sip rtpbin", err)
		return
	}

	wio.log.Infow("Linked RTP pad", "pad", padName)
}

func (wio *WebrtcIo) binPadAddedSendRtpSrc(rtpbin *gst.Element, pad *gst.Pad) {
	wio.log.Debugw("WEBRTC RTPBIN PAD ADDED", "pad", pad.GetName())
	padName := pad.GetName()

	if !strings.HasPrefix(padName, "send_rtp_src_") {
		return
	}

	var session uint32
	if _, err := fmt.Sscanf(padName, "send_rtp_src_%d", &session); err != nil {
		wio.log.Warnw("Invalid SIP RTP pad format", err, "pad", padName)
		return
	}

	wio.log.Infow("SIP RTP pad added", "pad", padName, "session", session)

	if err := LinkPad(
		pad,
		wio.LkRoom.GetRequestPad("sink_2_%u"),
	); err != nil {
		wio.log.Errorw("Failed to link sip rtpbin pad to sinkwriter", err)
		return
	}
	wio.log.Infow("Linked SIP RTP pad", "pad", padName)
}

func (wio *WebrtcIo) lkroomSrcRtp(lkroom *gst.Element, pad *gst.Pad) {
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "src_") || strings.HasSuffix(padName, "_rtcp") {
		return
	}

	var kind, trackID uint32
	if _, err := fmt.Sscanf(padName, "src_%d_%d", &kind, &trackID); err != nil {
		wio.log.Warnw("Invalid SIP pad format", err, "pad", padName)
		return
	}

	if kind != uint32(livekit.TrackSource_MICROPHONE) {
		wio.log.Warnw("Unsupported track kind for SIP audio", nil, "kind", kind, "pad", padName)
		return
	}

	wio.log.Infow("SIP audio pad added", "pad", padName, "trackID", trackID)

	if err := LinkPad(
		pad,
		wio.WebrtcRtpBin.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d", trackID)),
	); err != nil {
		wio.log.Errorw("Failed to link sip manager pad to rtpbin", err)
		return
	}
	wio.log.Infow("Linked SIP audio pad", "pad", padName)
}

// Link implements [GstChain].
func (wio *WebrtcIo) Link() error {
	wwio := weak.Make(wio)

	if _, err := wio.WebrtcRtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := wwio.Value()
		if ptr != nil {
			ptr.binPadAddedRecvRtpSrc(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to webrtc rtpbin pad-added signal: %w", err)
	}

	if _, err := wio.LkRoom.Connect("pad-added", func(lkroom *gst.Element, pad *gst.Pad) {
		ptr := wwio.Value()
		if ptr != nil {
			ptr.lkroomSrcRtp(lkroom, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to lkroom pad-added signal: %w", err)
	}

	// link rtp out
	if _, err := wio.WebrtcRtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := wwio.Value()
		if ptr != nil {
			ptr.binPadAddedSendRtpSrc(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to webrtc rtpbin pad-added signal: %w", err)
	}

	return nil
}

// Close implements [GstChain].
func (wio *WebrtcIo) Close() error {
	if err := wio.pipeline.Pipeline().RemoveMany(
		wio.WebrtcRtpBin,
		wio.LkRoom,
	); err != nil {
		return fmt.Errorf("errors occurred while closing webrtc io: %w", err)
	}

	return nil
}
