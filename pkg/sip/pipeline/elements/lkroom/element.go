package lkroom

import (
	"fmt"
	"math"
	"runtime/cgo"
	"sync/atomic"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

var CAT = gst.NewDebugCategory(
	"lkroom",
	gst.DebugColorFgGreen,
	"lkroom Element",
)

var properties = []*glib.ParamSpec{
	glib.NewUint64Param(
		"callbacks",
		"Callbacks Handle",
		"Handle for the LiveKit room callbacks",
		0,
		math.MaxUint64,
		0,
		glib.ParameterWritable,
	),
	glib.NewUint64Param(
		"room",
		"Room Handle",
		"Handle for the LiveKit room",
		0,
		math.MaxUint64,
		0,
		glib.ParameterReadable,
	),
	glib.NewBoolParam(
		"auto-join",
		"Auto Join",
		"Automatically join the room on state change to PLAYING (default: true, must emit a 'room-joined' signal after joining if false)",
		true,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"token",
		"Token",
		"LiveKit access token",
		nil,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"ws-url",
		"WebSocket URL",
		"LiveKit WebSocket URL",
		nil,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewUint64Param(
		"connect-options",
		"Options Handle",
		"Cgo Handle for the LiveKit room options",
		0,
		math.MaxUint64,
		0,
		glib.ParameterWritable,
	),
}

type config struct {
	AutoJoin bool
	Token    string
	WsURL    string
	OptHnd   []lksdk.ConnectOption
}

type state struct {
	joined atomic.Bool
}

type lkroom struct {
	err error // irrecoverable error during initialization

	self *gst.Bin

	state state
	config

	room      *lksdk.Room
	callbacks *lksdk.RoomCallback

	SinkRTCP *gst.Element
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
		glib.TYPE_BOOLEAN,
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	// class.AddPadTemplate(gst.NewPadTemplate(
	// 	"sink_camera",
	// 	gst.PadDirectionSink,
	// 	gst.PadPresenceRequest,
	// 	gst.NewCapsFromString("application/x-rtp")))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_rtcp",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtcp")))

	// class.AddPadTemplate(gst.NewPadTemplate(
	// 	"src_camera_%u",
	// 	gst.PadDirectionSource,
	// 	gst.PadPresenceSometimes,
	// 	gst.NewCapsFromString("application/x-rtp")))

	// class.AddPadTemplate(gst.NewPadTemplate(
	// 	"src_rtcp",
	// 	gst.PadDirectionSource,
	// 	gst.PadPresenceAlways,
	// 	gst.NewCapsFromString("application/x-rtcp")))

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

	s.SinkRTCP, err = gst.NewElement("lkroom_sinkrtcp")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating sink_rtcp %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error creating sink_rtcp", err.Error())
		return
	}

	if err := self.AddMany(s.SinkRTCP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding elements to bin: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error adding elements to bin", err.Error())
		return
	}

	gsrcRtcp := gst.NewGhostPadFromTemplate("sink_rtcp", s.SinkRTCP.GetStaticPad("sink"), class.GetPadTemplate("sink_rtcp"))
	self.AddPad(gsrcRtcp.Pad)
}

func (s *lkroom) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "callbacks":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting callbacks property value: %v", err))
			return
		}
		val, ok := gv.(uint64)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for callbacks property")
			return
		}
		h := cgo.Handle(uintptr(val))
		if h == 0 {
			self.Log(CAT, gst.LevelError, "Invalid handle provided for callbacks")
			return
		}
		obj := h.Value()
		cb, ok := obj.(*lksdk.RoomCallback)
		if !ok {
			self.Log(CAT, gst.LevelError, "Handle does not contain a RoomCallback")
			return
		}
		s.callbacks.Merge(cb)
	case "auto-join":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting auto-join property value: %v", err))
			return
		}
		s.AutoJoin = gv.(bool)
	case "token":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting token property value: %v", err))
			return
		}
		s.Token = gv.(string)
	case "ws-url":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting ws-url property value: %v", err))
			return
		}
		s.WsURL = gv.(string)
	case "connect-options":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting connect-options property value: %v", err))
			return
		}
		val, ok := gv.(uint64)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for connect-options property")
			return
		}
		h := cgo.Handle(uintptr(val))
		if h == 0 {
			self.Log(CAT, gst.LevelError, "Invalid handle provided for connect-options")
			return
		}
		obj := h.Value()
		opt, ok := obj.([]lksdk.ConnectOption)
		if !ok {
			self.Log(CAT, gst.LevelError, "Handle does not contain a ConnectOption")
			return
		}
		s.OptHnd = opt
	}
}

