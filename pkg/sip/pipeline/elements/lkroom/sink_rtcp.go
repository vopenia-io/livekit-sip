package lkroom

import (
	"errors"
	"fmt"
	"math"
	"runtime/cgo"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

type sinkRtcp struct {
	parent *lkroom

	pc *webrtc.PeerConnection
}

var sink_rtcp_properties = []*glib.ParamSpec{
	glib.NewUint64Param(
		"parent",
		"Parent Handle",
		"cgo.Handle (uintptr) to a the lkroom parent element",
		0,
		math.MaxUint64,
		0,
		glib.ParameterWritable,
	),
}

func (*sinkRtcp) New() glib.GoObjectSubclass {
	return &sinkRtcp{}
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

func (s *sinkRtcp) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseSink(instance)

	self.SetSync(false)
	self.SetAsyncEnabled(false)
	self.SetMaxBitrate(500_000)
}

func (s *sinkRtcp) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := base.ToGstBaseSink(instance)
	param := sink_rtcp_properties[id]
	switch param.Name() {
	case "parent":
		gv, _ := value.GoValue()
		val, _ := gv.(uint64)
		h := cgo.Handle(uintptr(val))
		if h == cgo.Handle(0) {
			self.Log(CAT, gst.LevelError, "Invalid parent handle provided")
			return
		}
		roomInterface := h.Value()
		lkroom, ok := roomInterface.(*lkroom)
		if !ok {
			self.Log(CAT, gst.LevelError, "Parent handle does not contain a lkroom parent element")
			return
		}
		s.parent = lkroom
		self.Log(CAT, gst.LevelInfo, "Track set from handle")
	}
}

func (s *sinkRtcp) SetCaps(self *base.GstBaseSink, caps *gst.Caps) bool {
	return true
}

func (s *sinkRtcp) GetCaps(self *base.GstBaseSink, filter *gst.Caps) *gst.Caps {
	caps := gst.NewCapsFromString("application/x-rtcp")
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get filter: %s", filter.String()))
		if intersect := caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get: %s", caps.String()))
	return caps.Copy().Ref()
}

func (s *sinkRtcp) Start(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Starting")
	if s.parent.room == nil {
		self.Log(CAT, gst.LevelError, "Room is not set")
		self.Error("Room is not set", errors.New("room is nil"))
		return false
	}

	return true
}

func (s *sinkRtcp) Stop(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")

	s.pc = nil

	return true
}

func (s *sinkRtcp) Render(self *base.GstBaseSink, buffer *gst.Buffer) gst.FlowReturn {
	self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Rendering RTCP buffer of size %d", buffer.GetSize()))

	if s.pc == nil {
		self.Log(CAT, gst.LevelError, "PeerConnection is not set, dropping RTCP packet")
		self.Error("PeerConnection is not set", errors.New("peer connection is nil"))
		return gst.FlowError
	}

	if state := s.pc.ConnectionState(); state != webrtc.PeerConnectionStateConnected {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("PeerConnection is not connected (state: %s), dropping RTCP packet", state.String()))
		return gst.FlowOK
	}

	pkts, err := rtcp.Unmarshal(buffer.Bytes())
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

func (s *sinkRtcp) startAsync(self *base.GstBaseSink, transition gst.StateChange) gst.StateChangeReturn {
	if s.parent.state.IsJoined() {
		s.pc = s.parent.room.LocalParticipant.GetPublisherPeerConnection()
		ret := self.ParentChangeState(transition)
		return ret
	}

	go func() {
		if !s.parent.state.WaitJoined() {
			self.Log(CAT, gst.LevelError, "Parent lkroom element failed to join room before starting sink_rtcp")
			self.ContinueState(gst.StateChangeFailure)
			return
		}
		self.Log(CAT, gst.LevelInfo, "Parent lkroom element joined room, continuing sink_rtcp state change")
		ret := s.startAsync(self, transition)
		self.ContinueState(ret)
	}()
	return gst.StateChangeAsync
}

func (s *sinkRtcp) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := base.ToGstBaseSink(instance)

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Changing state: %s", transition.String()))

	if s.parent == nil {
		self.Log(CAT, gst.LevelError, "Parent lkroom element is not set in sink_camera")
		self.Error("Parent lkroom element is not set", errors.New("parent lkroom is nil"))
		return gst.StateChangeFailure
	}

	if transition == gst.StateChangeReadyToPaused {
		return s.startAsync(self, transition)
	}

	ret := self.ParentChangeState(transition)

	if transition == gst.StateChangeReadyToNull {
		s.parent = nil
	}
	return ret
}
