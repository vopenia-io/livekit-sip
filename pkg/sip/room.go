// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	"github.com/pion/webrtc/v4"

	msdk "github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/dtmf"
	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/medialogutils"
	"github.com/livekit/protocol/sip"
	lksdk "github.com/livekit/server-sdk-go/v2"

	"github.com/livekit/media-sdk/mixer"

	"github.com/livekit/sip/pkg/config"
	"github.com/livekit/sip/pkg/media/opus"
)

type RoomStats struct {
	InputPackets atomic.Uint64
	InputBytes   atomic.Uint64
	DTMFPackets  atomic.Uint64

	MixerFrames  atomic.Uint64
	MixerSamples atomic.Uint64

	Mixer mixer.Stats

	OutputFrames  atomic.Uint64
	OutputSamples atomic.Uint64

	Closed atomic.Bool
}

type ParticipantInfo struct {
	ID       string
	RoomName string
	Identity string
	Name     string
}

type TrackCallback = func(ti *TrackInput)

type Room struct {
	log        logger.Logger
	roomLog    logger.Logger // deferred logger
	room       *lksdk.Room
	mix        *mixer.Mixer
	out        *msdk.SwitchWriter
	outDtmf    atomic.Pointer[dtmf.Writer]
	p          ParticipantInfo
	ready      core.Fuse
	subscribe  atomic.Bool
	subscribed core.Fuse
	stopped    core.Fuse
	closed     core.Fuse
	stats      *RoomStats

	participantTracks map[string][2]*TrackInput
	activeParticipant struct {
		t          uint8
		p          string
		lastSwitch time.Time
	}
	trackMu       sync.Mutex
	trackCallback TrackCallback
}

type ParticipantConfig struct {
	Identity   string
	Name       string
	Metadata   string
	Attributes map[string]string
}

type RoomConfig struct {
	WsUrl       string
	Token       string
	RoomName    string
	Participant ParticipantConfig
	RoomPreset  string
	RoomConfig  *livekit.RoomConfiguration
	JitterBuf   bool
}

func NewRoom(log logger.Logger, st *RoomStats) *Room {
	if st == nil {
		st = &RoomStats{}
	}
	r := &Room{log: log,
		stats:             st,
		out:               msdk.NewSwitchWriter(RoomSampleRate),
		participantTracks: make(map[string][2]*TrackInput),
	}
	out := newMediaWriterCount(r.out, &st.OutputFrames, &st.OutputSamples)

	var err error
	r.mix, err = mixer.NewMixer(out, rtp.DefFrameDur, &st.Mixer, 1, mixer.DefaultInputBufferFrames)
	if err != nil {
		panic(err)
	}

	roomLog, resolve := log.WithDeferredValues()
	r.roomLog = roomLog

	go func() {
		select {
		case <-r.ready.Watch():
			if r.room != nil {
				resolve.Resolve("room", r.room.Name(), "roomID", r.room.SID())
			} else {
				resolve.Resolve()
			}
		case <-r.stopped.Watch():
			resolve.Resolve()
		case <-r.closed.Watch():
			resolve.Resolve()
		}
	}()

	return r
}

func (r *Room) Closed() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.stopped.Watch()
}

func (r *Room) Subscribed() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.subscribed.Watch()
}

func (r *Room) Room() *lksdk.Room {
	if r == nil {
		return nil
	}
	return r.room
}

func (r *Room) participantJoin(rp *lksdk.RemoteParticipant) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID())
	log.Debugw("participant joined")
	switch rp.Kind() {
	case lksdk.ParticipantSIP:
		// Avoid a deadlock where two SIP participant join a room and won't publish their track.
		// Each waits for the other's track to subscribe before publishing its own track.
		// So we just assume SIP participants will eventually start speaking.
		r.subscribed.Break()
		log.Infow("unblocking subscription - second sip participant is in the room")
	}
}

func (r *Room) participantLeft(rp *lksdk.RemoteParticipant) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID())
	log.Debugw("participant left")

	r.DeleteParticipantTrack(rp.Identity(), nil)
}

func (r *Room) subscribeTo(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
	if k := pub.Kind(); k != lksdk.TrackKindAudio && k != lksdk.TrackKindVideo {
		log.Debugw("skipping non-audio/video track", "kind", k)
		return
	}
	log.Debugw("subscribing to a track")
	if err := pub.SetSubscribed(true); err != nil {
		log.Errorw("cannot subscribe to the track", err)
		return
	}
	r.subscribed.Break()
}