func (s *lkroom) GetProperty(instance *glib.Object, id uint) *glib.Value {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "room":
		h := cgo.NewHandle(s.room)
		val := uint64(uintptr(h))
		gv, _ := glib.GValue(val)
		self.Log(CAT, gst.LevelDebug, "GetProperty room called")
		return gv
	case "auto-join":
		gv, _ := glib.GValue(s.AutoJoin)
		return gv
	case "token":
		gv, _ := glib.GValue(s.Token)
		return gv
	case "ws-url":
		gv, _ := glib.GValue(s.WsURL)
		return gv
	}
	return nil
}

func (s *lkroom) open(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "open")

	// var err error

	s.room = lksdk.NewRoom(s.toCallbacks())

	roomHnd := cgo.NewHandle(s.room)
	defer roomHnd.Delete()

	if err := s.SinkRTCP.SetProperty("room", uint64(uintptr(roomHnd))); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting handle property on sink_rtcp: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error setting handle property on sink_rtcp", err.Error())
		return gst.StateChangeFailure
	}

	return gst.StateChangeSuccess
}

func (s *lkroom) joinRoom(self *gst.Bin) error {
	self.Log(CAT, gst.LevelDebug, "joinRoom")
	if s.state.joined.Load() {
		self.Log(CAT, gst.LevelInfo, "Already joined room")
		return nil
	}
	err := s.room.JoinWithToken(s.WsURL, s.Token, s.OptHnd...)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to room: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error connecting to room", err.Error())
		return fmt.Errorf("error connecting to room: %w", err)
	}
	s.state.joined.Store(true)
	self.Log(CAT, gst.LevelInfo, "Successfully joined room")
	return nil
}

func (s *lkroom) start(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "start")

	if s.state.joined.Load() {
		self.Log(CAT, gst.LevelInfo, "Already joined room")
		return gst.StateChangeSuccess
	}

	if s.AutoJoin {
		self.Log(CAT, gst.LevelInfo, "Auto-joining room")
		err := s.joinRoom(self)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to room: %v", err))
			self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error connecting to room", err.Error())
			return gst.StateChangeFailure
		}
	} else {
		self.Log(CAT, gst.LevelInfo, "Auto-join disabled, waiting for event")

		var (
			err error
			hnd glib.SignalHandle
		)
		if err := self.SetLockedState(true); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Could not lock element state: %v", err))
			self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Could not lock element state", err.Error())
			return gst.StateChangeFailure
		}
		hnd, err = self.Connect("join-room", func(instance *gst.Element) bool {
			self := gst.ToGstBin(instance)
			if hnd != 0 {
				self.HandlerDisconnect(hnd)
			}
			err := s.joinRoom(self)
			if err := self.SetLockedState(false); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Could not unlock element state: %v", err))
				self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Could not unlock element state", err.Error())
				return false
			}
			if err == nil {
				self.ContinueState(gst.StateChangeSuccess)
				return true
			} else {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Error joining room: %v", err))
				self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error joining room", err.Error())
				self.ContinueState(gst.StateChangeFailure)
				return false
			}
		})
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting join-room signal: %v", err))
			self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error connecting join-room signal", err.Error())
			if err := self.SetLockedState(false); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Could not unlock element state: %v", err))
			}
			return gst.StateChangeFailure
		}
		return gst.StateChangeAsync
	}

	return gst.StateChangeSuccess
}

func (s *lkroom) close(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "close")

	s.room.Disconnect()

	s.self = nil
	s.SinkRTCP = nil

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
	case gst.StateChangeNullToReady:
		// return s.open(self)
		if ret := s.open(self); ret != gst.StateChangeSuccess {
			return ret
		}
	case gst.StateChangeReadyToPaused:
		if ret := s.start(self); ret != gst.StateChangeSuccess {
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

// func (s *sipconn) close(self *gst.Element) {
// 	self.Log(CAT, gst.LevelDebug, "Closing UDP connections")

// 	if err := s.rtpconn.Close(); err != nil {
// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error closing RTP UDP connection: %v", err))
// 		self.Error("Error closing RTP UDP connection", err)
// 	}
// 	if err := s.rtcpconn.Close(); err != nil {
// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error closing RTCP UDP connection: %v", err))
// 		self.Error("Error closing RTCP UDP connection", err)
// 	}
// }
