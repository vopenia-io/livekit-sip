package pipeline

import (
	"fmt"
	"strings"
	"weak"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/iomanager"
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

func (sio *SipIo) binPadAddedRecvRtpSrc(_ *gst.Element, pad *gst.Pad) {
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

	destPad := sio.pipeline.IOManager.SipController.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, payloadType))
	if destPad == nil {
		sio.log.Errorw("Track rejected by remote, no matching pad available in SIP IO bin", nil, "pad", fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, payloadType))
		return
	}

	if err := LinkPad(
		pad,
		destPad,
	); err != nil {
		sio.log.Errorw("Failed to link rtpbin pad to destination element", err, "rtpPad", padName, "destPad", destPad.GetName())
		return
	}

	sio.log.Infow("Linked RTP pad", "pad", padName, "ssrc", ssrc, "payloadType", payloadType, "destPad", destPad.GetName())
}

func (sio *SipIo) binPadAddedSendRtpSrc(_ *gst.Element, pad *gst.Pad) {
	sio.log.Debugw("SIP RTPBIN PAD ADDED", "pad", pad.GetName())
	padName := pad.GetName()

	if !strings.HasPrefix(padName, "send_rtp_src_") {
		return
	}

	var session uint32
	if _, err := fmt.Sscanf(padName, "send_rtp_src_%d", &session); err != nil {
		sio.log.Warnw("Invalid SIP RTP pad format", err, "pad", padName)
		return
	}

	go func() {
		var media string
		switch iomanager.SessionKind(session) {
		case iomanager.SessionKindMicrophone:
			media = "audio"
		case iomanager.SessionKindCamera:
			media = "video"
		default:
			sio.log.Warnw("Unsupported session kind", nil, "session", session)
			return
		}

		rtpSinkPad := sio.SipManager.GetRequestPad(fmt.Sprintf("sink_%s_%%u", media))
		if rtpSinkPad == nil {
			sio.log.Warnw("Track rejected by remote", nil, "pad", fmt.Sprintf("sink_%s_%%u", media))
			return
		}

		if err := LinkPad(
			pad,
			rtpSinkPad,
		); err != nil {
			sio.log.Errorw("Failed to link sip rtpbin pad to sip manager sink pad", err, "rtpPad", padName)
			return
		}
		sio.log.Infow("Linked SIP RTP pad", "pad", padName)

		var trackID uint32
		if _, err := fmt.Sscanf(rtpSinkPad.GetName(), fmt.Sprintf("sink_%s_%%d", media), &trackID); err != nil {
			sio.log.Warnw("Invalid SIP manager sink pad format", err, "pad", rtpSinkPad.GetName())
			return
		}
		go func() {
			rtcpPadName := fmt.Sprintf("send_rtcp_src_%d", session)
			rtcpPad := sio.SipRtpBin.GetRequestPad(rtcpPadName)
			sio.log.Infow("Requested RTCP pad from rtpbin", "name", fmt.Sprintf("send_rtcp_src_%d", session), "pad", rtcpPad.GetName())
			rtcpSinkPad := sio.SipManager.GetRequestPad(fmt.Sprintf("sink_rtcp_%s_%d", media, trackID))
			if err := LinkPad(
				rtcpPad,
				rtcpSinkPad,
			); err != nil {
				sio.log.Errorw("Failed to link sip rtpbin RTCP pad to SIP manager RTCP sink pad", err)
				return
			}
			sio.log.Infow("Linked SIP RTCP pad", "pad", rtcpPad.GetName())
		}()
	}()
}

