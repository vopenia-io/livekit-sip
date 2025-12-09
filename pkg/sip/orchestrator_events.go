package sip

import (
	"fmt"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/config"
	"github.com/pion/webrtc/v4"
)

func (o *MediaOrchestrator) LocalParticipantReady(p *lksdk.LocalParticipant) error {
	if err := o.tracks.ParticipantReady(p); err != nil {
		return fmt.Errorf("could not set participant ready: %w", err)
	}

	if o.camera.Status() == VideoStatusStarted {
		to, err := o.tracks.Camera()
		if err != nil {
			return fmt.Errorf("could not start room camera: %w", err)
		}
		if err := o.camera.WebrtcTrackOutput(to); err != nil {
			return fmt.Errorf("could not set webrtc track output: %w", err)
		}
	}
	return nil
}

func (o *MediaOrchestrator) cameraTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) error {
	if o.camera.Status() != VideoStatusStarted {
		return nil
	}
	ti := NewTrackInput(track, pub, rp)
	return o.camera.WebrtcTrackInput(ti, rp.SID(), uint32(track.SSRC()))
}

func (o *MediaOrchestrator) WebrtcTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) error {
	log := o.log.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
	switch pub.Kind() {
	case lksdk.TrackKindVideo:
		switch pub.Source() {
		case livekit.TrackSource_CAMERA:
			return o.cameraTrackSubscribed(track, pub, rp, conf)
		}
	}
	log.Warnw("unsupported track kind for subscription", fmt.Errorf("kind=%s", pub.Kind()))
	return nil
}

func (o *MediaOrchestrator) cameraTrackUnsubscribed(_ *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, _ *config.Config) error {
	if o.camera.Status() != VideoStatusStarted {
		return nil
	}
	return o.camera.RemoveWebrtcTrackInput(rp.SID())
}

func (o *MediaOrchestrator) WebrtcTrackUnsubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) error {
	log := o.log.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
	switch pub.Kind() {
	case lksdk.TrackKindVideo:
		switch pub.Source() {
		case livekit.TrackSource_CAMERA:
			return o.cameraTrackUnsubscribed(track, pub, rp, conf)
		}
	}
	log.Warnw("unsupported track kind for unsubscription", fmt.Errorf("kind=%s", pub.Kind()))
	return nil
}

func (o *MediaOrchestrator) ActiveParticipantChanged(p []lksdk.Participant) error {
	if o.camera.Status() != VideoStatusStarted {
		return nil
	}
	if len(p) == 0 {
		o.log.Debugw("no active speakers found")
		return nil
	}
	sid := p[0].SID()
	if err := o.camera.SwitchActiveWebrtcTrack(sid); err != nil {
		o.log.Warnw("could not switch active webrtc track", err, "sid", sid)
		return nil
	}
	return nil
}
