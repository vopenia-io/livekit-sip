package sip

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/media-sdk/h264"
	mrtp "github.com/livekit/media-sdk/rtp"
	sdpv1 "github.com/livekit/media-sdk/sdp"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

var mainLoop *glib.MainLoop

func init() {
	gst.Init(nil)

	mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)
	_ = mainLoop
}

type SipPipeline interface {
	pipeline.GstPipeline
	RtpSrc() *app.Source
	RtpSink() *app.Sink
	RtcpSrc() *app.Source
	RtcpSink() *app.Sink
	Configure(media *sdpv2.SDPMedia) error
}

type PipelineFactory interface {
	CreateVideoPipeline(opts *MediaOptions) (SipPipeline, error)
}

type VideoStatus int

const (
	VideoStatusClosed VideoStatus = iota
	VideoStatusStopped
	VideoStatusStarted
)

func (vs VideoStatus) String() string {
	switch vs {
	case VideoStatusClosed:
		return "closed"
	case VideoStatusStopped:
		return "stopped"
	case VideoStatusStarted:
		return "started"
	default:
		return "unknown"
	}
}

func NewVideoManager(log logger.Logger, ctx context.Context, opts *MediaOptions, factory PipelineFactory) (*VideoManager, error) {
	// Allocate RTP/RTCP port pair according to RFC 3550 (RTCP on RTP+1)
	rtpConn, rtcpConn, err := mrtp.ListenUDPPortPair(opts.Ports.Start, opts.Ports.End, opts.IP)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP port pair for RTP/RTCP: %w", err)
	}

	v := &VideoManager{
		log:      log,
		ctx:      ctx,
		opts:     opts,
		rtpConn:  newUDPConn(log.WithComponent("video-rtp"), rtpConn),
		rtcpConn: newUDPConn(log.WithComponent("video-rtcp"), rtcpConn),
		io:       NewMediaIO(ctx, opts.MediaTimeout),
		factory:  factory,
		status:   VideoStatusStopped,
	}

	v.log.Infow("video manager created")

	return v, nil
}

type VideoManager struct {
	mu       sync.Mutex
	log      logger.Logger
	ctx      context.Context
	opts     *MediaOptions
	rtpConn  *udpConn
	rtcpConn *udpConn
	status   VideoStatus
	pipeline SipPipeline
	Media    *sdpv2.SDPMedia
	factory  PipelineFactory
	io       *MediaIO
	RtpIn    *MediaInput
	RtpOut   *MediaOutput
	RtcpIn   *MediaInput
	RtcpOut  *MediaOutput
}

func (v *VideoManager) RtpPort() int {
	return v.rtpConn.LocalAddr().(*net.UDPAddr).Port
}

func (v *VideoManager) RtcpPort() int {
	return v.rtcpConn.LocalAddr().(*net.UDPAddr).Port
}

func (v *VideoManager) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.status == VideoStatusClosed {
		return fmt.Errorf("video manager already closed")
	}
	v.log.Debugw("closing video manager")
	if v.pipeline != nil {
		if err := v.pipeline.Close(); err != nil {
			return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
		}
	}
	if err := v.rtpConn.Close(); err != nil {
		return fmt.Errorf("failed to close RTP connection: %w", err)
	}
	if err := v.rtcpConn.Close(); err != nil {
		return fmt.Errorf("failed to close RTCP connection: %w", err)
	}
	return nil
}

func (v *VideoManager) Direction() sdpv2.Direction {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.Media == nil {
		return sdpv2.DirectionInactive
	}
	return v.Media.Direction
}

func (v *VideoManager) Codec() *sdpv2.Codec {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.Media == nil {
		return nil
	}
	return v.Media.Codec
}

func (v *VideoManager) Status() VideoStatus {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.status
}

