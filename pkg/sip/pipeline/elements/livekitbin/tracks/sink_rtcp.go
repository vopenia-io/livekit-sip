package tracks

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

type SinkRtcp struct {
	pc      *webrtc.PeerConnection
	probeID atomic.Uint64
}

func (s *SinkRtcp) Setup(instance *gst.Element, pc *webrtc.PeerConnection) {
	self := base.ToGstBaseSink(instance)

	if id := s.probeID.Swap(0); id != 0 {
		s.pc = pc
		self.GetStaticPad("sink").RemoveProbe(id)
	} else {
		self.Log(CAT, gst.LevelWarning, "RTCP sink can only be set up once, ignoring subsequent setup")
	}
}

func (*SinkRtcp) New() glib.GoObjectSubclass {
	return &SinkRtcp{}
}

func (*SinkRtcp) ClassInit(klass *glib.ObjectClass) {
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
}

func (s *SinkRtcp) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseSink(instance)

	self.SetSync(false)
	self.SetAsyncEnabled(false)
	self.SetMaxBitrate(500_000)

	probeID := self.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, PadProbeDrop)
	if probeID == 0 {
		self.Log(CAT, gst.LevelError, "Failed to add probe to sink pad")
		self.Error("Failed to add probe to sink pad", errors.New("failed to add probe to sink pad"))
		return
	}
	s.probeID.Store(probeID)
}

func (s *SinkRtcp) SetCaps(self *base.GstBaseSink, caps *gst.Caps) bool {
	return true
}

func (s *SinkRtcp) GetCaps(self *base.GstBaseSink, filter *gst.Caps) *gst.Caps {
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

func (s *SinkRtcp) Start(self *base.GstBaseSink) bool {
	return true
}

func (s *SinkRtcp) Stop(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")

	s.pc = nil

	return true
}

func (s *SinkRtcp) Render(self *base.GstBaseSink, buffer *gst.Buffer) gst.FlowReturn {
	self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Rendering RTCP buffer of size %d", buffer.GetSize()))

	if s.pc == nil {
		self.Log(CAT, gst.LevelError, "PeerConnection is not set in sink_rtcp")
		self.Error("PeerConnection is not set", errors.New("peerconnection is nil"))
		return gst.FlowError
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

	// self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Sending %d RTCP packets to PeerConnection: %+v", len(pkts), pkts))

	if err := s.pc.WriteRTCP(pkts); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to write RTCP packets to PeerConnection: %v", err))
		self.Error("Failed to write RTCP packets to PeerConnection", err)
		return gst.FlowError
	}

	return gst.FlowOK
}
