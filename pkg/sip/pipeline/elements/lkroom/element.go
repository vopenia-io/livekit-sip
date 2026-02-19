package lkroom

import (
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

var CAT = gst.NewDebugCategory(
	"lkroom",
	gst.DebugColorFgGreen,
	"lkroom Element",
)

type config struct {
	AutoJoin bool
	Token    string
	WsURL    string
	Opt      []lksdk.ConnectOption
}

type state struct {
	mu         sync.Mutex
	joined     atomic.Bool
	closed     bool
	joinedCond *sync.Cond
}

func (s *state) IsJoined() bool {
	return s.joined.Load()
}

func (s *state) SetJoined(joined bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.joined.Store(joined)
	s.joinedCond.Broadcast()
}

func (s *state) WaitJoined() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for !s.joined.Load() && !s.closed {
		s.joinedCond.Wait()
	}
	return s.joined.Load()
}

type lkroom struct {
	err error // irrecoverable error during initialization

	self *gst.Bin

	state state
	config

	room      *lksdk.Room
	callbacks *lksdk.RoomCallback
}

func (*lkroom) New() glib.GoObjectSubclass {
	l := &lkroom{}
	return l
}

func (*lkroom) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"LiveKit Room",
		"Source/Sink",
		"Element to connect to a LiveKit room",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	gst.SignalNew(
		class.Type(),
		"join-room",
		gst.SignalRunLast,
		glib.TYPE_NONE,
	)

	gst.SignalNew(
		class.Type(),
		"room-joined",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_BOOLEAN, // success
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")

	for _, kind := range []livekit.TrackSource{ // dirty hack to allow auto id with static kind
		livekit.TrackSource_CAMERA,
		livekit.TrackSource_MICROPHONE,
		livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_SCREEN_SHARE_AUDIO,
	} {
		class.AddPadTemplate(gst.NewPadTemplate(
			fmt.Sprintf("sink_%d_%%u", kind),
			gst.PadDirectionSink,
			gst.PadPresenceRequest,
			gst.NewCapsFromString("application/x-rtp")))
	}

	// class.AddPadTemplate(gst.NewPadTemplate(
	// 	"sink_rtcp",
	// 	gst.PadDirectionSink,
	// 	gst.PadPresenceAlways,
	// 	gst.NewCapsFromString("application/x-rtcp")))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_%u_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewCapsFromString("application/x-rtp")))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_%u_%u_rtcp",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewCapsFromString("application/x-rtcp")))

	CAT.Log(gst.LevelDebug, "Installing properties")
	class.InstallProperties(properties)
}

func (s *lkroom) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	class := gst.ToElementClass(self.Class())
	self.Log(CAT, gst.LevelDebug, "InstanceInit")

	s.self = self

	var err error
	defer func() { s.err = err }()

	s.callbacks = lksdk.NewRoomCallback()
	s.config = config{
		AutoJoin: true,
	}

	s.state.joinedCond = sync.NewCond(&s.state.mu)

	s.room = lksdk.NewRoom(s.toCallbacks())

	sinkRTCP, err := gst.NewElement("lkroom_sinkrtcp")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating sink_rtcp %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error creating sink_rtcp", err.Error())
		return
	}

	if obj, ok := gst.SubclassFromElement[*sinkRtcp](sinkRTCP); ok {
		obj.parent = s
	} else {
		self.Log(CAT, gst.LevelError, "Error casting sink_rtcp to sinkRtcp subclass")
	}

	if err := self.AddMany(sinkRTCP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding elements to bin: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error adding elements to bin", err.Error())
		return
	}

	gsinkRtcp := gst.NewGhostPadFromTemplate("sink_rtcp", sinkRTCP.GetStaticPad("sink"), class.GetPadTemplate("sink_rtcp"))
	gsinkRtcp.Pad.AddProbe(s.padWaitReadyProbe())
	self.AddPad(gsinkRtcp.Pad)
}

func (s *lkroom) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)

	var (
		err error
	)
	_, err = self.Connect("join-room", func(instance *gst.Element) {
		self := gst.ToGstBin(instance)
		go func() {
			err := s.joinRoom(self)
			if err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Error joining room: %v", err))
				self.Error("Error joining room", err)
				return
			}
		}()
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting join-room signal: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error connecting join-room signal", err.Error())
	}
}

func (s *lkroom) start(self *gst.Bin) gst.StateChangeReturn {
	if s.state.IsJoined() {
		self.Log(CAT, gst.LevelInfo, "Already joined room")
		return gst.StateChangeSuccess
	}

	if s.AutoJoin {
		go func() {
			self.Log(CAT, gst.LevelInfo, "Auto-joining room")
			err := s.joinRoom(self)
			if err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to room: %v", err))
				self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error connecting to room", err.Error())
				return
			}
		}()
	}

	return gst.StateChangeSuccess
}

