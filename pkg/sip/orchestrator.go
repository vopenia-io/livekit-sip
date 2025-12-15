package sip

import (
	"context"
	"errors"
	"fmt"
	"sync"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
)

const (
	ScreenshareMSTreamID = 2
)

type AudioInfo interface {
	Port() uint16
	Codec() *sdpv2.Codec
	AvailableCodecs() []*sdpv2.Codec
	SetMedia(media *sdpv2.SDPMedia)
}

type MediaOrchestrator struct {
	mu        sync.Mutex
	ctx       context.Context
	opts      *MediaOptions
	log       logger.Logger
	inbound   *sipInbound
	audioinfo AudioInfo
	camera    *CameraManager
	tracks    *TrackManager
	bfcp      *BFCPManager

	sdp *sdpv2.SDP
}

func NewMediaOrchestrator(log logger.Logger, ctx context.Context, inbound *sipInbound, room *Room, audioinfo AudioInfo, opts *MediaOptions) (*MediaOrchestrator, error) {
	o := &MediaOrchestrator{
		log:       log,
		ctx:       ctx,
		inbound:   inbound,
		audioinfo: audioinfo,
		opts:      opts,
	}

	o.tracks = NewTrackManager(log)

	camera, err := NewCameraManager(log.WithComponent("camera"), o.ctx, room, opts, o.tracks)
	if err != nil {
		return nil, fmt.Errorf("could not create video manager: %w", err)
	}
	o.camera = camera

	o.bfcp = NewBFCPManager(o.ctx, log, opts, inbound)

	return o, nil
}

func (o *MediaOrchestrator) Close() error {
	return errors.Join(
		o.camera.Close(),
		o.bfcp.Close(),
	)
}

func (o *MediaOrchestrator) AnswerSDP(offer *sdpv2.SDP) (*sdpv2.SDP, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sdp = offer // TODO: remove after testing

	o.log.Debugw("answering sdp", "offer", offer)
	if offer.Audio == nil {
		return nil, fmt.Errorf("no audio in offer")
	}

	if err := offer.Audio.SelectCodec(); err != nil {
		return nil, fmt.Errorf("could not select audio codec: %w", err)
	}
	o.log.Debugw("selected audio codec", "codec", offer.Audio.Codec)

	o.audioinfo.SetMedia(offer.Audio)

	if offer.Video != nil {
		if err := offer.Video.SelectCodec(); err != nil {
			return nil, fmt.Errorf("could not select video codec: %w", err)
		}
		o.log.Debugw("selected video codec", "codec", offer.Video.Codec)
	}

	if err := o.setupSDP(offer); err != nil {
		o.log.Errorw("could not setup sdp", err)
		return nil, fmt.Errorf("could not setup sdp: %w", err)
	}
	o.log.Debugw("setup sdp complete")

	answer, err := o.offerSDP(offer.Video != nil, offer.BFCP != nil, (offer.Screenshare != nil || (offer.Video != nil && offer.BFCP != nil)))
	if err != nil {
		return nil, fmt.Errorf("could not create answer sdp: %w", err)
	}

	return answer, nil
}

func (o *MediaOrchestrator) offerSDP(camera bool, bfcp bool, screenshare bool) (*sdpv2.SDP, error) {
	builder := (&sdpv2.SDP{}).Builder()

	builder.SetAddress(o.opts.IP)

	// audio is required anyway
	builder.SetAudio(func(b *sdpv2.SDPMediaBuilder) (*sdpv2.SDPMedia, error) {
		codec := o.audioinfo.Codec()
		if codec == nil {
			for _, c := range o.audioinfo.AvailableCodecs() {
				b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
					return c, nil
				}, false)
			}
		} else {
			b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
				return codec, nil
			}, true)
		}
		return b.
			SetRTPPort(uint16(o.audioinfo.Port())).
			Build()
	}).Build()

	if bfcp {
		if screenshare {
			builder.SetBFCP(func(b *sdpv2.SDPBfcpBuilder) (*sdpv2.SDPBfcp, error) {
				return b.
					SetPort(o.bfcp.Port()).
					SetConnection(sdpv2.BfcpConnectionNew).
					SetProto(sdpv2.BfcpProtoTCP).
					SetFloorCtrl(sdpv2.BfcpFloorCtrlServer).
					SetSetup(sdpv2.BfcpSetupPassive).
					SetConfID(o.bfcp.config.ConferenceID).
					SetUserID(1).
					SetMStreamID(ScreenshareMSTreamID).
					Build()
			})
		}
	}

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
	o.log.Debugw("created offer sdp", "offer", offer)

	return offer, nil
}

func (o *MediaOrchestrator) OfferSDP() (*sdpv2.SDP, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.offerSDP(true, true, true)
}

func (o *MediaOrchestrator) setupSDP(sdp *sdpv2.SDP) error {
	o.log.Debugw("setting up sdp", "sdp", sdp)

	o.log.Debugw("reconciling camera")
	if _, err := o.camera.Reconcile(sdp.Addr, sdp.Video); err != nil {
		o.log.Errorw("could not reconcile video sdp", err)
		return fmt.Errorf("could not reconcile video sdp: %w", err)
	}
	return nil
}

func (o *MediaOrchestrator) SetupSDP(sdp *sdpv2.SDP) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.setupSDP(sdp)
}
