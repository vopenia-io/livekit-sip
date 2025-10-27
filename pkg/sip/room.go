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

	MixerFrames  atomic.Uint64
	MixerSamples atomic.Uint64

	Mixer mixer.Stats

	OutputFrames  atomic.Uint64
	OutputSamples atomic.Uint64
}

type ParticipantInfo struct {
	ID       string
	RoomName string
	Identity string
	Name     string
}

type videoTrackHandler struct {
	identity      string
	participantID string
	track         *webrtc.TrackRemote
	handler       *noErrHandlerCloser
}

type Room struct {
	log        logger.Logger
	roomLog    logger.Logger // deferred logger
	room       *lksdk.Room
	mix        *mixer.Mixer
	out        *msdk.SwitchWriter
	videoOut   *rtp.WriteStreamSwitcher
	outDtmf    atomic.Pointer[dtmf.Writer]
	p          ParticipantInfo
	ready      core.Fuse
	subscribe  atomic.Bool
	subscribed core.Fuse
	stopped    core.Fuse
	closed     core.Fuse
	stats      *RoomStats

	videoMu       sync.RWMutex
	videoHandlers map[string]*videoTrackHandler
	activeSpeaker string
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
	r := &Room{
		log:           log,
		stats:         st,
		out:           msdk.NewSwitchWriter(RoomSampleRate),
		videoOut:      rtp.NewWriteStreamSwitcher(),
		videoHandlers: make(map[string]*videoTrackHandler),
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

func (r *Room) handleActiveSpeakersChanged(speakers []lksdk.Participant) {
	r.roomLog.Debugw("active speakers changed event received", "speakerCount", len(speakers))
	if len(speakers) == 0 {
		r.roomLog.Debugw("no active speakers")
		return
	}

	speaker := speakers[0]
	identity := speaker.Identity()

	r.videoMu.Lock()
	defer r.videoMu.Unlock()

	if r.activeSpeaker == identity {
		r.roomLog.Debugw("speaker is already active, skipping switch", "speaker", identity)
		return
	}

	log := r.roomLog.WithValues("newSpeaker", identity, "previousSpeaker", r.activeSpeaker)
	log.Infow("active speaker changed")

	previousSpeaker := r.activeSpeaker
	r.activeSpeaker = identity

	r.switchVideoOutput(identity, previousSpeaker, log)
}

func (r *Room) switchVideoOutput(newSpeaker, previousSpeaker string, log logger.Logger) {

	log.Debugw("attempting to switch video output",
		"fromSpeaker", previousSpeaker,
		"toSpeaker", newSpeaker,
		"totalHandlers", len(r.videoHandlers))

	handler, exists := r.videoHandlers[newSpeaker]
	if !exists {
		log.Debugw("no video handler found for new active speaker",
			"speaker", newSpeaker,
			"availableHandlers", r.getHandlerIdentities())
		return
	}

	if handler.handler == nil {
		log.Debugw("video handler not initialized for new active speaker",
			"speaker", newSpeaker,
			"participantID", handler.participantID)
		return
	}

	if previousSpeaker != "" {
		if prevHandler, exists := r.videoHandlers[previousSpeaker]; exists && prevHandler.handler != nil {
			prevHandler.handler.w = rtp.NewWriteStreamSwitcher()
			log.Debugw("disconnected previous speaker from video output",
				"previousSpeaker", previousSpeaker,
				"previousParticipantID", prevHandler.participantID)
		}
	}

	log.Infow("switching video output to active speaker",
		"newSpeaker", newSpeaker,
		"newParticipantID", handler.participantID,
		"previousSpeaker", previousSpeaker)

	oldWriter := handler.handler.w
	handler.handler.w = r.videoOut

	log.Debugw("video output switched successfully",
		"newSpeaker", newSpeaker,
		"oldWriter", oldWriter.String(),
		"newWriter", r.videoOut.String())
}

func (r *Room) getHandlerIdentities() []string {
	identities := make([]string, 0, len(r.videoHandlers))
	for identity := range r.videoHandlers {
		identities = append(identities, identity)
	}
	return identities
}

func (r *Room) cleanupVideoHandler(identity string) {
	r.videoMu.Lock()
	defer r.videoMu.Unlock()

	handler, exists := r.videoHandlers[identity]
	if !exists {
		return
	}
	defer handler.handler.Close()
	delete(r.videoHandlers, identity)

	log := r.roomLog.WithValues("participant", identity)
	log.Debugw("cleaning up video handler")

	if r.activeSpeaker == identity {
		r.activeSpeaker = ""
		log.Infow("active speaker left, clearing active speaker")
	}
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
	r.cleanupVideoHandler(rp.Identity())
}

func (r *Room) subscribeTo(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name(), "source", pub.Source().String())
	if pub.Kind() != lksdk.TrackKindAudio && pub.Kind() != lksdk.TrackKindVideo {
		log.Debugw("skipping non-audio/video track")
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
		OnActiveSpeakersChanged: func(speakers []lksdk.Participant) {
			r.handleActiveSpeakersChanged(speakers)
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
			OnDataPacket: func(data lksdk.DataPacket, params lksdk.DataReceiveParams) {
				switch data := data.(type) {
				case *livekit.SipDTMF:
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

func (r *Room) SwapVideoOutput(out rtp.WriteStreamCloser) rtp.WriteStreamCloser {
	return r.videoOut.Swap(out)
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

	r.closed.Break()
	r.subscribe.Store(false)
	err := r.CloseOutput()
	// stop video output and close the previous writer
	if w := r.SwapVideoOutput(nil); w != nil {
		if e := w.Close(); err == nil {
			err = e
		}
	}

	// close all video handlers and clear state
	var closers []*noErrHandlerCloser
	r.videoMu.Lock()
	for _, vh := range r.videoHandlers {
		if vh != nil && vh.handler != nil {
			closers = append(closers, vh.handler)
		}
	}
	r.videoHandlers = make(map[string]*videoTrackHandler)
	r.activeSpeaker = ""
	r.videoMu.Unlock()
	for _, h := range closers {
		h.Close()
	}

	r.SetDTMFOutput(nil)
	if r.room != nil {
		r.room.DisconnectWithReason(reason)
		r.room = nil
	}
	if r.mix != nil {
		r.mix.Stop()
	}
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

func (r *Room) NewParticipantVideoTrack() (rtp.WriteStream, error) {
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeVP8,
	}, "camera", "pion")
	if err != nil {
		r.log.Errorw("cannot create rtp track", err)
		return nil, nil
	}
	p := r.room.LocalParticipant
	if _, err = p.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: p.Identity(),
	}); err != nil {
		r.log.Errorw("cannot publish rtp track", err)
		return nil, nil
	}

	r.log.Infow("published video track", "trackID", track.ID(), "streamID", track.StreamID())

	w := rtp.NewPacketWriteStream(track)

	return w, nil
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

type noErrHandlerCloser struct {
	w rtp.WriteStreamCloser
}

func (n noErrHandlerCloser) String() string {
	return n.w.String()
}

func (n noErrHandlerCloser) HandleRTP(h *rtp.Header, payload []byte) error {
	_, err := n.w.WriteRTP(h, payload)
	return err
}

func (n noErrHandlerCloser) Close() {
	_ = n.w.Close()
}

func (r *Room) participantVideoTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", track.ID(), "trackName", pub.Name())
	if !r.ready.IsBroken() {
		log.Warnw("ignoring track, room not ready", nil)
		return
	}
	log.Infow("handling new video track")

	identity := rp.Identity()

	hnd := &noErrHandlerCloser{
		w: rtp.NewWriteStreamSwitcher(),
	}

	r.videoMu.Lock()
	defer r.videoMu.Unlock()
	handler := &videoTrackHandler{
		identity:      identity,
		participantID: rp.SID(),
		track:         track,
		handler:       hnd,
	}
	r.videoHandlers[identity] = handler

	shouldActivate := r.activeSpeaker == "" || r.activeSpeaker == identity
	if shouldActivate {
		r.activeSpeaker = identity
		hnd.w = r.videoOut
		log.Infow("setting as initial active speaker video",
			"identity", identity,
			"reason", map[bool]string{true: "first participant", false: "already active speaker"}[r.activeSpeaker == ""])
	} else {
		log.Debugw("participant video stored but not active",
			"identity", identity,
			"currentActiveSpeaker", r.activeSpeaker)
	}

	go func() {
		err := rtp.HandleLoop(track, hnd)
		if err != nil && !errors.Is(err, io.EOF) {
			log.Infow("room track rtp handler returned with failure", "error", err)
		}
	}()
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