func (r *Room) Connect(conf *config.Config, rconf RoomConfig) error {
	if rconf.WsUrl == "" {
		rconf.WsUrl = conf.WsUrl
	}
	partConf := rconf.Participant
	r.p = ParticipantInfo{
		RoomName: rconf.RoomName,
		Identity: partConf.Identity,
		Name:     partConf.Name,
	}
	roomCallback := &lksdk.RoomCallback{
		OnParticipantConnected: func(rp *lksdk.RemoteParticipant) {
			log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID())
			if !r.subscribe.Load() {
				log.Debugw("skipping participant join event - subscribed flag not set")
				return // will subscribe later
			}
			r.participantJoin(rp)
		},
		OnParticipantDisconnected: func(rp *lksdk.RemoteParticipant) {
			r.participantLeft(rp)
		},
		OnActiveSpeakersChanged: func(p []lksdk.Participant) {
			r.UpdateActiveParticipant(p)
		},
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackPublished: func(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
				if !r.subscribe.Load() {
					log.Debugw("skipping track publish event - subscribed flag not set")
					return // will subscribe later
				}
				r.subscribeTo(pub, rp)
			},
			OnTrackSubscribed: func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				r.log.Debugw("track subscribed", "kind", pub.Kind(), "participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
				switch pub.Kind() {
				case lksdk.TrackKindAudio:
					r.participantAudioTrackSubscribed(track, pub, rp, conf)
				case lksdk.TrackKindVideo:
					r.participantVideoTrackSubscribed(track, pub, rp, conf)
				default:
					log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
					log.Warnw("unsupported track kind for subscription", fmt.Errorf("kind=%s", pub.Kind()))
				}
			},
			OnTrackUnsubscribed: func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				r.log.Debugw("track unsubscribed", "kind", pub.Kind(), "participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
				switch pub.Kind() {
				case lksdk.TrackKindAudio:
					// no-op
				case lksdk.TrackKindVideo:
					r.DeleteParticipantTrack(rp.Identity(), &pub.TrackInfo().Source)
				default:
					log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
					log.Warnw("unsupported track kind for unsubscription", fmt.Errorf("kind=%s", pub.Kind()))
				}
			},
			OnDataPacket: func(data lksdk.DataPacket, params lksdk.DataReceiveParams) {
				switch data := data.(type) {
				case *livekit.SipDTMF:
					r.stats.InputPackets.Add(1)
					// TODO: Only generate audio DTMF if the message was a broadcast from another SIP participant.
					//       DTMF audio tone will be automatically mixed in any case.
					r.sendDTMF(data)
				}
			},
		},
		OnDisconnected: func() {
			r.stopped.Break()
		},
	}

	if rconf.Token == "" {
		// TODO: Remove this code path, always sign tokens on LiveKit server.
		//       For now, match Cloud behavior and do not send extra attrs in the token.
		tokenAttrs := make(map[string]string, len(partConf.Attributes))
		for _, k := range []string{
			livekit.AttrSIPCallID,
			livekit.AttrSIPTrunkID,
			livekit.AttrSIPDispatchRuleID,
			livekit.AttrSIPTrunkNumber,
			livekit.AttrSIPPhoneNumber,
		} {
			if v, ok := partConf.Attributes[k]; ok {
				tokenAttrs[k] = v
			}
		}
		var err error
		rconf.Token, err = sip.BuildSIPToken(sip.SIPTokenParams{
			APIKey:                conf.ApiKey,
			APISecret:             conf.ApiSecret,
			RoomName:              rconf.RoomName,
			ParticipantIdentity:   partConf.Identity,
			ParticipantName:       partConf.Name,
			ParticipantMetadata:   partConf.Metadata,
			ParticipantAttributes: tokenAttrs,
			RoomPreset:            rconf.RoomPreset,
			RoomConfig:            rconf.RoomConfig,
		})
		if err != nil {
			return err
		}
	}
	room := lksdk.NewRoom(roomCallback)
	room.SetLogger(medialogutils.NewOverrideLogger(r.log))
	err := room.JoinWithToken(rconf.WsUrl, rconf.Token,
		lksdk.WithAutoSubscribe(false),
		lksdk.WithExtraAttributes(partConf.Attributes),
	)
	if err != nil {
		return err
	}
	r.room = room
	r.p.ID = r.room.LocalParticipant.SID()
	r.p.Identity = r.room.LocalParticipant.Identity()
	room.LocalParticipant.SetAttributes(partConf.Attributes)
	r.ready.Break()
	r.subscribe.Store(false) // already false, but keep for visibility

	// Not subscribing to any tracks just yet!
	return nil
}