func (sio *SipIo) sipPadAddedSrc(_ *gst.Element, pad *gst.Pad) {
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "src_") || strings.HasPrefix(padName, "src_rtcp_") {
		return
	}

	var (
		trackID uint32
		kind    string
	)
	if _, err := fmt.Sscanf(strings.ReplaceAll(padName, "_", " "), "src %s %d", &kind, &trackID); err != nil {
		sio.log.Warnw("Invalid SIP pad format", err, "pad", padName)
		return
	}

	var session int
	switch strings.ToLower(kind) {
	case "audio":
		session = int(iomanager.SessionKindMicrophone)
	case "video":
		session = int(iomanager.SessionKindCamera)
	default:
		sio.log.Warnw("Unsupported SIP kind", nil, "kind", kind)
		return
	}

	sio.log.Infow("SIP audio pad added", "pad", padName, "trackID", trackID, "kind", kind, "session", session)

	pname := fmt.Sprintf("recv_rtp_sink_%d", session)
	if sio.SipRtpBin.GetStaticPad(pname) != nil {
		sio.log.Warnw("Track rejected by remote, RTP pad already exists in rtpbin", nil, "pad", pname)
		return
	}

	destPad := sio.SipRtpBin.GetRequestPad(pname)
	if destPad == nil {
		sio.log.Warnw("Track rejected by remote, no RTP pad available in rtpbin", nil, "pad", fmt.Sprintf("recv_rtp_sink_%d", session))
		return
	}

	if err := LinkPad(
		pad,
		destPad,
	); err != nil {
		sio.log.Errorw("Failed to link sip manager pad to rtpbin", err)
		return
	}
	sio.log.Infow("Linked SIP audio pad", "pad", padName)
}

func (sio *SipIo) sipPadAddedSrcRtcp(_ *gst.Element, pad *gst.Pad) {
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "src_rtcp_") {
		return
	}

	var (
		trackID uint32
		kind    string
	)
	if _, err := fmt.Sscanf(strings.ReplaceAll(padName, "_", " "), "src rtcp %s %d", &kind, &trackID); err != nil {
		sio.log.Warnw("Invalid SIP RTCP pad format", err, "pad", padName)
		return
	}

	var session int
	switch strings.ToLower(kind) {
	case "audio":
		session = int(iomanager.SessionKindMicrophone)
	case "video":
		session = int(iomanager.SessionKindCamera)
	default:
		sio.log.Warnw("Unsupported SIP kind", nil, "kind", kind)
		return
	}

	sio.log.Infow("SIP audio RTCP pad added", "pad", padName, "trackID", trackID, "kind", kind, "session", session)

	pname := fmt.Sprintf("recv_rtcp_sink_%d", session)
	if sio.SipRtpBin.GetStaticPad(pname) != nil {
		sio.log.Warnw("Track rejected by remote, RTCP pad already exists in rtpbin", nil, "pad", pname)
		return
	}

	destPad := sio.SipRtpBin.GetRequestPad(fmt.Sprintf("recv_rtcp_sink_%d", session))
	if destPad == nil {
		sio.log.Warnw("Track rejected by remote, no RTCP pad available in rtpbin", nil, "pad", fmt.Sprintf("recv_rtcp_sink_%d", session))
		return
	}

	if err := LinkPad(
		pad,
		destPad,
	); err != nil {
		sio.log.Errorw("Failed to link sip manager RTCP pad to rtpbin", err)
		return
	}
	sio.log.Infow("Linked SIP audio RTCP pad", "pad", padName)
}

func (sio *SipIo) PtMap(_ *gst.Element, pt uint32) *gst.Caps {
	val, err := sio.SipManager.Emit("pt-map", pt)
	if err != nil {
		sio.log.Errorw("Failed to emit pt-map signal on SIP manager", err)
		return nil
	}
	caps, ok := val.(*gst.Caps)
	if !ok {
		sio.log.Errorw("Invalid return type from pt-map signal", nil, "type", fmt.Sprintf("%T", val))
		return nil
	}
	return caps
}

// Link implements [GstChain].
func (sio *SipIo) Link() error {
	// link rtp in
	siow := weak.Make(sio)

	if _, err := sio.SipRtpBin.Connect("request-pt-map", func(rtpbin *gst.Element, session uint32, pt uint32) *gst.Caps {
		ptr := siow.Value()
		if ptr != nil {
			return ptr.PtMap(rtpbin, pt)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin request-pt-map signal: %w", err)
	}

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
			ptr.sipPadAddedSrc(sipManager, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to sip manager pad-added signal: %w", err)
	}

	if _, err := sio.SipManager.Connect("pad-added", func(sipManager *gst.Element, pad *gst.Pad) {
		ptr := siow.Value()
		if ptr != nil {
			ptr.sipPadAddedSrcRtcp(sipManager, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to sip manager RTCP pad-added signal: %w", err)
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