func (v *VideoManager) SupportedCodecs() []*sdpv2.Codec {
	//TODO: make is dynamic.
	c := sdpv1.CodecByName(h264.SDPName)
	if c == nil {
		return []*sdpv2.Codec{}
	}
	codec, err := (&sdpv2.Codec{}).Builder().SetCodec(c).Build()
	if err != nil {
		return []*sdpv2.Codec{}
	}
	return []*sdpv2.Codec{
		codec,
	}
}

func isMedia(media *sdpv2.SDPMedia) bool {
	if media == nil {
		return false
	}
	if media.Port == 0 {
		return false
	}
	if media.Disabled {
		return false
	}
	if media.Codec == nil {
		return false
	}
	return true
}

func (v *VideoManager) mediaOK(newMedia *sdpv2.SDPMedia) bool {
	isOld := isMedia(v.Media)
	isNew := isMedia(newMedia)
	if !isOld && !isNew {
		return true
	}
	if !isOld || !isNew {
		return false
	}
	if v.Media.Port != newMedia.Port {
		return false
	}
	if v.Media.RTCPPort != newMedia.RTCPPort {
		return false
	}
	if v.Media.Direction != newMedia.Direction {
		return false
	}
	if v.Media.Codec.PayloadType != newMedia.Codec.PayloadType {
		return false
	}
	return true
}

func (v *VideoManager) resetIO() error {
	if err := v.io.Stop(); err != nil {
		return fmt.Errorf("failed to stop media IO: %w", err)
	}
	if v.RtpIn != nil {
		if err := v.io.RemoveInput(v.RtpIn); err != nil {
			return fmt.Errorf("failed to remove RTP media input: %w", err)
		}
		if err := v.RtpIn.Dst.(*GstWriter).Close(); err != nil {
			v.log.Warnw("failed to close RTP GstWriter", err)
		}
		v.RtpIn = nil
	}
	if v.RtcpIn != nil {
		if err := v.io.RemoveInput(v.RtcpIn); err != nil {
			return fmt.Errorf("failed to remove RTCP media input: %w", err)
		}
		if err := v.RtcpIn.Dst.(*GstWriter).Close(); err != nil {
			v.log.Warnw("failed to close RTCP GstWriter", err)
		}
		v.RtcpIn = nil
	}
	if v.RtpOut != nil {
		if err := v.io.RemoveOutput(v.RtpOut); err != nil {
			return fmt.Errorf("failed to remove RTP media output: %w", err)
		}
		if err := v.RtpOut.Src.(*GstReader).Close(); err != nil {
			v.log.Warnw("failed to close RTP GstReader", err)
		}
		v.RtpOut = nil
	}
	if v.RtcpOut != nil {
		if err := v.io.RemoveOutput(v.RtcpOut); err != nil {
			return fmt.Errorf("failed to remove RTCP media output: %w", err)
		}
		if err := v.RtcpOut.Src.(*GstReader).Close(); err != nil {
			v.log.Warnw("failed to close RTCP GstReader", err)
		}
		v.RtcpOut = nil
	}
	return nil
}

