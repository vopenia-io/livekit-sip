package lkroom

import (
	"errors"
	"fmt"

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

	// CAT.Log(gst.LevelDebug, "Installing properties")
	// class.InstallProperties(sink_rtcp_properties)
}

func (s *sinkRtcp) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseSink(instance)

	self.SetSync(false)
	self.SetAsyncEnabled(false)
	self.SetMaxBitrate(500_000)
}

func (s *sinkRtcp) SetCaps(self *base.GstBaseSink, caps *gst.Caps) bool {
	return true
}

func (s *sinkRtcp) GetCaps(self *base.GstBaseSink, filter *gst.Caps) *gst.Caps {
	caps := gst.NewCapsFromString("application/x-rtcp")
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		if intersect := caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
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

	if !s.parent.state.WaitJoined() {
		self.Log(CAT, gst.LevelError, "Parent lkroom element failed to join room before rendering RTCP packet")
		self.Error("Parent lkroom element failed to join room before rendering RTCP packet", errors.New("parent lkroom failed to join room"))
		return gst.FlowError
	}

	if s.pc == nil {
		pc := s.parent.room.LocalParticipant.GetPublisherPeerConnection()
		if pc == nil {
			self.Log(CAT, gst.LevelError, "PeerConnection is not set on room's LocalParticipant, dropping RTCP packet")
			self.Error("PeerConnection is not set on room's LocalParticipant", errors.New("peer connection is nil"))
			return gst.FlowError
		}
		s.pc = pc
	}

	if state := s.pc.ConnectionState(); state != webrtc.PeerConnectionStateConnected {
		self.Log(CAT, gst.LevelTrace, fmt.Sprintf("PeerConnection is not connected (state: %s), dropping RTCP packet", state.String()))
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

// func (s *sinkRtcp) startAsync(self *base.GstBaseSink, transition gst.StateChange) gst.StateChangeReturn {
// 	if s.parent.state.IsJoined() {
// 		s.pc = s.parent.room.LocalParticipant.GetPublisherPeerConnection()
// 		ret := self.ParentChangeState(transition)
// 		return ret
// 	}

// 	go func() {
// 		if !s.parent.state.WaitJoined() {
// 			self.Log(CAT, gst.LevelError, "Parent lkroom element failed to join room before starting sink_rtcp")
// 			self.AbortState()
// 			return
// 		}
// 		self.Log(CAT, gst.LevelInfo, "Parent lkroom element joined room, continuing sink_rtcp state change")
// 		ret := s.startAsync(self, transition)
// 		self.ContinueState(ret)
// 	}()
// 	return gst.StateChangeAsync
// }

func (s *sinkRtcp) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := base.ToGstBaseSink(instance)

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Changing state: %s", transition.String()))

	if s.parent == nil {
		self.Log(CAT, gst.LevelError, "Parent lkroom element is not set in sink_camera")
		self.Error("Parent lkroom element is not set", errors.New("parent lkroom is nil"))
		return gst.StateChangeFailure
	}

	// if transition == gst.StateChangeReadyToPaused {
	// 	return s.startAsync(self, transition)
	// }

	ret := self.ParentChangeState(transition)

	if transition == gst.StateChangeReadyToNull {
		s.parent = nil
	}
	return ret
}
