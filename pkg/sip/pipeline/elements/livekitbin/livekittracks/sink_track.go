package livekittracks

import (
	"errors"
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	"github.com/pion/webrtc/v4"
)

var sinkTrackProperties = []*glib.ParamSpec{
	glib.NewBoxedParam(
		"track",
		"Track",
		"The webrtc track this element will write to",
		glib.TYPE_ARBITRARY_DATA,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

type SinkTrack struct {
	track *webrtc.TrackLocalStaticRTP
}

func (*SinkTrack) New() glib.GoObjectSubclass {
	return &SinkTrack{}
}

func (*SinkTrack) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"sink_track",
		"sink",
		"Sends packets to a WebRTC PeerConnection",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp")))

	class.InstallProperties(sinkTrackProperties)
}

func (s *SinkTrack) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseSink(instance)

	self.SetSync(false)
	self.SetAsyncEnabled(false)
	self.SetMaxBitrate(1_500_000)
}

func (s *SinkTrack) SetCaps(self *base.GstBaseSink, caps *gst.Caps) bool {
	return true
}

func (s *SinkTrack) GetCaps(self *base.GstBaseSink, filter *gst.Caps) *gst.Caps {
	caps := gst.NewCapsFromString("application/x-rtp")
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		if intersect := caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	return caps.Copy().Ref()
}

func (s *SinkTrack) Start(self *base.GstBaseSink) bool {
	if s.track == nil {
		self.Log(CAT, gst.LevelError, "Track is not set in sink_track")
		self.Error("Track is not set", errors.New("track is nil"))
		return false
	}

	return true
}

func (s *SinkTrack) Stop(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")

	return true
}

func (s *SinkTrack) Render(self *base.GstBaseSink, buffer *gst.Buffer) gst.FlowReturn {
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

func (s *SinkTrack) Finalize(instance *glib.Object) {
	s.track = nil
}

func (s *SinkTrack) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := base.ToGstBaseSink(instance)
	param := sinkTrackProperties[id]
	switch param.Name() {
	case "track":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for track property: %v", err))
			self.Error("Failed to get Go value for track property", err)
			return
		}
		if gv == nil {
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for track property: %T", gv))
			self.Error("Invalid type for track property", fmt.Errorf("expected glib.ArbitraryValue, got %T", gv))
			return
		}
		track, ok := data.Data.(*webrtc.TrackLocalStaticRTP)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for track property: %T", data.Data))
			self.Error("Invalid data type for track property", fmt.Errorf("expected *webrtc.TrackLocalStaticRTP, got %T", data.Data))
			return
		}
		s.track = track
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown property ID %d for SinkTrack", id))
		self.Error(fmt.Sprintf("Unknown property ID %d for SinkTrack", id), nil)
	}
}
