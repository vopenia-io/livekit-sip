package lkroom

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// type ParticipantCallback struct {
// 	// for local participant
// 	OnLocalTrackPublished   func(publication *LocalTrackPublication, lp *LocalParticipant)
// 	OnLocalTrackUnpublished func(publication *LocalTrackPublication, lp *LocalParticipant)

// 	// for all participants
// 	OnTrackMuted               func(pub TrackPublication, p Participant)
// 	OnTrackUnmuted             func(pub TrackPublication, p Participant)
// 	OnMetadataChanged          func(oldMetadata string, p Participant)
// 	OnAttributesChanged        ParticipantAttributesChangedFunc
// 	OnIsSpeakingChanged        func(p Participant)
// 	OnConnectionQualityChanged func(update *livekit.ConnectionQualityInfo, p Participant)

// 	// for remote participants
// 	OnTrackSubscribed         func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant)
// 	OnTrackUnsubscribed       func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant)
// 	OnTrackSubscriptionFailed func(sid string, rp *RemoteParticipant)
// 	OnTrackPublished          func(publication *RemoteTrackPublication, rp *RemoteParticipant)
// 	OnTrackUnpublished        func(publication *RemoteTrackPublication, rp *RemoteParticipant)
// 	OnDataReceived            func(data []byte, params DataReceiveParams) // Deprecated: Use OnDataPacket instead
// 	OnDataPacket              func(data DataPacket, params DataReceiveParams)
// 	OnTranscriptionReceived   func(transcriptionSegments []*TranscriptionSegment, p Participant, publication TrackPublication)
// }

// type RoomCallback struct {
// 	OnDisconnected            func()
// 	OnDisconnectedWithReason  func(reason DisconnectionReason)
// 	OnParticipantConnected    func(*RemoteParticipant)
// 	OnParticipantDisconnected func(*RemoteParticipant)
// 	OnActiveSpeakersChanged   func([]Participant)
// 	OnRoomMetadataChanged     func(metadata string)
// 	OnRoomMoved               func(roomName string, token string)
// 	OnReconnecting            func()
// 	OnReconnected             func()
// 	OnLocalTrackSubscribed    func(publication *LocalTrackPublication, lp *LocalParticipant)

// 	// participant events are sent to the room as well
// 	ParticipantCallback
// }

func (s *lkroom) toCallbacks() *lksdk.RoomCallback {
	return &lksdk.RoomCallback{
		OnDisconnected:            s.OnDisconnected,
		OnDisconnectedWithReason:  s.OnDisconnectedWithReason,
		OnParticipantConnected:    s.OnParticipantConnected,
		OnParticipantDisconnected: s.OnParticipantDisconnected,
		OnActiveSpeakersChanged:   s.OnActiveSpeakersChanged,
		OnRoomMetadataChanged:     s.OnRoomMetadataChanged,
		OnRoomMoved:               s.OnRoomMoved,
		OnReconnecting:            s.OnReconnecting,
		OnReconnected:             s.OnReconnected,
		OnLocalTrackSubscribed:    s.OnLocalTrackSubscribed,

		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackMuted:               s.OnTrackMuted,
			OnTrackUnmuted:             s.OnTrackUnmuted,
			OnMetadataChanged:          s.OnMetadataChanged,
			OnAttributesChanged:        s.OnAttributesChanged,
			OnIsSpeakingChanged:        s.OnIsSpeakingChanged,
			OnConnectionQualityChanged: s.OnConnectionQualityChanged,
			OnTrackSubscribed:          s.OnTrackSubscribed,
			OnTrackUnsubscribed:        s.OnTrackUnsubscribed,
			OnTrackSubscriptionFailed:  s.OnTrackSubscriptionFailed,
			OnTrackPublished:           s.OnTrackPublished,
			OnTrackUnpublished:         s.OnTrackUnpublished,
			OnDataPacket:               s.OnDataPacket,
			OnTranscriptionReceived:    s.OnTranscriptionReceived,
		},
	}
}

func (s *lkroom) OnDisconnected() {
	s.self.Log(CAT, gst.LevelInfo, "Room disconnected")
	s.callbacks.OnDisconnected()
	// if err := s.self.SetState(gst.StateNull); err != nil {
	// 	s.self.Log(CAT, gst.LevelError, fmt.Sprintf("Could not set state to NULL after reconnection: %v", err))
	// }
	// s.self = nil
	// s.SinkRTCP = nil
}

func (s *lkroom) OnDisconnectedWithReason(reason lksdk.DisconnectionReason) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Room disconnected with reason: %s", reason))
	s.callbacks.OnDisconnectedWithReason(reason)
}

func (s *lkroom) OnParticipantConnected(p *lksdk.RemoteParticipant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Participant connected: %s", p.Identity()))
	s.callbacks.OnParticipantConnected(p)
}

