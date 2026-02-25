package livekitbin

import (
	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
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
			OnTrackSubscribed:   e.SubscribeTrack,
			OnTrackUnsubscribed: e.UnsubscribeTrack,
		},
	}
}