func (v *VideoManager) setupIO(remote netip.Addr, media *sdpv2.SDPMedia) error {
	if media.Direction.IsRecv() {
		rtpSource := v.pipeline.RtpSrc()
		if rtpSource == nil {
			return fmt.Errorf("RTP source is nil")
		}
		mi, err := NewMediaInput(v.ctx, rtpSource, v.rtpConn)
		if err != nil {
			return fmt.Errorf("failed to create RTP media input: %w", err)
		}
		if err := v.io.AddInputs(mi); err != nil {
			return fmt.Errorf("failed to add RTP media input: %w", err)
		}
		v.RtpIn = mi

		rtcpSource := v.pipeline.RtcpSrc()
		if rtcpSource == nil {
			v.log.Warnw("RTCP source isn't configured", nil)
		} else {
			mi, err := NewMediaInput(v.ctx, rtcpSource, v.rtcpConn)
			if err != nil {
				return fmt.Errorf("failed to create RTCP media input: %w", err)
			}
			if err := v.io.AddInputs(mi); err != nil {
				return fmt.Errorf("failed to add RTCP media input: %w", err)
			}
			v.RtcpIn = mi
		}
	}

	if media.Direction.IsSend() {
		v.rtpConn.SetDst(netip.AddrPortFrom(remote, media.Port))
		v.rtcpConn.SetDst(netip.AddrPortFrom(remote, media.RTCPPort))

		rtpSink := v.pipeline.RtpSink()
		if rtpSink == nil {
			return fmt.Errorf("RTP sink is nil")
		}
		mo, err := NewMediaOutput(v.ctx, v.rtpConn, rtpSink)
		if err != nil {
			return fmt.Errorf("failed to create RTP media output: %w", err)
		}
		if err := v.io.AddOutputs(mo); err != nil {
			return fmt.Errorf("failed to add RTP media output: %w", err)
		}
		v.RtpOut = mo

		rtcpSink := v.pipeline.RtcpSink()
		if rtcpSink == nil {
			v.log.Warnw("RTCP sink isn't configured", nil)
		} else {
			mo, err := NewMediaOutput(v.ctx, v.rtcpConn, rtcpSink)
			if err != nil {
				return fmt.Errorf("failed to create RTCP media output: %w", err)
			}
			if err := v.io.AddOutputs(mo); err != nil {
				return fmt.Errorf("failed to add RTCP media output: %w", err)
			}
			v.RtcpOut = mo
		}
	} else {
		v.rtpConn.SetDst(netip.AddrPort{})
		v.rtcpConn.SetDst(netip.AddrPort{})
	}

	if err := v.io.Start(); err != nil {
		return fmt.Errorf("failed to start media IO: %w", err)
	}

	return nil
}

func (v *VideoManager) Reconcile(remote netip.Addr, media *sdpv2.SDPMedia) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status == VideoStatusClosed {
		return fmt.Errorf("video manager is closed")
	}

	if v.mediaOK(media) {
		v.log.Debugw("video media unchanged, no reconciliation needed", "oldMedia", v.Media, "newMedia", media)
		v.Media = media
		return nil
	}

	if !isMedia(media) {
		v.log.Debugw("video media disabled, stopping video manager", "media", media)
		v.Media = nil
		return v.stop()
	}

	if err := v.ensurePipeline(); err != nil {
		return fmt.Errorf("failed to ensure GStreamer pipeline: %w", err)
	}

	if err := v.pipeline.SetState(gst.StatePaused); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to paused: %w", err)
	}

	if err := v.resetIO(); err != nil {
		return fmt.Errorf("failed to reset media IO: %w", err)
	}

	v.log.Infow("video setup", "remote", remote.String(), "rtp_port", v.RtpPort(), "rtcp_port", v.RtcpPort(), "codec", media.Codec, "direction", media.Direction)

	if err := v.pipeline.Configure(media); err != nil {
		return fmt.Errorf("failed to configure GStreamer pipeline: %w", err)
	}

	if err := v.setupIO(remote, media); err != nil {
		return fmt.Errorf("failed to setup media IO: %w", err)
	}

	v.Media = media
	v.status = VideoStatusStarted

	v.log.Debugw("starting video pipeline")
	if err := v.pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to playing: %w", err)
	}

	return nil
}

func (v *VideoManager) ensurePipeline() error {
	if v.pipeline != nil {
		return nil
	}

	pipeline, err := v.factory.CreateVideoPipeline(v.opts)
	if err != nil {
		return fmt.Errorf("failed to create GStreamer pipeline: %w", err)
	}
	v.pipeline = pipeline

	return nil
}

func (v *VideoManager) stop() error {
	v.log.Debugw("stopping video manager")

	if err := v.resetIO(); err != nil {
		return fmt.Errorf("failed to reset media IO: %w", err)
	}

	if err := v.io.Stop(); err != nil {
		return fmt.Errorf("failed to stop media IO: %w", err)
	}

	v.Media = nil

	if err := v.pipeline.Close(); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
	}
	v.pipeline = nil

	v.status = VideoStatusStopped

	return nil
}