func (s *lkroom) OnParticipantDisconnected(p *lksdk.RemoteParticipant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Participant disconnected: %s", p.Identity()))
	s.callbacks.OnParticipantDisconnected(p)
}

func (s *lkroom) OnActiveSpeakersChanged(participants []lksdk.Participant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Active speakers changed: %d participants", len(participants)))
	s.SelectActiveSpeaker(participants)
	s.callbacks.OnActiveSpeakersChanged(participants)
}

func (s *lkroom) OnRoomMetadataChanged(metadata string) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Room metadata changed: %s", metadata))
	s.callbacks.OnRoomMetadataChanged(metadata)
}

func (s *lkroom) OnRoomMoved(roomName string, token string) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Room moved to: %s", roomName))
	s.callbacks.OnRoomMoved(roomName, token)
}

func (s *lkroom) OnReconnecting() {
	s.self.Log(CAT, gst.LevelInfo, "Room reconnecting")
	s.callbacks.OnReconnecting()
}

func (s *lkroom) OnReconnected() {
	s.self.Log(CAT, gst.LevelInfo, "Room reconnected")
	s.callbacks.OnReconnected()
}

func (s *lkroom) OnLocalTrackSubscribed(publication *lksdk.LocalTrackPublication, lp *lksdk.LocalParticipant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Local track subscribed: %s", publication.SID()))
	s.callbacks.OnLocalTrackSubscribed(publication, lp)
}

func (s *lkroom) OnLocalTrackPublished(publication *lksdk.LocalTrackPublication, lp *lksdk.LocalParticipant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Local track published: %s", publication.SID()))
	s.callbacks.OnLocalTrackPublished(publication, lp)
}

func (s *lkroom) OnLocalTrackUnpublished(publication *lksdk.LocalTrackPublication, lp *lksdk.LocalParticipant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Local track unpublished: %s", publication.SID()))
	s.callbacks.OnLocalTrackUnpublished(publication, lp)
}

func (s *lkroom) OnTrackMuted(pub lksdk.TrackPublication, p lksdk.Participant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track muted: %s", pub.SID()))
	s.callbacks.OnTrackMuted(pub, p)
}

func (s *lkroom) OnTrackUnmuted(pub lksdk.TrackPublication, p lksdk.Participant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track unmuted: %s", pub.SID()))
	s.callbacks.OnTrackUnmuted(pub, p)
}

func (s *lkroom) OnMetadataChanged(oldMetadata string, p lksdk.Participant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Metadata changed from '%s' to '%s'", oldMetadata, p.Metadata()))
	s.callbacks.OnMetadataChanged(oldMetadata, p)
}

func (s *lkroom) OnAttributesChanged(changed map[string]string, p lksdk.Participant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Attributes changed: %v", changed))
	s.callbacks.OnAttributesChanged(changed, p)
}

func (s *lkroom) OnIsSpeakingChanged(p lksdk.Participant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("IsSpeaking changed: %v", p.IsSpeaking()))
	s.callbacks.OnIsSpeakingChanged(p)
}

func (s *lkroom) OnConnectionQualityChanged(update *livekit.ConnectionQualityInfo, p lksdk.Participant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Connection quality changed: %+v", update))
	s.callbacks.OnConnectionQualityChanged(update, p)
}

func (s *lkroom) OnTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track subscribed: %s", publication.SID()))
	s.SometimesTrackAdded(track, publication, rp)
	s.callbacks.OnTrackSubscribed(track, publication, rp)
}

func (s *lkroom) OnTrackUnsubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track unsubscribed: %s", publication.SID()))
	s.callbacks.OnTrackUnsubscribed(track, publication, rp)
}

func (s *lkroom) OnTrackSubscriptionFailed(sid string, rp *lksdk.RemoteParticipant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track subscription failed: %s", sid))
	s.callbacks.OnTrackSubscriptionFailed(sid, rp)
}

func (s *lkroom) OnTrackPublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track published: %s", publication.SID()))
	s.callbacks.OnTrackPublished(publication, rp)
}

func (s *lkroom) OnTrackUnpublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track unpublished: %s", publication.SID()))
	s.callbacks.OnTrackUnpublished(publication, rp)
}

func (s *lkroom) OnDataPacket(data lksdk.DataPacket, params lksdk.DataReceiveParams) {
	s.self.Log(CAT, gst.LevelInfo, "Data packet received")
	s.callbacks.OnDataPacket(data, params)
}

func (s *lkroom) OnTranscriptionReceived(transcriptionSegments []*lksdk.TranscriptionSegment, p lksdk.Participant, publication lksdk.TrackPublication) {
	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Transcription received: %d segments", len(transcriptionSegments)))
	s.callbacks.OnTranscriptionReceived(transcriptionSegments, p, publication)
}
