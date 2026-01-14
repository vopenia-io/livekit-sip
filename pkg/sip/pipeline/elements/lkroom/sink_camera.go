package lkroom

import (
	"errors"
	"fmt"
	"math"
	"runtime/cgo"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

type sinkCamera struct {
	parent *lkroom

	track *webrtc.TrackLocalStaticRTP
	pt    *lksdk.LocalTrackPublication
}

var sink_camera_properties = []*glib.ParamSpec{
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

func (*sinkCamera) New() glib.GoObjectSubclass {
	return &sinkCamera{}
}

func (*sinkCamera) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"sink_camera",
		"sink/video",
		"Sends video packets to a WebRTC PeerConnection",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)VP8, payload=(int)96")))

	CAT.Log(gst.LevelDebug, "Installing properties")
	class.InstallProperties(sink_camera_properties)
}

func (s *sinkCamera) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseSink(instance)

	self.SetSync(false)
	self.SetAsyncEnabled(false)
	self.SetMaxBitrate(1_500_000)
}

func (s *sinkCamera) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := base.ToGstBaseSink(instance)
	param := sink_camera_properties[id]
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

func (s *sinkCamera) SetCaps(self *base.GstBaseSink, caps *gst.Caps) bool {
	return true
}

func (s *sinkCamera) GetCaps(self *base.GstBaseSink, filter *gst.Caps) *gst.Caps {
	caps := gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)VP8, payload=(int)96")
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get filter: %s", filter.String()))
		if intersect := caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get: %s", caps.String()))
	return caps.Copy().Ref()
}

func (s *sinkCamera) Start(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Starting")
	if s.parent.room == nil {
		self.Log(CAT, gst.LevelError, "Room is not set")
		self.Error("Room is not set", errors.New("room is nil"))
		return false
	}

	return true
}

func (s *sinkCamera) Stop(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")

	s.track = nil
	s.pt.CloseTrack()
	s.pt = nil

	return true
}

func (s *sinkCamera) Render(self *base.GstBaseSink, buffer *gst.Buffer) gst.FlowReturn {
	self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Rendering RTCP buffer of size %d", buffer.GetSize()))

	if s.track == nil {
		self.Log(CAT, gst.LevelError, "Track is not set, dropping RTCP packet")
		self.Error("Track is not set", errors.New("track is nil"))
		return gst.FlowError
	}

	if _, err := s.track.Write(buffer.Bytes()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to write RTCP packet to track: %v", err))
		self.Error("Failed to write RTCP packet to track", err)
		return gst.FlowError
	}

	return gst.FlowOK
}

func (s *sinkCamera) publishTrack(self *base.GstBaseSink) bool {
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeVP8,
	}, "video", "pion")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create new local track: %v", err))
		self.Error("Failed to create new local track", err)
		return false
	}

	p := s.parent.room.LocalParticipant

	if p == nil {
		self.Log(CAT, gst.LevelError, "Local participant is not available in the room")
		self.Error("Local participant is not available in the room", errors.New("local participant is nil"))
		return false
	}

	pt, err := p.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: p.Identity(),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to publish track: %v", err))
		self.Error("Failed to publish track", err)
		return false
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Published camera track with SID %s", pt.SID()))

	s.track = track
	s.pt = pt

	return true
}

func (s *sinkCamera) startAsync(self *base.GstBaseSink, transition gst.StateChange) gst.StateChangeReturn {
	if s.parent.state.IsJoined() {
		if !s.publishTrack(self) {
			self.Log(CAT, gst.LevelError, "Failed to publish track after joining room")
			self.ContinueState(gst.StateChangeFailure)
			return gst.StateChangeFailure
		}
		self.Log(CAT, gst.LevelInfo, "Parent lkroom element already joined room, published track")
		ret := self.ParentChangeState(transition)
		return ret
	}

	go func() {
		if !s.parent.state.WaitJoined() {
			self.Log(CAT, gst.LevelError, "Parent lkroom element failed to join room before starting sink_camera")
			self.ContinueState(gst.StateChangeFailure)
			return
		}
		self.Log(CAT, gst.LevelInfo, "Parent lkroom element joined room, continuing sink_camera state change")
		ret := s.startAsync(self, transition)
		self.ContinueState(ret)
	}()
	return gst.StateChangeAsync
}

func (s *sinkCamera) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
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
