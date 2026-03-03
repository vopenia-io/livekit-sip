package tracks

import (
	"errors"
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

type TrackCfg livekit.TrackSource

func (t TrackCfg) CapsString() string {
	switch livekit.TrackSource(t) {
	case livekit.TrackSource_CAMERA:
		return "application/x-rtp, media=(string)video, encoding-name=(string)VP8, payload=(int)96"
	case livekit.TrackSource_MICROPHONE:
		return "application/x-rtp, media=(string)audio, encoding-name=(string)OPUS, payload=(int)111"
	default:
		return "application/x-rtp"
	}
}

func (t TrackCfg) MimeType() string {
	switch livekit.TrackSource(t) {
	case livekit.TrackSource_CAMERA:
		return webrtc.MimeTypeVP8
	case livekit.TrackSource_MICROPHONE:
		return webrtc.MimeTypeOpus
	default:
		return ""
	}
}

func (t TrackCfg) Label() string {
	return livekit.TrackSource(t).String()
}

func NewSinkTrack(participant *lksdk.LocalParticipant, kind livekit.TrackSource) (*gst.Element, *SinkTrack, error) {
	element, err := gst.NewElement("livekitbin_sinktrack")
	if err != nil {
		return nil, nil, err
	}
	sink, ok := gst.SubclassFromElement[*SinkTrack](element)
	if !ok {
		return nil, nil, fmt.Errorf("failed to cast element to SinkTrack")
	}
	sink.Participant = participant
	sink.TrackCfg = TrackCfg(kind)
	return element, sink, nil
}

type SinkTrack struct {
	TrackCfg

	Participant *lksdk.LocalParticipant

	track *webrtc.TrackLocalStaticRTP
	pt    *lksdk.LocalTrackPublication
}

func NewTrackSink(cfg TrackCfg) (*gst.Element, error) {
	element, err := gst.NewElement("lkroom_sinktrack")
	if err != nil {
		return nil, err
	}
	sink, ok := gst.SubclassFromElement[*SinkTrack](element)
	if !ok {
		return nil, fmt.Errorf("failed to cast element to SinkTrack")
	}
	sink.TrackCfg = cfg
	return element, nil
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
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp")))
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
	caps := gst.NewCapsFromString(s.CapsString())
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		if intersect := caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	return caps.Copy().Ref()
}

func (s *SinkTrack) Start(self *base.GstBaseSink) bool {
	if s.Participant == nil {
		self.Log(CAT, gst.LevelError, "Participant is not set, dropping RTCP packet")
		self.Error("Participant is not set", errors.New("participant is nil"))
		return false
	}

	return s.publishTrack(self)
}

func (s *SinkTrack) Stop(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")

	s.track = nil
	s.pt.CloseTrack()
	s.pt = nil
	s.Participant = nil

	return true
}

func (s *SinkTrack) Render(self *base.GstBaseSink, buffer *gst.Buffer) gst.FlowReturn {
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

func (s *SinkTrack) publishTrack(self *base.GstBaseSink) bool {
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: s.MimeType(),
	}, s.Label(), "pion")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create new local track: %v", err))
		self.Error("Failed to create new local track", err)
		return false
	}

	pt, err := s.Participant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: fmt.Sprintf("%s_%s", s.Participant.Identity(), s.Label()),
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