func (r *Room) Subscribe() {
	if r.room == nil {
		return
	}
	r.subscribe.Store(true)
	list := r.room.GetRemoteParticipants()
	r.log.Debugw("subscribing to existing room participants", "participants", len(list))
	for _, rp := range list {
		r.participantJoin(rp)
		for _, pub := range rp.TrackPublications() {
			if remotePub, ok := pub.(*lksdk.RemoteTrackPublication); ok {
				r.subscribeTo(remotePub, rp)
			}
		}
	}
}

func (r *Room) Output() msdk.Writer[msdk.PCM16Sample] {
	return r.out.Get()
}

// SwapOutput sets room audio output and returns the old one.
// Caller is responsible for closing the old writer.
func (r *Room) SwapOutput(out msdk.PCM16Writer) msdk.PCM16Writer {
	if r == nil {
		return nil
	}
	if out == nil {
		return r.out.Swap(nil)
	}
	return r.out.Swap(msdk.ResampleWriter(out, r.mix.SampleRate()))
}

func (r *Room) CloseOutput() error {
	w := r.SwapOutput(nil)
	if w == nil {
		return nil
	}
	return w.Close()
}

func (r *Room) SetDTMFOutput(w dtmf.Writer) {
	if r == nil {
		return
	}
	if w == nil {
		r.outDtmf.Store(nil)
		return
	}
	r.outDtmf.Store(&w)
}

func (r *Room) sendDTMF(msg *livekit.SipDTMF) {
	outDTMF := r.outDtmf.Load()
	if outDTMF == nil {
		r.log.Infow("ignoring dtmf", "digit", msg.Digit)
		return
	}
	// TODO: Separate goroutine?
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.log.Infow("forwarding dtmf to sip", "digit", msg.Digit)
	_ = (*outDTMF).WriteDTMF(ctx, msg.Digit)
}

func (r *Room) Close() error {
	return r.CloseWithReason(livekit.DisconnectReason_UNKNOWN_REASON)
}

func (r *Room) CloseWithReason(reason livekit.DisconnectReason) error {
	if r == nil {
		return nil
	}
	var err error
	r.closed.Once(func() {
		defer r.stats.Closed.Store(true)

		r.subscribe.Store(false)
		err = r.CloseOutput()
		r.SetDTMFOutput(nil)
		if r.room != nil {
			r.room.DisconnectWithReason(reason)
			r.room = nil
		}
		if r.mix != nil {
			r.mix.Stop()
		}
	})
	return err
}

func (r *Room) Participant() ParticipantInfo {
	if r == nil {
		return ParticipantInfo{}
	}
	return r.p
}

func (r *Room) NewParticipantTrack(sampleRate int) (msdk.WriteCloser[msdk.PCM16Sample], error) {
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion")
	if err != nil {
		return nil, err
	}
	p := r.room.LocalParticipant
	if _, err = p.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: p.Identity(),
	}); err != nil {
		return nil, err
	}
	ow := msdk.FromSampleWriter[opus.Sample](track, sampleRate, rtp.DefFrameDur)
	pw, err := opus.Encode(ow, channels, r.log)
	if err != nil {
		return nil, err
	}
	return pw, nil
}

func (r *Room) NewParticipantVideoTrack() (*webrtc.TrackLocalStaticRTP, error) {
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeVP8,
	}, "video", "pion")
	if err != nil {
		return nil, err
	}
	p := r.room.LocalParticipant
	if _, err = p.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: p.Identity(),
	}); err != nil {
		return nil, err
	}
	return track, nil
}

func (r *Room) SendData(data lksdk.DataPacket, opts ...lksdk.DataPublishOption) error {
	if r == nil || !r.ready.IsBroken() || r.closed.IsBroken() {
		return nil
	}
	return r.room.LocalParticipant.PublishDataPacket(data, opts...)
}