func (s *lkroom) joinRoom(self *gst.Bin) error {
	self.Log(CAT, gst.LevelDebug, "joinRoom")
	if s.state.IsJoined() {
		self.Log(CAT, gst.LevelInfo, "Already joined room")
		return nil
	}
	defer func() {
		if _, err := self.Emit("room-joined", s.state.IsJoined()); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting room-joined signal: %v", err))
			self.Error("Error emitting room-joined signal", err)
		}
	}()
	err := s.room.JoinWithToken(s.WsURL, s.Token, s.Opt...)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to room: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error connecting to room", err.Error())
		return fmt.Errorf("error connecting to room: %w", err)
	}
	for {
		state := s.room.ConnectionState()
		if state == lksdk.ConnectionStateConnected {
			break
		}
		if state == lksdk.ConnectionStateDisconnected {
			return fmt.Errorf("disconnected while joining room")
		}
		runtime.Gosched()
	}
	for _, pc := range []*webrtc.PeerConnection{
		s.room.LocalParticipant.GetPublisherPeerConnection(),
		s.room.LocalParticipant.GetSubscriberPeerConnection(),
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
	s.state.SetJoined(true)
	self.Log(CAT, gst.LevelInfo, "Successfully joined room")
	return nil
}

func (s *lkroom) close(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "close")

	s.state.mu.Lock()
	s.state.closed = true
	s.state.joinedCond.Broadcast()
	s.state.mu.Unlock()

	s.room.Disconnect()

	return gst.StateChangeSuccess
}

func (s *lkroom) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)
	if s.err != nil {
		if transition == gst.StateChangeReadyToNull {
			return self.ParentChangeState(transition)
		}
		return gst.StateChangeFailure
	}
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("ChangeState: %v", transition))

	switch transition {
	case gst.StateChangeReadyToPaused:
		if ret := s.start(self); ret == gst.StateChangeFailure {
			return ret
		}
	}

	ret := self.ParentChangeState(transition)
	if ret == gst.StateChangeFailure {
		return ret
	}

	switch transition {
	case gst.StateChangeReadyToNull:
		return s.close(self)
	}

	return ret
}

func (s *lkroom) padWaitReadyProbe() (gst.PadProbeType, gst.PadProbeCallback) {
	sweak := weak.Make(s)
	return gst.PadProbeTypeBuffer | gst.PadProbeTypeBufferList, func(p *gst.Pad, ppi *gst.PadProbeInfo) gst.PadProbeReturn {
		s := sweak.Value()
		if s == nil {
			return gst.PadProbeRemove
		}
		if !s.state.IsJoined() {
			return gst.PadProbeDrop
		}
		return gst.PadProbeRemove
	}
}

func (s *lkroom) startTrack(self *gst.Bin, cfg TrackCfg) *gst.Pad {
	self.Log(CAT, gst.LevelDebug, "startTrack")
	sink, err := NewTrackSink(s, cfg)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating sink_track: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error creating sink_track", err.Error())
		return nil
	}

	if err := self.Add(sink); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding sink_track to bin: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error adding sink_track to bin", err.Error())
		return nil
	}

	class := gst.ToElementClass(self.Class())

	pname := fmt.Sprintf("sink_%d_%d", cfg.Kind, cfg.ID)
	tmplname := fmt.Sprintf("sink_%d_%%u", cfg.Kind)
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Creating ghost pad %s", pname))

	pad := sink.GetStaticPad("sink")

	gsinkSink := gst.NewGhostPadFromTemplate(pname, pad, class.GetPadTemplate(tmplname))
	if !self.AddPad(gsinkSink.Pad) {
		return nil
	}

	if !gsinkSink.Pad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad for track_sink")
		return nil
	}

	gsinkSink.Pad.AddProbe(s.padWaitReadyProbe())

	if !sink.SyncStateWithParent() {
		self.Log(CAT, gst.LevelError, "Failed to sync sink_track state with parent")
	}

	return gsinkSink.Pad
}

func (s *lkroom) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	if name != "" {
		return self.GetStaticPad(name)
	}

	name = templ.Name()

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("RequestNewPad: %s (%d) => %+v", name, len(name), []byte(name)))

	var kindID int
	if _, err := fmt.Sscanf(name, "sink_%d_%%u", &kindID); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse track config from pad name %q: %v", name, err))
		return nil
	}

	kind := livekit.TrackSource(kindID)
	switch kind {
	case livekit.TrackSource_CAMERA,
		livekit.TrackSource_MICROPHONE:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported track source %d (%s) for pad %s", kind, kind.String(), name))
		return nil
	}

	id := 0
	for {
		if self.GetStaticPad(fmt.Sprintf("sink_%d_%d", kindID, id)) == nil {
			break
		}
		id++
	}

	fmt.Printf("RequestNewPad: kind=%d id=%d\n", kindID, id)

	cfg := TrackCfg{
		Kind: kind,
		ID:   uint(id),
	}

	return s.startTrack(self, cfg)
}

func (s *lkroom) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)
	name := pad.GetName()

	gpad := pad.AsGhostPad()
	if gpad == nil {
		return
	}

	if !self.RemovePad(pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("ReleasePad: Failed to remove pad %s from bin", name))
	}

	target := gpad.GetTarget()
	if target == nil {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("ReleasePad: %s has no target (internal cleanup already done)", name))
		return
	}

	child := target.GetParentElement()
	if child != nil {
		if err := child.SetState(gst.StateNull); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("ReleasePad: Failed to set child element %s to NULL: %v", child.GetName(), err))
		}
		if err := self.Remove(child); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("ReleasePad: Failed to remove child element %s from bin: %v", child.GetName(), err))
		}
	}
}
