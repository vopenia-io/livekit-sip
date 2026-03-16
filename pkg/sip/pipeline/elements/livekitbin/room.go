package livekitbin

import (
	"fmt"
	"runtime"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/pion/webrtc/v4"
	"github.com/samber/lo"
)

func (e *LivekitBin) OnConnectSignal(instance *gst.Element) {
	self := gst.ToGstBin(instance)

	if err := e.Wait(RoomStatePlaying); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error waiting for room to be playing: %v", err))
		self.Error(fmt.Sprintf("Error waiting for room to be playing: %v", err), err)
		return
	}

	if e.Set(RoomStateJoining)&RoomStateJoining != 0 {
		self.Log(CAT, gst.LevelWarning, "Already joining a LiveKit room")
		return
	}

	defer func() {
		e.Unset(RoomStateJoining)
	}()

	if e.Is(RoomStateJoined) {
		self.Log(CAT, gst.LevelWarning, "Already connected to a LiveKit room")
		return
	}

	if e.wsURL == "" || e.token == "" {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("WebSocket URL and token must be set before connecting to a LiveKit room (ws-url: %s, token: %s)", e.wsURL, e.token))
		self.Error("WebSocket URL and token must be set before connecting to a LiveKit room", fmt.Errorf("invalid config: ws-url: %s, token: %s", e.wsURL, e.token))
		return
	}

	self.Log(CAT, gst.LevelInfo, "Connecting to LiveKit room...")
	if err := e.room.JoinWithToken(e.wsURL, e.token,
		lksdk.WithAutoSubscribe(false),
		lksdk.WithExtraAttributes(e.defaultParticipantAttributes),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to LiveKit room: %v", err))
		self.Error("Error connecting to LiveKit room", err)
		return
	}
	self.Log(CAT, gst.LevelInfo, "Successfully joined LiveKit room, waiting for connection to be established...")
	if err := roomWaitConnected(e.room); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error waiting for LiveKit room connection: %v", err))
		self.Error("Error waiting for LiveKit room connection", err)
		return
	}

	e.Set(RoomStateJoined)
	e.Unset(RoomStateJoining)
	self.Log(CAT, gst.LevelInfo, "Connected to LiveKit room")

	if err := e.setupRtcpSink(); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting up RTCP sink: %v", err))
		self.Error("Error setting up RTCP sink", err)
	}

	if _, err := self.Emit("connected"); err != nil {
		self.Log(CAT, gst.LevelError, "Error emitting connected signal")
		self.Error("Error emitting connected signal", err)
	}
}

func (e *LivekitBin) setupRtcpSink() error {
	rtcpSink, ok := gst.SubclassFromElement[*livekittracks.SinkRtcp](e.RtcpSink)
	if !ok {
		return fmt.Errorf("failed to get SinkRtcp subclass from element")
	}
	rtcpSink.Setup(e.RtcpSink, e.room.LocalParticipant.GetPublisherPeerConnection())

	return nil
}

func roomWaitConnected(room *lksdk.Room) error {
	for {
		state := room.ConnectionState()
		if state == lksdk.ConnectionStateConnected {
			break
		}
		if state == lksdk.ConnectionStateDisconnected {
			return fmt.Errorf("disconnected while joining room")
		}
		runtime.Gosched()
	}
	for _, pc := range []*webrtc.PeerConnection{
		room.LocalParticipant.GetPublisherPeerConnection(),
		room.LocalParticipant.GetSubscriberPeerConnection(),
	} {
		for lo.Contains([]webrtc.PeerConnectionState{
			webrtc.PeerConnectionStateNew,
			webrtc.PeerConnectionStateConnecting,
		}, pc.ConnectionState()) {
			runtime.Gosched()
		}
		if state := pc.ConnectionState(); state != webrtc.PeerConnectionStateConnected {
			return fmt.Errorf("peer connection not connected after joining room: %s", state.String())
		}
	}
	return nil
}

func (e *LivekitBin) Close() {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, "Closing LivekitBin and disconnecting from LiveKit room")

	e.Set(RoomStateClosed)

	if e.room == nil {
		return
	}

	if e.room.ConnectionState() != lksdk.ConnectionStateDisconnected {
		e.room.Disconnect()
	}

	e.UnsubscribeAll()

	if _, err := self.Emit("closed"); err != nil {
		self.Log(CAT, gst.LevelError, "Error emitting closed signal")
		self.Error("Error emitting closed signal", err)
	}
	self.Log(CAT, gst.LevelInfo, "Disconnected from LiveKit room")
}

