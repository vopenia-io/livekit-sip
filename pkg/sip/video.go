package sip

import (
	"context"
	"fmt"
	"net/netip"
	"runtime"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/media-sdk/h264"
	sdpv1 "github.com/livekit/media-sdk/sdp"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/activeselector"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/h264vp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/lkroom"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sinkwriter"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipconn"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sourcereader"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/vp8h264"
)

var mainLoop *glib.MainLoop

func init() {
	gst.Init(nil)
	if !sourcereader.Register() {
		panic("failed to register sourcereader element")
	}

	if !sinkwriter.Register() {
		panic("failed to register sinkwriter element")
	}

	if !h264vp8.Register() {
		panic("Failed to register h264-vp8")
	}

	if !vp8h264.Register() {
		panic("Failed to register vp8-h264")
	}

	if !sipconn.Register() {
		panic("Failed to register sipconn")
	}

	if !lkroom.Register() {
		panic("Failed to register lkroom")
	}

	if !activeselector.Register() {
		panic("Failed to register active-selector")
	}

	mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)
	_ = mainLoop
}

type VideoStatus int

const (
	VideoStatusClosed VideoStatus = iota
	VideoStatusStopped
	VideoStatusReady
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

func NewVideoManager(log logger.Logger, ctx context.Context, opts *MediaOptions) (*VideoManager, error) {
	bindIP := opts.IPLocal
	if !bindIP.IsValid() {
		bindIP = opts.IP
	}

	v := &VideoManager{
		log:    log,
		ctx:    ctx,
		opts:   opts,
		status: VideoStatusStopped,
	}

	v.log.Infow("video manager created")

	return v, nil
}

type VideoManager struct {
	log      logger.Logger
	ctx      context.Context
	opts     *MediaOptions
	status   VideoStatus
	pipeline *pipeline.Pipeline
	Media    *sdpv2.SDPMedia
	Remote   netip.Addr
}

func (v *VideoManager) RtpPort() int {
	return int(v.pipeline.SipRtpPort())
}

func (v *VideoManager) RtcpPort() int {
	return int(v.pipeline.SipRtcpPort())
}

func (v *VideoManager) SetRoomCallbacks(callbacks *lksdk.RoomCallback) error {
	return v.pipeline.SetRoomCallbacks(callbacks)
}

func (v *VideoManager) GetRoom() (*lksdk.Room, error) {
	return v.pipeline.GetRoom()
}

func (v *VideoManager) SetRoomOptions(wsUrl, token string, opts ...lksdk.ConnectOption) error {
	return v.pipeline.SetRoomOptions(wsUrl, token, opts...)
}

func (v *VideoManager) Close() error {
	if v.status == VideoStatusClosed {
		return fmt.Errorf("video manager already closed")
	}
	v.log.Debugw("closing video manager")
	if err := v.stop(); err != nil {
		return fmt.Errorf("failed to stop video manager: %w", err)
	}
	v.status = VideoStatusClosed
	return nil
}

func (v *VideoManager) Direction() sdpv2.Direction {
	if v.Media == nil {
		return sdpv2.DirectionInactive
	}
	return v.Media.Direction
}

func (v *VideoManager) Codec() *sdpv2.Codec {
	if v.Media == nil {
		return nil
	}
	return v.Media.Codec
}

func (v *VideoManager) Status() VideoStatus {
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

type ReconcileStatus int

const (
	ReconcileStatusUnchanged ReconcileStatus = iota
	ReconcileStatusCreated
	ReconcileStatusUpdated
	ReconcileStatusStopped
)

func (v *VideoManager) Reconcile(remote netip.Addr, media *sdpv2.SDPMedia) (ReconcileStatus, error) {
	if v.status == VideoStatusClosed {
		return ReconcileStatusUnchanged, fmt.Errorf("video manager is closed")
	}

	if v.mediaOK(media) {
		v.log.Debugw("video media unchanged, no reconciliation needed", "oldMedia", v.Media, "newMedia", media)
		v.Media = media
		return ReconcileStatusUnchanged, nil
	}

	if !isMedia(media) {
		v.log.Debugw("video media disabled, stopping video manager", "media", media)
		v.Media = nil
		return ReconcileStatusStopped, v.stop()
	}

	rs := ReconcileStatusUpdated

	if init, err := v.resetPipeline(); err != nil {
		return ReconcileStatusUnchanged, fmt.Errorf("failed to reset GStreamer pipeline: %w", err)
	} else if init {
		rs = ReconcileStatusCreated
	}

	v.log.Infow("video setup", "remote", remote.String(), "rtp_port", v.RtpPort(), "rtcp_port", v.RtcpPort(), "codec", media.Codec, "direction", media.Direction)

	v.Remote = remote

	v.Media = media
	v.status = VideoStatusReady

	return rs, nil
}

func (v *VideoManager) Start() error {
	if v.status == VideoStatusStarted {
		return nil
	}

	if err := v.pipeline.Configure(v.Remote, v.Media); err != nil {
		v.log.Errorw("failed to configure SIP IO", err)
		return fmt.Errorf("failed to configure SIP IO: %w", err)
	}

	v.status = VideoStatusStarted

	return nil
}

func (v *VideoManager) resetPipeline() (bool, error) {
	init := true
	v.log.Debugw("resetting video pipeline")
	if v.pipeline != nil {
		init = false
		v.log.Debugw("closing existing GStreamer pipeline")
		if err := v.pipeline.Close(); err != nil {
			v.log.Errorw("failed to close GStreamer pipeline, going to leak it", err)
		}
		v.pipeline = nil
		v.log.Debugw("existing GStreamer pipeline closed")
	}

	v.log.Debugw("creating new GStreamer pipeline")
	pipeline, err := pipeline.New(v.ctx, v.log)
	if err != nil {
		return init, fmt.Errorf("failed to create GStreamer pipeline: %w", err)
	}
	pipeline.Monitor()

	v.pipeline = pipeline
	v.log.Debugw("new GStreamer pipeline created")

	v.log.Debugw("starting video pipeline")
	if err := v.pipeline.SetState(gst.StatePaused); err != nil {
		return init, fmt.Errorf("failed to set GStreamer pipeline to paused: %w", err)
	}

	return init, nil
}

// var pid = os.Getpid()

func (v *VideoManager) stop() error {
	v.log.Debugw("stopping video manager")

	if v.status == VideoStatusStopped {
		v.log.Debugw("video manager already stopped")
		return nil
	}

	v.Media = nil

	if v.pipeline != nil {
		if err := v.pipeline.Close(); err != nil {
			return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
		}
		v.pipeline = nil
		time.Sleep(100 * time.Millisecond) // DO NOT REMOVE: force both GC to fully release resources
		runtime.GC()
		time.Sleep(100 * time.Millisecond)
		runtime.GC()
		time.Sleep(1 * time.Second) // DO NOT REMOVE: give time to GStreamer to cleanup
	}

	v.status = VideoStatusStopped

	return nil
}
