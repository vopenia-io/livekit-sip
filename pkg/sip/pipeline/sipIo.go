package pipeline

import (
	"fmt"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/samber/lo"
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

	SipBin *gst.Element
}

var _ GstChain = (*SipIo)(nil)

// Create implements [GstChain].
func (sio *SipIo) Create() error {
	var err error

	formatCaps := []*gst.Caps{
		gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=PCMU,clock-rate=8000"),
		// gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=PCMA,clock-rate=8000"), // TODO: fix g711-audio and then enable that back
		gst.NewCapsFromString("application/x-rtp,media=video,encoding-name=H264,clock-rate=90000"),
	}

	formats := lo.Map(formatCaps, func(caps *gst.Caps, _ int) interface{} {
		return caps
	})

	arr, err := glib.NewArray(formats)
	if err != nil {
		return fmt.Errorf("failed to create formats array: %w", err)
	}

	sio.SipBin, err = gst.NewElementWithProperties("sipbin", map[string]interface{}{
		"ip":         sio.opts.IP,
		"port-start": uint(sio.opts.PortStart),
		"port-end":   uint(sio.opts.PortEnd),
		"formats":    arr,
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP sipbin: %w", err)
	}

	return nil
}

// Add implements [GstChain].
func (sio *SipIo) Add() error {
	return sio.pipeline.Pipeline().AddMany(
		sio.SipBin,
	)
}

func (sio *SipIo) binPadAddedRecvRtpSrc(rtpbin *gst.Element, pad *gst.Pad) {
	var session, ssrc, pt uint
	if _, err := fmt.Sscanf(pad.GetName(), "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		sio.log.Warnw("Received new pad on rtpbin with unrecognized name format", err, "padName", pad.GetName())
		return
	}

	sio.log.Debugw("Received new recv RTP src pad on rtpbin", "session", session, "ssrc", ssrc, "pt", pt)

	sink := sio.pipeline.IOManager.SipController.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, pt))
	if sink == nil {
		sio.log.Warnw("Received new recv RTP src pad on rtpbin, but no matching sink pad was found on sipbin", nil, "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	if ret := pad.Link(sink); ret != gst.PadLinkOK {
		sio.log.Errorw("Failed to link new recv RTP src pad from rtpbin to sipbin sink pad", fmt.Errorf("link failed: %v", ret), "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	sio.log.Infow("Linked new recv RTP src pad from rtpbin to sipbin sink pad", "session", session, "ssrc", ssrc, "pt", pt)

	// go func() {
	switch livekit.TrackSource(session) {
	case livekit.TrackSource_CAMERA:
		if err := sio.pipeline.WebrtcIo.LivekitBin.SetProperty("camera", true); err != nil {
			sio.log.Errorw("Failed to set camera property on LiveKit bin after linking new RTP pad for camera track", err)
		}
	case livekit.TrackSource_MICROPHONE:
		if err := sio.pipeline.WebrtcIo.LivekitBin.SetProperty("microphone", true); err != nil {
			sio.log.Errorw("Failed to set microphone property on LiveKit bin after linking new RTP pad for microphone track", err)
		}
	case livekit.TrackSource_SCREEN_SHARE:
		if err := sio.pipeline.WebrtcIo.LivekitBin.SetProperty("screenshare", true); err != nil {
			sio.log.Errorw("Failed to set screenshare property on LiveKit bin after linking new RTP pad for screenshare track", err)
		}
		// most sip devices mix screenshare audio into the microphone track
		if err := sio.pipeline.WebrtcIo.LivekitBin.SetProperty("screenshare-audio", true); err != nil {
			sio.log.Errorw("Failed to set screenshare-audio property on LiveKit bin after linking new RTP pad for screenshare audio track", err)
		}
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
		if err := sio.pipeline.WebrtcIo.LivekitBin.SetProperty("screenshare-audio", true); err != nil {
			sio.log.Errorw("Failed to set screenshare-audio property on LiveKit bin after linking new RTP pad for screenshare audio track", err)
		}
	default:
		sio.log.Warnw("Received new recv RTP src pad on rtpbin with unrecognized session kind", nil, "session", session, "ssrc", ssrc, "pt", pt)
		return
	}
	// }()
}

// Link implements [GstChain].
func (sio *SipIo) Link() error {
	// link rtp in
	siow := weak.Make(sio)

	if _, err := sio.SipBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := siow.Value()
		if ptr != nil {
			ptr.binPadAddedRecvRtpSrc(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	return nil
}

// Close implements [GstChain].
func (sio *SipIo) Close() error {
	if err := sio.pipeline.Pipeline().RemoveMany(
		sio.SipBin,
	); err != nil {
		return fmt.Errorf("failed to remove SIP IO elements from pipeline: %w", err)
	}
	return nil
}
