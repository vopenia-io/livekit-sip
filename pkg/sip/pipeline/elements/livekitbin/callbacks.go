package livekitbin

import (
	"fmt"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

func (e *LivekitBin) callabcks() *lksdk.RoomCallback {
	return &lksdk.RoomCallback{
		OnDisconnected: func() {
			self := gst.ToGstBin(e.self.Get())
			if self == nil || self.Instance() == nil {
				return
			}
			self.Log(CAT, gst.LevelInfo, "Disconnected from LiveKit room, closing LivekitBin")
			e.livekitMu.Lock()
			if _, err := glib.IdleAdd(func() {
				defer e.livekitMu.Unlock()
				e.Close()
			}); err != nil {
				e.livekitMu.Unlock()
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add LivekitBin close to main loop: %v", err))
			}
		},
		OnParticipantConnected: func(rp *lksdk.RemoteParticipant) {
			e.livekitMu.Lock()
			if _, err := glib.IdleAdd(func() {
				defer e.livekitMu.Unlock()
				e.OnParticipantConnected(rp)
			}); err != nil {
				e.livekitMu.Unlock()
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add participant connection to main loop: %v", err))
			}
		},
		OnParticipantDisconnected: func(rp *lksdk.RemoteParticipant) {
			e.livekitMu.Lock()
			if _, err := glib.IdleAdd(func() {
				defer e.livekitMu.Unlock()
				e.OnParticipantDisconnected(rp)
			}); err != nil {
				e.livekitMu.Unlock()
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add participant disconnection to main loop: %v", err))
			}
		},
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				e.livekitMu.Lock()
				if _, err := glib.IdleAdd(func() {
					defer e.livekitMu.Unlock()
					e.SubscribeTrack(track, publication, rp)
					time.Sleep(5 * time.Millisecond)
				}); err != nil {
					e.livekitMu.Unlock()
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track subscription to main loop: %v", err))
				}
			},
			OnTrackUnsubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				e.livekitMu.Lock()
				if _, err := glib.IdleAdd(func() {
					defer e.livekitMu.Unlock()
					e.UnsubscribeTrack(track, publication, rp)
				}); err != nil {
					e.livekitMu.Unlock()
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track unsubscription to main loop: %v", err))
				}
			},
			OnTrackPublished: e.OnTrackPublished,
			OnTrackMuted: func(pub lksdk.TrackPublication, p lksdk.Participant) {
				e.livekitMu.Lock()
				if _, err := glib.IdleAdd(func() {
					defer e.livekitMu.Unlock()
					e.OnTrackMuted(pub, p)
				}); err != nil {
					e.livekitMu.Unlock()
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track muted to main loop: %v", err))
				}
			},
			OnTrackUnmuted: func(pub lksdk.TrackPublication, p lksdk.Participant) {
				e.livekitMu.Lock()
				if _, err := glib.IdleAdd(func() {
					defer e.livekitMu.Unlock()
					e.OnTrackUnmuted(pub, p)
				}); err != nil {
					e.livekitMu.Unlock()
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track unmuted to main loop: %v", err))
				}
			},
		},
		OnActiveSpeakersChanged: func(p []lksdk.Participant) {
			e.livekitMu.Lock()
			if _, err := glib.IdleAdd(func() {
				defer e.livekitMu.Unlock()
				e.OnActiveSpeakersChanged(p)
			}); err != nil {
				e.livekitMu.Unlock()
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add active speakers update to main loop: %v", err))
			}
		},
	}
}
