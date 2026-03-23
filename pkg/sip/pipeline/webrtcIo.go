package pipeline

import (
	"fmt"
	"strings"
	"weak"

	"github.com/frostbyte73/core"
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

	connected core.Fuse
	closed    core.Fuse

	LivekitBin *gst.Element
}

var _ GstChain = (*WebrtcIo)(nil)

// Create implements [GstChain].
func (wio *WebrtcIo) Create() error {
	var err error
	wio.LivekitBin, err = gst.NewElementWithProperties("livekitbin", map[string]interface{}{
		"max-active-participants": uint(6),
		"camera":                  false,
		"microphone":              false,
		"screenshare":             false,
		"screenshare-audio":       false,
	})
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

	if _, err := wio.LivekitBin.Connect("active-speakers-changed", func(_ *gst.Element, structure *gst.Structure) {
		ptr := wiow.Value()
		if ptr != nil {
			if _, err := ptr.pipeline.LivekitController.Emit("active-speakers-changed", structure); err != nil {
				ptr.log.Errorw("Failed to emit active-speakers-changed signal from livekitbin to io manager", err)
			}
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to livekitbin active-speakers-changed signal: %w", err)
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

func (wio *WebrtcIo) binPadAdded(_ *gst.Element, pad *gst.Pad) {
	wio.log.Debugw("RTP bin pad added", "pad", pad.GetName())
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
	case livekit.TrackSource_MICROPHONE:
	default:
		wio.log.Warnw("Unknown track source in RTP pad name", nil, "session", session, "pad", padName)
		return
	}

	sinkPad := wio.pipeline.IOManager.LivekitController.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, payloadType))
	if err := LinkPad(
		pad,
		sinkPad,
	); err != nil {
		wio.log.Errorw("Failed to link webrtc rtpbin pad to io manager", err, "pad", padName, "session", session, "ssrc", ssrc, "payloadType", payloadType)
		return
	}
	pad.SetQData(QDataPadPeerKey, sinkPad)
	wio.log.Infow("Linked WebRTC RTP pad to IO Manager", "pad", padName, "session", session, "ssrc", ssrc, "payloadType", payloadType)
}

func (wio *WebrtcIo) binPadRemoved(_ *gst.Element, pad *gst.Pad) {
	wio.log.Debugw("RTP bin pad removed", "pad", pad.GetName())
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "recv_rtp_src_") {
		return
	}

	peer, ok := pad.GetQData(QDataPadPeerKey).(*gst.Pad)
	if !ok {
		wio.log.Warnw("Failed to get peer pad from QData", nil, "pad", padName)
		return
	}
	wio.pipeline.IOManager.LivekitController.ReleaseRequestPad(peer)
}

// Link implements [GstChain].
func (wio *WebrtcIo) Link() error {
	wwio := weak.Make(wio)

	if _, err := wio.LivekitBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := wwio.Value()
		if ptr != nil {
			ptr.binPadAdded(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to webrtc rtpbin pad-added signal: %w", err)
	}

	if _, err := wio.LivekitBin.Connect("pad-removed", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := wwio.Value()
		if ptr != nil {
			ptr.binPadRemoved(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to webrtc rtpbin pad-removed signal: %w", err)
	}

	return nil
}

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
