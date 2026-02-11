package pipeline

import (
	"fmt"
	"strings"
	"weak"

	"github.com/go-gst/go-glib/glib"
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

	RtpDtmlDepay *gst.Element
	FakeSink     *gst.Element
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

	sio.RtpDtmlDepay, err = gst.NewElement("rtpdtmfdepay")
	if err != nil {
		return fmt.Errorf("failed to create DTMF depayloader element: %w", err)
	}

	sio.FakeSink, err = gst.NewElementWithProperties("fakesink", map[string]interface{}{
		"name":  "dtmf_fakesink",
		"sync":  false,
		"async": false,
	})
	if err != nil {
		return fmt.Errorf("failed to create fakesink element: %w", err)
	}

	return nil
}

// Add implements [GstChain].
func (sio *SipIo) Add() error {
	return sio.pipeline.Pipeline().AddMany(
		sio.SipRtpBin,
		sio.SipManager,
		sio.RtpDtmlDepay,
		sio.FakeSink,
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

	caps := sio.PtMap(sio.SipRtpBin, payloadType)
	if caps == nil {
		sio.log.Warnw("No payload type mapping found for RTP pad", nil, "pad", padName, "session", session, "payloadType", payloadType)
		return
	}

	encVal, err := caps.GetStructureAt(0).GetValue("encoding-name")
	if err != nil {
		sio.log.Warnw("Failed to get encoding name from caps", err, "caps", caps.String())
		return
	}

	enc, ok := encVal.(string)
	if !ok {
		sio.log.Warnw("Encoding name in caps is not a string", nil, "caps", caps.String(), "type", fmt.Sprintf("%T", encVal))
		return
	}

	sio.log.Infow("RTP pad added", "pad", padName, "ssrc", ssrc, "payloadType", payloadType, "encoding", enc, "caps", caps.String())

	var destPad *gst.Pad
	var srcPad *gst.Pad
	switch strings.ToLower(enc) {
	case "pcmu", "pcma":
		destPad = sio.pipeline.SipToWebrtc.G711OpusDtmf.GetStaticPad("sink")
		srcPad = sio.pipeline.SipToWebrtc.G711OpusDtmf.GetStaticPad("src")
	case "h264":
		destPad = sio.pipeline.SipToWebrtc.H264Vp8.GetStaticPad("sink")
		srcPad = sio.pipeline.SipToWebrtc.H264Vp8.GetStaticPad("src")
	case "telephone-event":
		destPad = sio.RtpDtmlDepay.GetStaticPad("sink")
	default:
		sio.log.Warnw("Unsupported payload type", nil, "payloadType", payloadType, "encoding", enc)
		return
	}

	if destPad != nil {
		if destPad.IsLinked() {
			sio.log.Warnw("Destination pad is already linked, cannot link to new RTP pad", nil, "pad", destPad.GetName())
			return
		}

		if err := LinkPad(
			pad,
			destPad,
		); err != nil {
			sio.log.Errorw("Failed to link rtpbin pad to destination element", err)
			return
		}

		sio.log.Infow("Linked RTP pad", "pad", padName, "destination", destPad.GetName())
	}

	if srcPad != nil {
		if srcPad.IsLinked() {
			sio.log.Debugw("G711OpusDtmf src pad is already linked, cannot link to new RTP pad", "pad", srcPad.GetName())
			return
		}
		if err := LinkPad(
			srcPad,
			sio.pipeline.WebrtcIo.WebrtcRtpBin.GetRequestPad(fmt.Sprintf("send_rtp_sink_%d", session)),
		); err != nil {
			sio.log.Errorw("Failed to link rtp payloader to webrtc rtpbin", err)
			return
		}

		sio.log.Infow("Linked G711OpusDtmf to WebRTC RTP bin", "srcPad", srcPad.GetName(), "destPad", fmt.Sprintf("send_rtp_sink_%d", session))
	}
}

func (sio *SipIo) binPadAddedSendRtSrcCaps(pad *gst.Pad, session uint32) {
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "send_rtp_src_") {
		return
	}

	caps := pad.CurrentCaps()
	if caps == nil {
		sio.log.Warnw("No caps found on RTP pad", nil, "pad", pad.GetName())
		return
	}

	structure := caps.GetStructureAt(0)
	mediaVal, err := structure.GetValue("media")
	if err != nil {
		sio.log.Warnw("Failed to get media from caps", err, "caps", caps.String())
		return
	}
	media, ok := mediaVal.(string)
	if !ok {
		sio.log.Warnw("Media in caps is not a string", nil, "caps", caps.String(), "type", fmt.Sprintf("%T", mediaVal))
		return
	}

	sio.log.Infow("SIP RTP pad added", "pad", padName, "session", session, "media", media, "caps", caps.String())

	switch strings.ToLower(media) {
	case "audio":
		media = "audio"
	case "video":
		media = "video"
	default:
		sio.log.Warnw("Unsupported media type", nil, "media", media)
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
}

func (sio *SipIo) binSendRtpSrcUpdatedCaps(pad *gst.Pad) {
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "send_rtp_src_") {
		return
	}

	if !pad.IsLinked() {
		sio.log.Warnw("RTP pad is not linked", nil, "pad", padName)
		return
	}

	caps := pad.PeerQueryCaps(nil)
	if caps == nil {
		sio.log.Warnw("No caps found on RTP pad", nil, "pad", pad.GetName())
		return
	}

	structure := caps.GetStructureAt(0)
	mediaVal, err := structure.GetValue("media")
	if err != nil {
		sio.log.Warnw("Failed to get media from caps", err, "caps", caps.String())
		return
	}
	media, ok := mediaVal.(string)
	if !ok {
		sio.log.Warnw("Media in caps is not a string", nil, "caps", caps.String(), "type", fmt.Sprintf("%T", mediaVal))
		return
	}

	payloadVal, err := structure.GetValue("payload")
	if err != nil {
		sio.log.Warnw("Failed to get payload from caps", err, "caps", caps.String())
		return
	}

	payload, ok := payloadVal.(int)
	if !ok {
		sio.log.Warnw("Payload in caps is not an int", nil, "caps", caps.String(), "type", fmt.Sprintf("%T", payloadVal))
		return
	}

	sio.log.Infow("RTP pad caps updated", "pad", padName, "media", media, "payload", payload, "caps", caps.String())

	switch strings.ToLower(media) {
	case "audio":
	case "video":
		if err := sio.pipeline.WebrtcToSip.Vp8H264.SetProperty("h264-pt", uint(payload)); err != nil {
			sio.log.Errorw("Failed to set H264 payload type", err)
			return
		}
		sio.log.Infow("Set H264 payload type", "payload", payload)
	default:
		sio.log.Warnw("Unsupported media type", nil, "media", media)
		return
	}
}

