package sip

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/frostbyte73/core"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

type MediaOrchestrator struct {
	mu      sync.Mutex
	ctx     context.Context
	opts    *MediaOptions
	log     logger.Logger
	inbound *sipInbound
	camera  *CameraManager
	tracks  *TrackManager
}

func NewMediaOrchestrator(log logger.Logger, ctx context.Context, inbound *sipInbound, room *Room, opts *MediaOptions) (*MediaOrchestrator, error) {
	o := &MediaOrchestrator{
		log:     log,
		ctx:     ctx,
		inbound: inbound,
		opts:    opts,
	}

	o.tracks = NewTrackManager(log)

	camera, err := NewCameraManager(log.WithComponent("camera"), o.ctx, room, opts)
	if err != nil {
		return nil, fmt.Errorf("could not create video manager: %w", err)
	}
	o.camera = camera

	return o, nil
}

func (o *MediaOrchestrator) Close() error {
	return errors.Join(o.camera.Close())
}

func (o *MediaOrchestrator) AnswerSDP(offer *sdpv2.SDP) (*sdpv2.SDP, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if offer.Video != nil {
		if err := offer.Video.SelectCodec(); err != nil {
			return nil, fmt.Errorf("could not select video codec: %w", err)
		}
	}

	if err := o.setupSDP(offer); err != nil {
		return nil, fmt.Errorf("could not setup sdp: %w", err)
	}

	answer, err := o.offerSDP(offer.Video != nil)
	if err != nil {
		return nil, fmt.Errorf("could not create answer sdp: %w", err)
	}

	return answer, nil
}

func (o *MediaOrchestrator) offerSDP(camera bool) (*sdpv2.SDP, error) {
	builder := (&sdpv2.SDP{}).Builder()

	builder.SetAddress(o.opts.IP)

	if camera {
		builder.SetVideo(func(b *sdpv2.SDPMediaBuilder) (*sdpv2.SDPMedia, error) {
			codec := o.camera.Codec()
			if codec == nil {
				for _, c := range o.camera.SupportedCodecs() {
					b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
						return c, nil
					}, false)
				}
			} else {
				b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
					return codec, nil
				}, true)
			}
			b.SetDisabled(o.camera.Status() != VideoStatusStarted)
			b.SetRTPPort(uint16(o.camera.RtpPort()))
			b.SetRTCPPort(uint16(o.camera.RtcpPort()))
			b.SetDirection(o.camera.Direction())
			return b.Build()
		})
	}

	offer, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("could create a new sdp: %w", err)
	}

	return offer, nil
}

func (o *MediaOrchestrator) OfferSDP() (*sdpv2.SDP, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.offerSDP(true)
}

func (o *MediaOrchestrator) setupSDP(sdp *sdpv2.SDP) error {
	if err := o.camera.Reconcile(sdp.Addr, sdp.Video); err != nil {
		return fmt.Errorf("could not reconcile video sdp: %w", err)
	}
	return nil
}

func (o *MediaOrchestrator) SetupSDP(sdp *sdpv2.SDP) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.setupSDP(sdp)
}

func NewTrackManager(log logger.Logger) *TrackManager {
	return &TrackManager{
		log: log.WithComponent("TrackManager"),
	}
}

type TrackManager struct {
	ready core.Fuse
	mu    sync.Mutex
	log   logger.Logger
	p     *lksdk.LocalParticipant

	camera *TrackOutput
}

func (tm *TrackManager) ParticipantReady(p *lksdk.LocalParticipant) error {
	if tm.ready.IsBroken() {
		return fmt.Errorf("track manager is already ready")
	}
	tm.p = p // can't lock the fuse because other function already lock it while waiting for the participant
	tm.ready.Break()
	return nil
}

func (tm *TrackManager) Camera() (*TrackOutput, error) {

	tm.mu.Lock()
	defer tm.mu.Unlock()

	if !tm.ready.IsBroken() {
		tm.log.Warnw("track manager not ready yet, waiting", nil)
		<-tm.ready.Watch()
		tm.log.Infow("track manager is now ready", nil)
	}

	if tm.camera != nil {
		return tm.camera, nil
	}

	to := &TrackOutput{}

	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeVP8,
	}, "video", "pion")
	if err != nil {
		return nil, fmt.Errorf("could not create room camera track: %w", err)
	}
	pt, err := tm.p.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: tm.p.Identity(),
	})
	if err != nil {
		return nil, err
	}
	tm.log.Infow("published video track", "SID", pt.SID())
	trackRtcp := &RtcpWriter{
		pc: tm.p.GetSubscriberPeerConnection(),
	}
	to.RtpOut = &CallbackWriteCloser{
		Writer: track,
		Callback: func() error {
			tm.mu.Lock()
			defer tm.mu.Unlock()
			tm.log.Infow("unpublishing video track", "SID", pt.SID())
			tm.camera = nil
			if err := tm.p.UnpublishTrack(pt.SID()); err != nil {
				return fmt.Errorf("could not unpublish track: %w", err)
			}
			return nil
		},
	}
	to.RtcpOut = &NopWriteCloser{Writer: trackRtcp}

	tm.camera = to

	return to, nil
}
