package sip

import (
	"errors"
	"io"
	"net"
	"sync"

	"github.com/frostbyte73/core"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type videoBridge struct {
	log    logger.Logger
	media  *MediaPort
	conf   *VideoConfig
	track  *webrtc.TrackLocalStaticRTP
	closed core.Fuse
	wg     sync.WaitGroup
}

func newVideoBridge(r *Room, media *MediaPort, conf *VideoConfig) (*videoBridge, error) {
	vb := &videoBridge{
		log:   r.log,
		media: media,
		conf:  conf,
	}
	if conf.Direction.Send {
		cap := webrtc.RTPCodecCapability{
			MimeType:    conf.MimeType,
			ClockRate:   int(conf.ClockRate),
			SDPFmtpLine: conf.SDPFmtpLine,
		}
		track, err := webrtc.NewTrackLocalStaticRTP(cap, "video", "sip")
		if err != nil {
			return nil, err
		}
		lp := r.room.LocalParticipant
		name := r.p.Identity
		if name == "" {
			name = "sip-video"
		} else {
			name += "-video"
		}
		if _, err = lp.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: name}); err != nil {
			return nil, err
		}
		vb.track = track
		media.SetVideoHandler(rtp.NewNopCloser(rtp.HandlerFunc(func(h *rtp.Header, payload []byte) error {
			pkt := &rtp.Packet{Header: *h, Payload: append([]byte(nil), payload...)}
			return track.WriteRTP(pkt)
		})))
	} else {
		media.SetVideoHandler(nil)
	}
	return vb, nil
}

func (v *videoBridge) Close() {
	if v == nil {
		return
	}
	v.closed.Once(func() {
		v.media.SetVideoHandler(nil)
		v.wg.Wait()
	})
}

func (v *videoBridge) handleRemoteTrack(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if v == nil || v.conf == nil || !v.conf.Direction.Recv {
		return
	}
	v.wg.Add(1)
	go func() {
		defer v.wg.Done()
		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
					v.log.Debugw("video bridge stopped reading track", "error", err)
				}
				return
			}
			if err = v.media.WriteVideoRTP(&pkt.Header, pkt.Payload); err != nil {
				if !errors.Is(err, net.ErrClosed) {
					v.log.Debugw("failed to forward video packet", "error", err)
				}
				return
			}
		}
	}()
}
