package lkroom

import (
	"fmt"
	"math"
	"runtime/cgo"
	"sync/atomic"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

type sinkRtcp struct {
	room  *lksdk.Room
	ready atomic.Bool
	pc    *webrtc.PeerConnection
	caps  *gst.Caps
}

var sink_rtcp_properties = []*glib.ParamSpec{
	glib.NewUint64Param(
		"room",
		"room",
		"cgo.Handle (uintptr) to a the LiveKit Room",
		0,
		math.MaxUint64,
		0,
		glib.ParameterWritable,
	),
}

func (*sinkRtcp) New() glib.GoObjectSubclass {
	return &sinkRtcp{
		caps: gst.NewCapsFromString("application/x-rtcp"),
	}
}

func (*sinkRtcp) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"sink_rtcp",
		"sink/rtcp",
		"Sends RTCP packets to a WebRTC PeerConnection",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtcp")))

	CAT.Log(gst.LevelDebug, "Installing properties")
	class.InstallProperties(sink_rtcp_properties)
}

func (s *sinkRtcp) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := base.ToGstBaseSink(instance)
	param := sink_rtcp_properties[id]
	switch param.Name() {
	case "room":
		gv, _ := value.GoValue()
		val, _ := gv.(uint64)
		h := cgo.Handle(uintptr(val))
		if h == cgo.Handle(0) {
			self.Log(CAT, gst.LevelError, "Invalid room handle provided")
			return
		}
		roomInterface := h.Value()
		room, ok := roomInterface.(*lksdk.Room)
		if !ok {
			self.Log(CAT, gst.LevelError, "Room handle does not contain a LiveKit Room")
			return
		}
		s.room = room
		self.Log(CAT, gst.LevelInfo, "Room set from handle")
	case "caps":
		val, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting caps property value: %v", err))
			return
		}
		caps, ok := val.(*gst.Caps)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for caps property")
			return
		}
		if caps == nil {
			self.Log(CAT, gst.LevelError, "Nil caps provided")
			return
		}
		s.caps = caps.Copy()
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Element caps set to: %v", caps))
		if self != nil {
			self.GetStaticPad("sink").MarkReconfigure()
		}
	}
}

func (s *sinkRtcp) Constructed(instance *glib.Object) {
	self := base.ToGstBaseSink(instance)

	self.Log(CAT, gst.LevelDebug, "Constructing")

	self.SetSync(false)
	self.SetAsyncEnabled(false)
	self.SetMaxBitrate(1_500_000)
}

func (s *sinkRtcp) SetCaps(self *base.GstBaseSink, caps *gst.Caps) bool {
	return true
}

func (s *sinkRtcp) GetCaps(self *base.GstBaseSink, filter *gst.Caps) *gst.Caps {
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get filter: %s", filter.String()))
		if intersect := s.caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get: %s", s.caps.String()))
	return s.caps.Copy()
}

func (s *sinkRtcp) Start(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Starting")
	if s.room == nil {
		self.Log(CAT, gst.LevelError, "Room is not set")
		// self.Error("Room is not set", nil)
		return false
	}
	return true
}

func (s *sinkRtcp) Stop(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")

	s.ready.Store(false)
	s.pc = nil
	s.room = nil

	// if err := s.pc.Close(); err != nil {
	// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to close PeerConnection: %v", err))
	// 	self.Error("Failed to close PeerConnection", err)
	// 	return false
	// }

	return true
}

func (s *sinkRtcp) Render(self *base.GstBaseSink, buffer *gst.Buffer) gst.FlowReturn {
	self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Rendering RTCP buffer of size %d", buffer.GetSize()))

	if !s.ready.Load() {
		if s.room == nil || s.room.LocalParticipant == nil {
			self.Log(CAT, gst.LevelWarning, "RTCP sink not ready, room or local participant is nil")
			return gst.FlowOK
		}

		pc := s.room.LocalParticipant.GetPublisherPeerConnection()
		if pc == nil {
			self.Log(CAT, gst.LevelWarning, "RTCP sink not ready, publisher peer connection is nil")
			return gst.FlowOK
		}
		pc.ConnectionState()
		s.pc = pc
		s.ready.Store(true)
		self.Log(CAT, gst.LevelInfo, "RTCP sink is now ready")
	}

	if s.pc == nil || s.pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		self.Log(CAT, gst.LevelWarning, "PeerConnection is not set or not connected, dropping RTCP packet")
		return gst.FlowOK
	}

	data := buffer.Bytes()
	if len(data) == 0 {
		self.Log(CAT, gst.LevelWarning, "Received empty RTCP packet, dropping")
		return gst.FlowOK
	}

	pkts, err := rtcp.Unmarshal(data)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to unmarshal RTCP packet: %v", err))
		self.Error("Failed to unmarshal RTCP packet", err)
		return gst.FlowError
	}

	if err := s.pc.WriteRTCP(pkts); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to write RTCP packets to PeerConnection: %v", err))
		self.Error("Failed to write RTCP packets to PeerConnection", err)
		return gst.FlowError
	}

	return gst.FlowOK
}

// func (s *sinkRtcp) Unlock(self *base.GstBaseSink) bool {
// 	self.Log(CAT, gst.LevelInfo, "unlocked")

// 	return true
// }