func (sio *SipIo) binPadAddedSendRtpSrc(_ *gst.Element, pad *gst.Pad) {
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

	wsio := weak.Make(sio)
	var (
		hnd glib.SignalHandle
		err error
	)
	if hnd, err = pad.Connect("notify::caps", func(padVal any, _ any) {
		var pad *gst.Pad
		switch v := padVal.(type) {
		case *gst.Pad:
			pad = v
		case *gst.GhostPad:
			pad = v.Pad
		default:
			return
		}

		ptr := wsio.Value()
		if ptr != nil {
			ptr.binPadAddedSendRtSrcCaps(pad, session)
			pad.HandlerDisconnect(hnd)
		}
	}); err != nil {
		sio.log.Errorw("Failed to connect to caps notify signal on RTP pad", err, "pad", padName)
		return
	}

	if _, err := pad.Connect("notify::caps", func(padVal any, _ any) {
		var pad *gst.Pad
		switch v := padVal.(type) {
		case *gst.Pad:
			pad = v
		case *gst.GhostPad:
			pad = v.Pad
		default:
			return
		}

		ptr := wsio.Value()
		if ptr != nil {
			ptr.binSendRtpSrcUpdatedCaps(pad)
		}
	}); err != nil {
		sio.log.Errorw("Failed to connect to caps notify signal on RTP pad", err, "pad", padName)
		return
	}
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

	sio.log.Infow("SIP audio pad added", "pad", padName, "trackID", trackID, "kind", kind)

	if err := LinkPad(
		pad,
		sio.SipRtpBin.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d", trackID)),
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

	sio.log.Infow("SIP audio RTCP pad added", "pad", padName, "trackID", trackID, "kind", kind)

	if err := LinkPad(
		pad,
		sio.SipRtpBin.GetRequestPad(fmt.Sprintf("recv_rtcp_sink_%d", trackID)),
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
	if err := gst.ElementLinkMany(
		sio.RtpDtmlDepay,
		sio.FakeSink,
	); err != nil {
		return fmt.Errorf("failed to link SIP IO elements: %w", err)
	}

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
		sio.RtpDtmlDepay,
		sio.FakeSink,
	); err != nil {
		return fmt.Errorf("failed to remove SIP IO elements from pipeline: %w", err)
	}
	return nil
}
