package livekitbin

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

func (e *LivekitBin) callabcks() *lksdk.RoomCallback {
	return &lksdk.RoomCallback{
		OnDisconnected: func() {
			self := gst.ToGstBin(e.self.Get())
			if self == nil {
				return
			}
			if _, err := self.Emit("closed"); err != nil {
				self.Log(CAT, gst.LevelError, "Error emitting closed signal")
				self.Error("Error emitting closed signal", err)
			}
			self.Log(CAT, gst.LevelInfo, "Disconnected from LiveKit room")
		},
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if _, err := glib.IdleAdd(func() {
					e.SubscribeTrack(track, publication, rp)
				}); err != nil {
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track subscription to main loop: %v", err))
				}
			},
			OnTrackUnsubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if _, err := glib.IdleAdd(func() {
					e.UnsubscribeTrack(track, publication, rp)
				}); err != nil {
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track unsubscription to main loop: %v", err))
				}
			},
		},
		OnActiveSpeakersChanged: func(p []lksdk.Participant) {
			if _, err := glib.IdleAdd(func() {
				e.OnActiveSpeakersChanged(p)
			}); err != nil {
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add active speakers update to main loop: %v", err))
			}
		},
	}
}
