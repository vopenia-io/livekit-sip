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
	"io"
	"log/slog"
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

	// Video support
	videoOut *msdk.FrameSwitchWriter
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
	r := &Room{log: log, stats: st, out: msdk.NewSwitchWriter(RoomSampleRate), videoOut: msdk.NewFrameSwitchWriter(90000)}
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
}

func (r *Room) subscribeTo(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
	if pub.Kind() != lksdk.TrackKindAudio && pub.Kind() != lksdk.TrackKindVideo {
		log.Debugw("skipping non-audio/video track")
		return
	}
	log.Debugw("subscribing to a track", "kind", pub.Kind())
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
				log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", track.ID(), "trackName", pub.Name())
				if !r.ready.IsBroken() {
					log.Warnw("ignoring track, room not ready", nil)
					return
				}
				log.Infow("mixing track", "kind", pub.Kind())

				go func() {
					if pub.Kind() == lksdk.TrackKindAudio {
						r.handleAudioTrack(track, pub, rp, log, conf)
					} else if pub.Kind() == lksdk.TrackKindVideo {
						r.handleVideoTrack(track, pub, rp, log, conf)
					}
				}()
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

func (r *Room) SwapVideoOutput(out msdk.FrameWriter) msdk.FrameWriter {
	if r == nil {
		return nil
	}
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

// // SetVideoOutput sets the video output writer for the room.
// func (r *Room) SetVideoOutput(w msdk.Writer[VideoFrame]) {
// 	if r == nil {
// 		return
// 	}
// 	if w == nil {
// 		r.videoOut.Store(nil)
// 		return
// 	}
// 	r.videoOut.Store(&w)
// }

// // GetVideoOutput returns the current video output writer.
// func (r *Room) GetVideoOutput() msdk.Writer[VideoFrame] {
// 	if r == nil {
// 		return nil
// 	}
// 	ptr := r.videoOut.Load()
// 	if ptr == nil {
// 		return nil
// 	}
// 	return *ptr
// }

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

func (r *Room) NewParticipantVideoTrack(sampleRate int) (msdk.WriteCloser[msdk.FrameSample], error) {
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeH264,
	}, string(lksdk.TrackKindVideo), "pion")
	if err != nil {
		return nil, err
	}
	p := r.room.LocalParticipant
	slog.Info("publishing track ben lalalolo", "participant", p.Identity(), "pID", p.SID(), "trackID", track.ID())
	if _, err = p.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: p.Identity(),
	}); err != nil {
		return nil, err
	}
	ow := msdk.FromSampleWriter[msdk.FrameSample](track, sampleRate, rtp.DefFrameDur)
	return ow, nil
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

	// go func()
	// {
	// 	file := "/home/maxime/Downloads/output.ivf"
	// 	videoWidth := 854
	// 	videoHeight := 480
	// 	track, err := lksdk.NewLocalFileTrack(file,
	// 		// control FPS to ensure synchronization
	// 		lksdk.ReaderTrackWithFrameDuration(33*time.Millisecond),
	// 		lksdk.ReaderTrackWithOnWriteComplete(func() { fmt.Println("track finished") }),
	// 	)
	// 	if err != nil {
	// 		panic(err)
	// 	}
	// 	if _, err = r.room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
	// 		Name:        file,
	// 		VideoWidth:  videoWidth,
	// 		VideoHeight: videoHeight,
	// 	}); err != nil {
	// 		panic(err)
	// 	}
	// 	slog.Info("publishing video track lalalala", "trackID", track.ID(), "file", file, "width", videoWidth, "height", videoHeight)
	// }

	return pw, nil
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

func (r *Room) handleAudioTrack(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, log logger.Logger, conf *config.Config) {
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
}

// func (r *Room) handleVideoTrack(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, log logger.Logger, conf *config.Config) {
// 	videoOut := r.videoOut.Load()
// 	if videoOut == nil {
// 		log.Debugw("no video output set, ignoring video track")
// 		return
// 	}

// 	in := newRTPReaderCount(track, &r.stats.InputPackets, &r.stats.InputBytes)

// 	// For now, we'll use a simple pass-through for video
// 	// In a real implementation, you'd want proper H.264 decoding
// 	var h rtp.HandlerCloser = rtp.NewNopCloser(rtp.NewMediaStreamIn[VideoFrame](*videoOut))
// 	if conf.EnableJitterBuffer {
// 		h = rtp.HandleJitter(h)
// 	}
// 	err := rtp.HandleLoop(in, h)
// 	if err != nil && !errors.Is(err, io.EOF) {
// 		log.Infow("room video track rtp handler returned with failure", "error", err)
// 	}
// }

func (r *Room) handleVideoTrack(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, log logger.Logger, conf *config.Config) {
	r.log.Debugw("handling video track", "trackID", track.ID(), "streamID", track.StreamID(), "codec", track.Codec().MimeType)

	var h rtp.HandlerCloser = rtp.NewNopCloser(rtp.NewMediaStreamIn(r.videoOut))

	err := rtp.HandleLoop(track, h)
	if err != nil && !errors.Is(err, io.EOF) {
		log.Infow("room track rtp handler returned with failure", "error", err)
	}
}
