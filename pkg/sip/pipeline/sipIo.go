package pipeline

import (
	"fmt"
	"strings"
	"weak"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

func NewSipInput(log logger.Logger, parent *Pipeline, opts SipOpt) *SipIo {
	return &SipIo{
		log:      log.WithComponent("sip_input"),
		pipeline: parent,
		opts:     opts,
	}
}

type SipOpt struct {
	IP        string
	PortStart uint16
	PortEnd   uint16
}

type SipIo struct {
	log      logger.Logger
	pipeline *Pipeline

	opts SipOpt

	SipRtpBin *gst.Element

	SipManager *gst.Element
}

var _ GstChain = (*SipIo)(nil)

// Create implements [GstChain].
func (sio *SipIo) Create() error {
	var err error
	sio.SipRtpBin, err = gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name":        "sip_rtp_bin",
		"rtp-profile": int(3), // GST_RTP_PROFILE_AVPF
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtpbin: %w", err)
	}

	sio.SipManager, err = gst.NewElementWithProperties("sipmanager", map[string]interface{}{
		"name":       "sipmanager",
		"ip":         sio.opts.IP,
		"port-start": uint(sio.opts.PortStart),
		"port-end":   uint(sio.opts.PortEnd),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP connection element: %w", err)
	}

	return nil
}

// Add implements [GstChain].
func (sio *SipIo) Add() error {
	return sio.pipeline.Pipeline().AddMany(
		sio.SipRtpBin,
		sio.SipManager,
	)
}

func (sio *SipIo) binPadAddedRecvRtpSrc(rtpbin *gst.Element, pad *gst.Pad) {
	sio.log.Debugw("RTPBIN PAD ADDED", "pad", pad.GetName())
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "recv_rtp_src_") {
		return
	}
	var session, ssrc, payloadType uint32
	if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &session, &ssrc, &payloadType); err != nil {
		sio.log.Warnw("Invalid RTP pad format", err, "pad", padName)
		return
	}
	sio.log.Infow("RTP pad added", "pad", padName, "ssrc", ssrc, "payloadType", payloadType)
	if err := LinkPad(
		pad,
		sio.pipeline.SipToWebrtc.G711Opus.GetStaticPad("sink"),
	); err != nil {
		sio.log.Errorw("Failed to link rtpbin pad to depayloader", err)
		return
	}

	if err := LinkPad(
		sio.pipeline.SipToWebrtc.G711Opus.GetStaticPad("src"),
		sio.pipeline.WebrtcIo.WebrtcRtpBin.GetRequestPad("send_rtp_sink_0"),
	); err != nil {
		sio.log.Errorw("Failed to link rtp payloader to webrtc rtpbin", err)
		return
	}

	sio.log.Infow("Linked RTP pad", "pad", padName)
}

func (sio *SipIo) binPadAddedSendRtpSrc(rtpbin *gst.Element, pad *gst.Pad) {
	sio.log.Debugw("WEBRTC RTPBIN PAD ADDED", "pad", pad.GetName())
	padName := pad.GetName()

	if !strings.HasPrefix(padName, "send_rtp_src_") {
		return
	}

	var session uint32
	if _, err := fmt.Sscanf(padName, "send_rtp_src_%d", &session); err != nil {
		sio.log.Warnw("Invalid SIP RTP pad format", err, "pad", padName)
		return
	}

	sio.log.Infow("SIP RTP pad added", "pad", padName, "session", session)

	if err := LinkPad(
		pad,
		sio.SipManager.GetRequestPad("sink_audio_%u"),
	); err != nil {
		sio.log.Errorw("Failed to link sip rtpbin pad to sinkwriter", err)
		return
	}
	sio.log.Infow("Linked SIP RTP pad", "pad", padName)
}

func (sio *SipIo) sipPadAddedAudioSrc(sipManager *gst.Element, pad *gst.Pad) {
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "src_audio_") || strings.HasSuffix(padName, "_rtcp") {
		return
	}

	var trackID uint32
	if _, err := fmt.Sscanf(padName, "src_audio_%d", &trackID); err != nil {
		sio.log.Warnw("Invalid SIP pad format", err, "pad", padName)
		return
	}

	sio.log.Infow("SIP audio pad added", "pad", padName, "trackID", trackID)

	if err := LinkPad(
		pad,
		sio.SipRtpBin.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d", trackID)),
	); err != nil {
		sio.log.Errorw("Failed to link sip manager pad to rtpbin", err)
		return
	}
	sio.log.Infow("Linked SIP audio pad", "pad", padName)
}

// Link implements [GstChain].
func (sio *SipIo) Link() error {
	// link rtp in
	siow := weak.Make(sio)
	if _, err := sio.SipRtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := siow.Value()
		if ptr != nil {
			ptr.binPadAddedRecvRtpSrc(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if _, err := sio.SipManager.Connect("pad-added", func(sipManager *gst.Element, pad *gst.Pad) {
		ptr := siow.Value()
		if ptr != nil {
			ptr.sipPadAddedAudioSrc(sipManager, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to sip manager pad-added signal: %w", err)
	}

	// link rtp out
	if _, err := sio.SipRtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := siow.Value()
		if ptr != nil {
			ptr.binPadAddedSendRtpSrc(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to sip rtpbin pad-added signal: %w", err)
	}

	return nil
}

// Close implements [GstChain].
func (sio *SipIo) Close() error {
	if err := sio.pipeline.Pipeline().RemoveMany(
		sio.SipRtpBin,
		sio.SipManager,
	); err != nil {
		return fmt.Errorf("failed to remove SIP IO elements from pipeline: %w", err)
	}
	return nil
}
