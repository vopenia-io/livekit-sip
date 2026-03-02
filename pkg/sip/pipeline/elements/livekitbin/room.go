package livekitbin

import (
	"fmt"
	"runtime"
	"slices"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/tracks"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/vp8h264select"
	"github.com/pion/webrtc/v4"
)

func (e *LivekitBin) OnConnectSignal(instance *gst.Element) {
	self := gst.ToGstBin(instance)

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
		lksdk.WithAutoSubscribe(true), // TODO: maybe manually subscribe when we will limit tracks to 6 for tilling
		lksdk.WithExtraAttributes(e.defaultParticipantAttributes),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to LiveKit room: %v", err))
		self.Error("Error connecting to LiveKit room", err)
		return
	}
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
	rtcpSink, ok := gst.SubclassFromElement[*tracks.SinkRtcp](e.RtcpSink)
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
		for slices.Contains([]webrtc.PeerConnectionState{
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

func (e *LivekitBin) OnActiveSpeakersChanged(p []lksdk.Participant) {
	if len(p) == 0 {
		return
	}
	// e.callbackMu.Lock()
	// defer e.callbackMu.Unlock()

	self := gst.ToGstBin(e.self.Get())
	if self == nil {
		CAT.Log(gst.LevelError, "Failed to get parent bin")
		return
	}
	if !e.Is(RoomStateJoined) {
		self.Log(CAT, gst.LevelWarning, "Received active speakers changed callback while not joined to a room")
		return
	}

	var ssrcs []uint32

	for _, part := range p {
		if !part.IsCameraEnabled() {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Skipping participant %s because camera is not enabled", part.Identity()))
			continue
		}
		pub, ok := part.GetTrackPublication(livekit.TrackSource_CAMERA).(*lksdk.RemoteTrackPublication)
		if !ok {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Skipping participant %s because no camera track publication found", part.Identity()))
			continue
		}

		if !pub.IsSubscribed() {
			continue
		}

		ssrcs = append(ssrcs, uint32(pub.TrackRemote().SSRC()))
	}
	if len(ssrcs) == 0 {
		return
	}

	pads, err := self.GetSrcPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get src pads: %v", err))
		return
	}

	for _, pad := range pads {
		pname := pad.GetName()
		var session, ssrc, pt int
		if _, err := fmt.Sscanf(pname, "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to parse pad name %s: %v", pname, err))
			continue
		}
		if session != int(livekit.TrackSource_CAMERA) {
			continue
		}
		if !slices.Contains(ssrcs, uint32(ssrc)) {
			continue
		}
		structure := gst.NewStructure(vp8h264select.SelectEventName)
		runtime.SetFinalizer(structure, nil) // give ownership to the event
		if !pad.PushEvent(gst.NewCustomEvent(gst.EventTypeCustomDownstream, structure)) {
			err := fmt.Errorf("failed to send selector event on pad %s with structure %v", pname, structure.Values())
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error sending selector event on pad %s: %v", pname, err))
			self.Error(fmt.Sprintf("Error sending selector event on pad %s", pname), err)
			continue
		}
	}
}