func (r *Room) NewTrack() *mixer.Input {
	if r == nil {
		return nil
	}
	return r.mix.NewInput()
}

func (r *Room) participantAudioTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", track.ID(), "trackName", pub.Name())
	if !r.ready.IsBroken() {
		log.Warnw("ignoring track, room not ready", nil)
		return
	}
	log.Infow("mixing track")

	go func() {
		mTrack := r.NewTrack()
		if mTrack == nil {
			return // closed
		}
		defer mTrack.Close()

		in := newRTPReaderCount(track, &r.stats.InputPackets, &r.stats.InputBytes)
		out := newMediaWriterCount(mTrack, &r.stats.MixerFrames, &r.stats.MixerSamples)

		odec, err := opus.Decode(out, channels, log)
		if err != nil {
			log.Errorw("cannot create opus decoder", err)
			return
		}
		defer odec.Close()

		var h rtp.HandlerCloser = rtp.NewNopCloser(rtp.NewMediaStreamIn[opus.Sample](odec))
		if conf.EnableJitterBuffer {
			h = rtp.HandleJitter(h)
		}
		err = rtp.HandleLoop(in, h)
		if err != nil && !errors.Is(err, io.EOF) {
			log.Infow("room track rtp handler returned with failure", "error", err)
		}
	}()
}

func (r *Room) DeleteParticipantTrack(participantID string, source *livekit.TrackSource) {
	r.log.Infow("deleting participant track", "participant", participantID, "source", source)
	defer r.log.Infow("deleted participant track", "participant", participantID, "source", source)

	func() {
		r.trackMu.Lock()
		defer r.trackMu.Unlock()
		if source != nil {
			pos := 0
			switch *source {
			case livekit.TrackSource_SCREEN_SHARE:
				pos = 0
			case livekit.TrackSource_CAMERA:
				pos = 1
			default:
				return
			}
			tracks, ok := r.participantTracks[participantID]
			if !ok {
				return
			}
			tracks[pos] = nil
			r.participantTracks[participantID] = tracks
		} else {
			delete(r.participantTracks, participantID)
		}
	}()

	if r.activeParticipant.p == participantID {
		r.UpdateActiveParticipant(nil)
	}
}

func (r *Room) SetTrackCallback(cb TrackCallback) {
	r.trackMu.Lock()
	defer r.trackMu.Unlock()
	r.trackCallback = cb
}

