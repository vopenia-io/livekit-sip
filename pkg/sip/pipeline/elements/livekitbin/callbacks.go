package livekitbin

import (
	"fmt"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

func debugLock(name string) chan struct{} {
	ch := make(chan struct{})
	go func() {
		start := time.Now()
		for {
			select {
			case <-ch:
				return
			case <-time.After(5 * time.Second):
				fmt.Print("\n\n\n================================================================\n")
				fmt.Printf("Lock %s held for %v\n", name, time.Since(start))
				fmt.Print("================================================================\n\n\n\n")
			}
		}
	}()

	return ch
}

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
			done := debugLock(fmt.Sprintf("OnParticipantConnected %s", rp.SID()))
			e.livekitMu.Lock()
			if _, err := glib.IdleAdd(func() {
				defer close(done)
				defer e.livekitMu.Unlock()
				e.OnParticipantConnected(rp)
			}); err != nil {
				e.livekitMu.Unlock()
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add participant connection to main loop: %v", err))
			}
		},
		OnParticipantDisconnected: func(rp *lksdk.RemoteParticipant) {
			done := debugLock(fmt.Sprintf("OnParticipantDisconnected %s", rp.SID()))
			e.livekitMu.Lock()
			if _, err := glib.IdleAdd(func() {
				defer close(done)
				defer e.livekitMu.Unlock()
				e.OnParticipantDisconnected(rp)
			}); err != nil {
				e.livekitMu.Unlock()
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add participant disconnection to main loop: %v", err))
			}
		},
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				done := debugLock(fmt.Sprintf("OnTrackSubscribed %s", rp.SID()))
				e.livekitMu.Lock()
				if _, err := glib.IdleAdd(func() {
					defer close(done)
					defer e.livekitMu.Unlock()
					e.SubscribeTrack(track, publication, rp)
					time.Sleep(5 * time.Millisecond)
				}); err != nil {
					e.livekitMu.Unlock()
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track subscription to main loop: %v", err))
				}
			},
			OnTrackUnsubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				done := debugLock(fmt.Sprintf("OnTrackUnsubscribed %s", rp.SID()))
				e.livekitMu.Lock()
				if _, err := glib.IdleAdd(func() {
					defer close(done)
					defer e.livekitMu.Unlock()
					e.UnsubscribeTrack(track, publication, rp)
				}); err != nil {
					e.livekitMu.Unlock()
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track unsubscription to main loop: %v", err))
				}
			},
			OnTrackPublished: e.OnTrackPublished,
		},
		OnActiveSpeakersChanged: func(p []lksdk.Participant) {
			done := debugLock("OnActiveSpeakersChanged")
			e.livekitMu.Lock()
			if _, err := glib.IdleAdd(func() {
				defer close(done)
				defer e.livekitMu.Unlock()
				e.OnActiveSpeakersChanged(p)
			}); err != nil {
				e.livekitMu.Unlock()
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add active speakers update to main loop: %v", err))
			}
		},
	}
}
