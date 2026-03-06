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
			if _, err := glib.IdleAdd(func() {
				e.Close()
			}); err != nil {
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add LivekitBin close to main loop: %v", err))
			}
		},
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if _, err := glib.IdleAdd(func() {
					e.livekitMu.Lock()
					defer e.livekitMu.Unlock()
					e.SubscribeTrack(track, publication, rp)
					time.Sleep(5*time.Millisecond)
				}); err != nil {
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track subscription to main loop: %v", err))
				}
			},
			OnTrackUnsubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if _, err := glib.IdleAdd(func() {
					e.livekitMu.Lock()
					defer e.livekitMu.Unlock()
					e.UnsubscribeTrack(track, publication, rp)
				}); err != nil {
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track unsubscription to main loop: %v", err))
				}
			},
			OnTrackPublished: e.OnTrackPublished,
		},
		OnActiveSpeakersChanged: func(p []lksdk.Participant) {
			// e.livekitMu.Lock()
			// defer e.livekitMu.Unlock()
			if _, err := glib.IdleAdd(func() {
				e.OnActiveSpeakersChanged(p)
			}); err != nil {
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add active speakers update to main loop: %v", err))
			}
		},
	}
}
