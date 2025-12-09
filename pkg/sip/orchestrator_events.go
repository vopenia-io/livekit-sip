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

	to, err := o.tracks.Camera()
	if err != nil {
		return fmt.Errorf("could not start room camera: %w", err)
	}
	return o.camera.WebrtcTrackOutput(to)
}

func (o *MediaOrchestrator) cameraTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) error {
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

func (o *MediaOrchestrator) WebrtcTrackUnsubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) error {
	o.log.Warnw("WebrtcTrackUnsubscribed not implemented", nil)
	return nil
}

func (o *MediaOrchestrator) ActiveParticipantChanged(p []lksdk.Participant) error {
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
