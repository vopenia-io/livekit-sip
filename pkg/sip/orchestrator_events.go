package sip

import (
	"fmt"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

func (o *MediaOrchestrator) LocalParticipantReady(p *lksdk.LocalParticipant) error {
	if err := o.okStates(MediaStateReady); err != nil {
		return err
	}
	if err := o.tracks.ParticipantReady(p); err != nil {
		return fmt.Errorf("could not set participant ready: %w", err)
	}

	// if err := o.Start(); err != nil {
	// 	return fmt.Errorf("could not start media orchestrator: %w", err)
	// }
	return nil
}

func (o *MediaOrchestrator) cameraTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	if o.camera.Status() != VideoStatusStarted {
		return nil
	}
	ti := NewTrackInput(track, pub, rp)
	return o.camera.WebrtcTrackInput(ti, rp.SID(), uint32(track.SSRC()))
}

func (o *MediaOrchestrator) WebrtcTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	return nil
	// log := o.log.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
	// switch pub.Kind() {
	// case lksdk.TrackKindVideo:
	// 	switch pub.Source() {
	// 	case livekit.TrackSource_CAMERA:
	// 		return o.tracks.CameraTracks.TrackSubscribed(track, pub, rp, o.cameraTrackSubscribed)
	// 	}
	// }
	// log.Warnw("unsupported track kind for subscription", fmt.Errorf("kind=%s", pub.Kind()))
	// return nil
}

func (o *MediaOrchestrator) cameraTrackUnsubscribed(_ *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	return nil
	// if o.camera.Status() != VideoStatusStarted {
	// 	return nil
	// }
	// return o.camera.RemoveWebrtcTrackInput(rp.SID())
}

func (o *MediaOrchestrator) WebrtcTrackUnsubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	return nil
	// log := o.log.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
	// switch pub.Kind() {
	// case lksdk.TrackKindVideo:
	// 	switch pub.Source() {
	// 	case livekit.TrackSource_CAMERA:
	// 		return o.tracks.CameraTracks.TrackUnsubscribed(track, pub, rp, o.cameraTrackUnsubscribed)
	// 	}
	// }
	// log.Warnw("unsupported track kind for unsubscription", fmt.Errorf("kind=%s", pub.Kind()))
	// return nil
}

func (o *MediaOrchestrator) ActiveParticipantChanged(p []lksdk.Participant) error {
	return nil
	// if o.camera.Status() != VideoStatusStarted {
	// 	return nil
	// }
	// if len(p) == 0 {
	// 	o.log.Debugw("no active speakers found")
	// 	return nil
	// }
	// sid := p[0].SID()
	// if err := o.camera.SwitchActiveWebrtcTrack(sid); err != nil {
	// 	o.log.Warnw("could not switch active webrtc track", err, "sid", sid)
	// 	return nil
	// }
	// return nil
}
