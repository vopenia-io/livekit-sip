package livekitbin

import (
	"fmt"
	"runtime"
	"slices"

	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/tracks"
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
