package sip

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	lrtp "github.com/livekit/media-sdk/rtp"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type videoBridge struct {
	log   logger.Logger
	media *MediaPort
	conf  *VideoConfig

	track *webrtc.TrackLocalStaticRTP
	pub   *lksdk.LocalTrackPublication
	lp    *lksdk.LocalParticipant

	closed core.Fuse
	wg     sync.WaitGroup

	remoteMu sync.Mutex
	remotes  map[string]*bridgeRemoteTrack
}

type bridgeRemoteTrack struct {
	track *webrtc.TrackRemote
	pub   *lksdk.RemoteTrackPublication
}

func newVideoBridge(r *Room, media *MediaPort, conf *VideoConfig) (*videoBridge, error) {
	var lp *lksdk.LocalParticipant
	if r.room != nil {
		lp = r.room.LocalParticipant
	}
	vb := &videoBridge{
		log:     r.log,
		media:   media,
		conf:    conf,
		lp:      lp,
		remotes: make(map[string]*bridgeRemoteTrack),
	}
	if conf.Direction.Send {
		cap := webrtc.RTPCodecCapability{
			MimeType:    conf.MimeType,
			ClockRate:   conf.ClockRate,
			SDPFmtpLine: conf.SDPFmtpLine,
		}
		track, err := webrtc.NewTrackLocalStaticRTP(cap, "video", "sip")
		if err != nil {
			return nil, err
		}
		if lp == nil {
			return nil, errors.New("room is not ready to publish video track")
		}
		name := r.p.Identity
		if name == "" {
			name = "sip-video"
		} else {
			name += "-video"
		}
		pub, err := lp.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: name})
		if err != nil {
			return nil, err
		}
		vb.track = track
		vb.pub = pub
		media.SetVideoHandler(lrtp.NewNopCloser(lrtp.HandlerFunc(func(h *lrtp.Header, payload []byte) error {
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

		v.remoteMu.Lock()
		remotes := make([]*bridgeRemoteTrack, 0, len(v.remotes))
		for key, rv := range v.remotes {
			remotes = append(remotes, rv)
			delete(v.remotes, key)
		}
		v.remoteMu.Unlock()

		for _, rv := range remotes {
			if rv.pub != nil {
				_ = rv.pub.SetSubscribed(false)
			}
			if rv.track != nil {
				_ = rv.track.SetReadDeadline(time.Now())
			}
		}

		if v.pub != nil && v.lp != nil {
			if err := v.lp.UnpublishTrack(v.pub.SID()); err != nil {
				v.log.Debugw("failed to unpublish video track", "error", err)
			}
		} else if v.pub != nil {
			v.pub.CloseTrack()
		}
		v.wg.Wait()
	})
}

func (v *videoBridge) handleRemoteTrack(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if v == nil || v.conf == nil || !v.conf.Direction.Recv {
		return
	}
	key := ""
	if pub != nil {
		key = pub.SID()
	}
	if key == "" && track != nil {
		key = fmt.Sprintf("%p", track)
	}
	v.remoteMu.Lock()
	if v.remotes == nil {
		v.remotes = make(map[string]*bridgeRemoteTrack)
	}
	v.remotes[key] = &bridgeRemoteTrack{track: track, pub: pub}
	v.remoteMu.Unlock()
	v.wg.Add(1)
	go func() {
		defer v.wg.Done()
		defer func() {
			v.remoteMu.Lock()
			delete(v.remotes, key)
			v.remoteMu.Unlock()
		}()
		for {
			if err := track.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil && !v.closed.IsBroken() {
				v.log.Debugw("video bridge failed to update read deadline", "error", err)
			}
			pkt, _, err := track.ReadRTP()
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					if v.closed.IsBroken() {
						if pub != nil {
							_ = pub.SetSubscribed(false)
						}
						return
					}
					continue
				}
				if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !v.closed.IsBroken() {
					v.log.Debugw("video bridge stopped reading track", "error", err)
				}
				return
			}
			if err = v.media.WriteVideoRTP(&pkt.Header, pkt.Payload); err != nil {
				if errors.Is(err, net.ErrClosed) || v.closed.IsBroken() {
					return
				}
				v.log.Debugw("failed to forward video packet", "error", err)
				return
			}
		}
	}()
}