func (r *Room) UpdateActiveParticipant(p []lksdk.Participant) {
	r.log.Debugw("updating active participant", "candidates", len(p))
	if r.room == nil {
		return
	}
	if p == nil {
		p = []lksdk.Participant{}
	}
	for len(p) == 0 {
		as := r.room.ActiveSpeakers()
		if len(as) > 0 {
			p = append(p, as...)
		} else {
			for _, rp := range r.room.GetRemoteParticipants() {
				p = append(p, rp)
			}
		}
		if len(p) > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	best := p[0]
	for _, candidate := range p {
		if candidate.IsScreenShareEnabled() && !best.IsScreenShareEnabled() {
			best = candidate
			continue
		}
		if candidate.IsScreenShareAudioEnabled() && !best.IsScreenShareAudioEnabled() {
			best = candidate
			continue
		}
		if candidate.IsCameraEnabled() && !best.IsCameraEnabled() {
			best = candidate
			continue
		}
		if candidate.IsMicrophoneEnabled() && !best.IsMicrophoneEnabled() {
			best = candidate
		}
		if candidate.AudioLevel() > best.AudioLevel() {
			best = candidate
		}
	}

	r.trackMu.Lock()
	defer r.trackMu.Unlock()

	id := best.Identity()
	// if r.activeParticipant == id {
	// 	return
	// }

	track, ok := r.participantTracks[id]
	if !ok || (track[0] == nil && track[1] == nil) {
		// No tracks available for this participant
		return
	}
	activeParticipant := id
	activeTrack := uint8(0)
	if track[0] == nil {
		activeTrack = 1
	}

	if r.activeParticipant.p == activeParticipant && r.activeParticipant.t == activeTrack {
		r.log.Debugw("Already on this participant/track, skipping switch",
			"participant", activeParticipant,
			"track", activeTrack)
		return
	}

	// Hysteresis: prevent rapid switching by requiring minimum time between switches
	// Exception: screenshare always takes priority and bypasses hysteresis
	const minSwitchInterval = 2 * time.Second
	timeSinceLastSwitch := time.Since(r.activeParticipant.lastSwitch)
	isScreenshare := activeTrack == 0 // Track 0 is screenshare

	if timeSinceLastSwitch < minSwitchInterval && !isScreenshare && !r.activeParticipant.lastSwitch.IsZero() {
		r.log.Debugw("Skipping participant switch due to hysteresis",
			"newParticipant", activeParticipant,
			"timeSinceLastSwitch", timeSinceLastSwitch,
			"minInterval", minSwitchInterval)
		return
	}

	r.activeParticipant.t = activeTrack
	r.activeParticipant.p = activeParticipant
	r.activeParticipant.lastSwitch = time.Now()

	if r.trackCallback != nil && track[activeTrack] != nil {

		r.log.Infow("invoking track callback for active participant",
			"participant", id,
			"trackType", map[uint8]string{0: "screenshare", 1: "camera"}[activeTrack],
			"tracks", track)

		go r.trackCallback(track[activeTrack])
	} else if track[activeTrack] == nil {
		r.log.Warnw("Active track is nil, skipping callback", nil,
			"participant", id,
			"activeTrack", activeTrack)
	}
}

func (r *Room) participantVideoTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", track.ID(), "trackName", pub.Name())
	if !r.ready.IsBroken() {
		log.Warnw("ignoring track, room not ready", nil)
		return
	}

	id := rp.Identity()
	source := pub.TrackInfo().Source

	log.Infow("handling new video track", "source", source)
	pos := 0
	switch source {
	case livekit.TrackSource_SCREEN_SHARE:
		pos = 0
	case livekit.TrackSource_CAMERA:
		pos = 1
	default:
		log.Warnw("unsupported video source for subscription", fmt.Errorf("source=%s", source))
		return
	}

	ti := NewTrackInput(track, pub, rp, conf)

	func() {
		r.trackMu.Lock()
		defer r.trackMu.Unlock()
		t := r.participantTracks[id]
		t[pos] = ti
		r.log.Debugw("storing participant video track", "participant", id, "position", pos, "tracksStored", t)
		r.participantTracks[id] = t
	}()

	// Start aggressive PLI sender for this track to ensure keyframes are always fresh
	// This ensures <100ms switching time when active speaker changes
	go r.sendPeriodicPLI(ti, rp.Identity(), log)

	// Don't call trackCallback directly - let UpdateActiveParticipant handle it
	// to ensure proper screenshare priority and avoid double-switching
	time.Sleep(1 * time.Second)
	r.UpdateActiveParticipant(nil)

	// if cb, ok := r.videoTrackCallback[pub.Name()]; ok {
	// 	go cb(track, pub, rp, conf)
	// 	return
	// }
	// if cb, ok := r.videoTrackCallback["*"]; ok {
	// 	go cb(track, pub, rp, conf)
	// 	return
	// }
	// log.Warnw("no video track callback registered for this track", nil)
}

func (r *Room) LocalParticipant() *lksdk.LocalParticipant {
	if r == nil || r.room == nil {
		return nil
	}
	return r.room.LocalParticipant
}

// sendPeriodicPLI sends PLI (Picture Loss Indication) packets to a video track
// every 1 second to ensure keyframes are generated regularly. This enables
// fast (<100ms) switching when the active speaker changes.
func (r *Room) sendPeriodicPLI(ti *TrackInput, participantID string, log logger.Logger) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	log.Debugw("Starting periodic PLI sender", "participant", participantID)

	for {
		select {
		case <-ticker.C:
			if r.closed.IsBroken() {
				log.Debugw("Room closed, stopping PLI sender", "participant", participantID)
				return
			}

			// Send PLI to request keyframe
			if err := ti.SendPLI(); err != nil {
				log.Debugw("Failed to send PLI", "error", err, "participant", participantID)
			} else {
				log.Debugw("Sent periodic PLI", "participant", participantID)
			}

		case <-r.stopped.Watch():
			log.Debugw("Room stopped, stopping PLI sender", "participant", participantID)
			return
		}
	}
}
