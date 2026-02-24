package pipeline

import (
	"fmt"
	"strings"
	"weak"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/iomanager"
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

	connected core.Fuse
	closed    core.Fuse

	LivekitBin *gst.Element
}

var _ GstChain = (*WebrtcIo)(nil)

// Create implements [GstChain].
func (wio *WebrtcIo) Create() error {
	var err error
	wio.LivekitBin, err = gst.NewElementWithProperties("livekitbin", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("failed to create livekitbin: %w", err)
	}

	wiow := weak.Make(wio)
	if _, err := wio.LivekitBin.Connect("connected", func(_ *gst.Element) {
		ptr := wiow.Value()
		if ptr != nil {
			ptr.connected.Break()
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to livekitbin connected signal: %w", err)
	}

	if _, err := wio.LivekitBin.Connect("closed", func(_ *gst.Element) {
		ptr := wiow.Value()
		if ptr != nil {
			ptr.closed.Break()
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to livekitbin closed signal: %w", err)
	}

	return nil
}

// Add implements [GstChain].
func (wio *WebrtcIo) Add() error {
	if err := wio.pipeline.Pipeline().AddMany(
		wio.LivekitBin,
	); err != nil {
		return fmt.Errorf("failed to add webrtc io to pipeline: %w", err)
	}
	return nil
}

func (wio *WebrtcIo) binPadAddedRecvRtpSrc(_ *gst.Element, pad *gst.Pad) {
	wio.log.Debugw("RTPBIN PAD ADDED", "pad", pad.GetName())
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "recv_rtp_src_") {
		return
	}

	var session, ssrc, payloadType int
	if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &session, &ssrc, &payloadType); err != nil {
		wio.log.Warnw("Invalid RTP pad format", err, "pad", padName)
		return
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_CAMERA:
		session = int(iomanager.SessionKindCamera)
	case livekit.TrackSource_MICROPHONE:
		session = int(iomanager.SessionKindMicrophone)
	default:
		wio.log.Warnw("Unknown track source in RTP pad name", nil, "session", session, "pad", padName)
		return
	}

	sinkPad := wio.pipeline.IOManager.LkController.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, payloadType))
	if err := LinkPad(
		pad,
		sinkPad,
	); err != nil {
		wio.log.Errorw("Failed to link webrtc rtpbin pad to io manager", err, "pad", padName, "session", session, "ssrc", ssrc, "payloadType", payloadType)
		return
	}
	wio.log.Infow("Linked WebRTC RTP pad to IO Manager", "pad", padName, "session", session, "ssrc", ssrc, "payloadType", payloadType)
}

// Link implements [GstChain].
func (wio *WebrtcIo) Link() error {
	wwio := weak.Make(wio)

	if _, err := wio.LivekitBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := wwio.Value()
		if ptr != nil {
			ptr.binPadAddedRecvRtpSrc(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to webrtc rtpbin pad-added signal: %w", err)
	}

	return nil
}

// Close implements [GstChain].
func (wio *WebrtcIo) Close() error {
	if err := wio.pipeline.Pipeline().RemoveMany(
		wio.LivekitBin,
	); err != nil {
		return fmt.Errorf("errors occurred while closing webrtc io: %w", err)
	}

	return nil
}

func (wio *WebrtcIo) Connected() <-chan struct{} {
	if wio == nil {
		return nil
	}
	return wio.connected.Watch()
}

func (wio *WebrtcIo) Closed() <-chan struct{} {
	if wio == nil {
		return nil
	}
	return wio.closed.Watch()
}