func (e *LivekitBin) OnActiveSpeakersChanged(p []lksdk.Participant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Active speakers changed: %v", lo.Map(p, func(part lksdk.Participant, i int) string { return part.SID() })))

	if !e.Is(RoomStateJoined) {
		self.Log(CAT, gst.LevelWarning, "Received active speakers changed callback while not joined to a room")
		return
	}

	maxParticipants := e.maxActiveParticipants
	if maxParticipants == 0 {
		maxParticipants = MAX_ACTIVE_PARTICIPANTS
	}

	if len(p) >= int(maxParticipants) {
		p = p[:maxParticipants]
	} else {
		for _, sid := range e.activeSpeakers {
			if len(p) >= int(maxParticipants) {
				break
			}
			if lo.ContainsBy(p, func(part lksdk.Participant) bool { return part.SID() == sid }) {
				continue
			}
			part := e.room.GetParticipantBySID(sid)
			if part == nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Could not find participant with SID %s", sid))
				continue
			}
			p = append(p, part)
		}
	}

	e.updateActiveSpeakers(self, p)

	if e.maxActiveParticipants == 0 {
		return
	}

	// for _, part := range p {
	// 	rp, ok := part.(*lksdk.RemoteParticipant)
	// 	if !ok {
	// 		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Participant %s is not a remote participant", part.Identity()))
	// 		continue
	// 	}
	// 	camera, ok := rp.GetTrackPublication(livekit.TrackSource_CAMERA).(*lksdk.RemoteTrackPublication)
	// 	if !ok || camera == nil {
	// 		continue
	// 	}

	// 	if !camera.IsSubscribed() {
	// 		if err := camera.SetSubscribed(true); err != nil {
	// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to subscribe to camera track for participant %s: %v", rp.Identity(), err))
	// 		}
	// 	}

	// 	if !camera.IsEnabled() {
	// 		camera.SetEnabled(true)
	// 	}
	// }

	// remote := e.room.GetRemoteParticipants()
	// inactive := lo.Filter(remote, func(part *lksdk.RemoteParticipant, i int) bool {
	// 	return !lo.ContainsBy(p, func(active lksdk.Participant) bool {
	// 		return active.SID() == part.SID()
	// 	})
	// })
	// for _, part := range inactive {
	// 	camera, ok := part.GetTrackPublication(livekit.TrackSource_CAMERA).(*lksdk.RemoteTrackPublication)
	// 	if !ok || camera == nil {
	// 		continue
	// 	}
	// 	if camera.IsEnabled() {
	// 		camera.SetEnabled(false)
	// 	}
	// }
}

func (e *LivekitBin) OnTrackPublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	done := debugLock(fmt.Sprintf("OnTrackPublished %s", rp.SID()))
	defer close(done)
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track published by participant %s: %s (source: %s)", rp.Identity(), publication.Name(), publication.Source().String()))

	// if e.maxActiveParticipants != 0 {
	// 	if !lo.Contains(e.activeSpeakers, rp.SID()) && len(e.activeSpeakers) < int(e.maxActiveParticipants) {
	// 		p := append(e.getCurrentActiveSpeakers(), rp)
	// 		e.updateActiveSpeakers(self, p)
	// 	}
	// }

	switch publication.Source() {
	case livekit.TrackSource_MICROPHONE:
		if err := publication.SetSubscribed(true); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to subscribe to microphone track publication for participant %s: %v", rp.Identity(), err))
			self.Error(fmt.Sprintf("Failed to subscribe to microphone track publication for participant %s", rp.Identity()), err)
			return
		}
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Subscribed to microphone track publication for participant %s", rp.Identity()))
	case livekit.TrackSource_CAMERA:
		if err := publication.SetSubscribed(true); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to subscribe to microphone track publication for participant %s: %v", rp.Identity(), err))
			self.Error(fmt.Sprintf("Failed to subscribe to microphone track publication for participant %s", rp.Identity()), err)
			return
		}
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Subscribed to microphone track publication for participant %s", rp.Identity()))
	default:
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Not subscribing to track publication for participant %s of kind %s", rp.Identity(), publication.Source().String()))
		return
	}

	go func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.updateActiveSpeakers(self, append(e.getCurrentActiveSpeakers(), rp))
	}()
}

func (e *LivekitBin) OnParticipantConnected(rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Participant connected: %s", rp.SID()))
	if _, err := self.Emit("participant-join", rp.SID()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting participant-join signal: %v", err))
		self.Error("Error emitting participant-join signal", err)
		return
	}
}

func (e *LivekitBin) OnParticipantDisconnected(rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	// if lo.Contains(e.activeSpeakers, rp.SID()) {
	// 	p := e.getCurrentActiveSpeakers()
	// 	p = lo.Filter(p, func(part lksdk.Participant, idx int) bool {
	// 		return part.SID() != rp.SID()
	// 	})
	// 	e.updateActiveSpeakers(self, p)
	// }

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Participant disconnected: %s", rp.SID()))
	if _, err := self.Emit("participant-left", rp.SID()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting participant-left signal: %v", err))
		self.Error("Error emitting participant-left signal", err)
		return
	}
}
