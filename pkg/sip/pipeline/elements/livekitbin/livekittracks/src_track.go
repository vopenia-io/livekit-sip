package livekittracks

import (
	"errors"
	"fmt"
	"runtime"
	"time"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

const (
	QDataSrcTrackSource = "livekitbin_srctrack-element-source"
	SrcTrackNamePrefix  = "livekitbin_srctrack_"
)

var srcTrackProperties = []*glib.ParamSpec{
	glib.NewBoolParam(
		"enabled",
		"Enabled",
		"Whether the track is enabled",
		false,
		glib.ParameterReadable|glib.ParameterWritable,
	),
}

func SrcTrackName(sid string) string {
	return SrcTrackNamePrefix + sid
}

func NewSrcTrack(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) (*gst.Element, error) {
	element, err := gst.NewElementWithName("livekitbin_srctrack", SrcTrackName(pub.SID()))
	if err != nil {
		return nil, err
	}
	src, ok := gst.SubclassFromElement[*SrcTrack](element)
	if !ok {
		return nil, fmt.Errorf("failed to cast element to SrcTrack")
	}
	src.Track = track
	src.Pub = pub
	src.Rp = rp

	src.SSRC = uint32(track.SSRC())

	return element, nil
}

type SrcTrack struct {
	Track *webrtc.TrackRemote
	Pub   *lksdk.RemoteTrackPublication
	Rp    *lksdk.RemoteParticipant

	SSRC uint32

	src   *gst.Element
	Queue *gst.Element
}

func (*SrcTrack) New() glib.GoObjectSubclass {
	return &SrcTrack{}
}

func (*SrcTrack) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"livekitbin_srctrack",
		"src",
		"Receives packets from a WebRTC PeerConnection",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	gst.SignalNew(
		class.Type(),
		"send-info",
		gst.SignalRunLast,
		glib.TYPE_NONE,
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_rtcp",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtcp")))

	class.InstallProperties(srcTrackProperties)
}

func (s *SrcTrack) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	class := gst.ToElementClass(self.Class())

	var err error
	s.src, err = NewSrcTrackRtp(s)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create srctrack_rtp: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Failed to create srctrack_rtp", err.Error())
		return
	}

	s.Queue, err = gst.NewElement("queue")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element: %v", err))
		self.Error("Failed to create queue element", err)
		return
	}

	if err := self.AddMany(s.src, s.Queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add srctrack_rtp: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Failed to add srctrack_rtp", err.Error())
		return
	}

	if ret := s.src.GetStaticPad("src").Link(s.Queue.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link srctrack_rtp to queue: %v", ret))
		self.Error("Failed to link srctrack_rtp to queue", errors.New("failed to link srctrack_rtp to queue"))
		return
	}

	gsrcPad := gst.NewGhostPadFromTemplate("src", s.Queue.GetStaticPad("src"), class.GetPadTemplate("src"))
	self.AddPad(gsrcPad.Pad)

	// rtcp
	rtcpPad := gst.NewPadFromTemplate(class.GetPadTemplate("src_rtcp"), "src_rtcp")
	rtcpPad.UseFixedCaps()
	self.AddPad(rtcpPad)

	sweak := weak.Make(s)
	if _, err := self.Connect("send-info", func(self *gst.Element) {
		ptr := sweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "SrcTrack instance is nil in send-info signal callback")
			return
		}
		if err := ptr.SendSourceInfo(); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to send source info: %v", err))
			self.Error("Failed to send source info", err)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to send-info signal: %v", err))
		self.Error("Failed to connect to send-info signal", err)
		return
	}
}

func (s *SrcTrack) open(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "Opening SrcTrack element")

	rtcpPad := self.GetStaticPad("src_rtcp")
	if rtcpPad == nil {
		self.Log(CAT, gst.LevelError, "Failed to get src_rtcp pad from srcTrack element")
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Failed to get src_rtcp pad from srcTrack element", "pad is nil")
		return gst.StateChangeFailure
	}

	if !rtcpPad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate src_rtcp pad for srcTrack element")
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Failed to activate src_rtcp pad for srcTrack element", "failed to activate src_rtcp pad")
		return gst.StateChangeFailure
	}

	streamID := rtcpPad.CreateStreamID(self.Element, "rtcp")
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Created RTCP stream ID: %s", streamID))
	evt := gst.NewStreamStartEvent(streamID)
	evt.SetGroupID(gst.NextGroupID())
	if !rtcpPad.PushEvent(evt) && !rtcpPad.IsLinked() {
		self.Log(CAT, gst.LevelError, "Failed to push StreamStart event on src_rtcp pad")
		self.Error("Failed to push StreamStart event on src_rtcp pad", errors.New("push event failed"))
		return gst.StateChangeFailure
	}

	caps := gst.NewCapsFromString("application/x-rtcp")
	if !rtcpPad.PushEvent(gst.NewCapsEvent(caps)) {
		self.Log(CAT, gst.LevelWarning, "Failed to push caps event on rtcp pad")
		if rtcpPad.IsLinked() {
			self.Log(CAT, gst.LevelWarning, "Failed to push Caps event on RTCP pad")
		}
	}

	segment := gst.NewFormattedSegment(gst.FormatTime)
	if !rtcpPad.PushEvent(gst.NewSegmentEvent(segment)) {
		if rtcpPad.IsLinked() {
			self.Log(CAT, gst.LevelWarning, "Failed to push Segment event on RTCP pad")
		}
	}

	return gst.StateChangeSuccess
}

func (s *SrcTrack) start(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "Starting SrcTrack element")

	rtcpPad := self.GetStaticPad("src_rtcp")
	if rtcpPad == nil {
		self.Log(CAT, gst.LevelError, "Failed to get src_rtcp pad")
		self.Error("Failed to get src_rtcp pad", errors.New("src_rtcp pad is nil"))
		return gst.StateChangeFailure
	}

	s.Pub.OnRTCP(s.onRtcp(self, rtcpPad))

	return gst.StateChangeSuccess
}

func (s *SrcTrack) stop(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "Stopping SrcTrack element")

	s.Pub.OnRTCP(nil)

	done := make(chan struct{})
	go func() {
		s.SendRtcpBye(self)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		self.Log(CAT, gst.LevelWarning, "Timeout waiting for SendRtcpBye to complete")
	}

	self.Log(CAT, gst.LevelInfo, "Stopped SrcTrack element and sent RTCP BYE")

	return gst.StateChangeSuccess
}

func (s *SrcTrack) SendRtcpBye(self *gst.Bin) {
	rtcpPad := self.GetStaticPad("src_rtcp")
	if rtcpPad == nil {
		self.Log(CAT, gst.LevelWarning, "Failed to get src_rtcp pad while sending RTCP BYE in SrcTrack element")
		return
	}
	s.pushRtcp(self, rtcpPad, &rtcp.Goodbye{
		Sources: []uint32{uint32(s.Track.SSRC())},
	})
}

func (s *SrcTrack) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("SrcTrack %s state change: %s", s.Pub.SID(), transition.String()))
	defer self.Log(CAT, gst.LevelDebug, fmt.Sprintf("SrcTrack %s state change completed: %s", s.Pub.SID(), transition.String()))

	switch transition {
	case gst.StateChangeNullToReady:
		if ret := s.open(self); ret != gst.StateChangeSuccess {
			return ret
		}
	case gst.StateChangePausedToPlaying:
		if ret := s.start(self); ret != gst.StateChangeSuccess {
			return ret
		}
	case gst.StateChangePlayingToPaused:
		s.stop(self)
	}

	ret := self.ParentChangeState(transition)
	if ret == gst.StateChangeFailure {
		return ret
	}

	switch transition {
	case gst.StateChangeReadyToNull:
		s.src = nil
	}

	return ret
}

func filterSSRC(pkt rtcp.Packet, ssrc uint32) rtcp.Packet {
	switch p := pkt.(type) {
	case *rtcp.SenderReport:
		if p.SSRC != ssrc {
			return nil
		}
		return pkt
	case *rtcp.ReceiverReport:
		if p.SSRC != ssrc {
			return nil
		}
		return pkt
	case *rtcp.Goodbye:
		res := &rtcp.Goodbye{
			Sources: []uint32{},
			Reason:  p.Reason,
		}
		for _, s := range p.Sources {
			if s == ssrc {
				res.Sources = append(res.Sources, s)
			}
		}
		if len(res.Sources) == 0 {
			return nil
		}
		return res
	case *rtcp.SourceDescription:
		res := &rtcp.SourceDescription{
			Chunks: []rtcp.SourceDescriptionChunk{},
		}
		for _, c := range p.Chunks {
			if c.Source == ssrc {
				res.Chunks = append(res.Chunks, c)
			}
		}
		if len(res.Chunks) == 0 {
			return nil
		}
		return res
	case *rtcp.PictureLossIndication:
		if p.SenderSSRC != ssrc {
			return nil
		}
		return p
	case *rtcp.FullIntraRequest:
		if p.SenderSSRC != ssrc {
			return nil
		}
		return p
	case *rtcp.ExtendedReport:
		if p.SenderSSRC != ssrc {
			return nil
		}
		return p
	}
	return nil
}

func (s *SrcTrack) pushRtcp(self *gst.Bin, rtcpPad *gst.Pad, pkt rtcp.Packet) {
	raw, err := pkt.Marshal()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to marshal RTCP packet: %v", err))
		self.Error("Failed to marshal RTCP packet", err)
		return
	}

	buf := gst.NewBufferFromBytes(raw)
	if ret := rtcpPad.Push(buf); ret != gst.FlowOK {
		if ret == gst.FlowNotLinked {
			self.Log(CAT, gst.LevelDebug, "RTCP pad is not linked, dropping RTCP packet")
			return
		}
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to push RTCP buffer: %v", ret))
	}
	// self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Pushed RTCP packet of size %d: %+v", len(raw), filtered))
}

func (s *SrcTrack) onRtcp(self *gst.Bin, rtcpPad *gst.Pad) func(p rtcp.Packet) {
	return func(p rtcp.Packet) {

		if _, ok := p.(*rtcp.Goodbye); ok {
			return
		}

		filtered := filterSSRC(p, uint32(s.Track.SSRC()))
		if filtered == nil {
			return
		}

		self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Pushing RTCP packet: %T", filtered))

		s.pushRtcp(self, rtcpPad, filtered)
	}
}

func (s *SrcTrack) SendSourceInfo() error {
	structure := NewTrackSourceInfo(s.Rp, s.Pub).Structure()
	if structure == nil {
		return fmt.Errorf("failed to create structure for track source info")
	}
	runtime.SetFinalizer(structure, nil)
	evt := gst.NewCustomEvent(gst.EventTypeCustomDownstreamSticky, structure)
	s.src.GetStaticPad("src").PushEvent(evt)
	return nil
}

func (s *SrcTrack) GetProperty(instance *glib.Object, id uint) *glib.Value {
	self := gst.ToGstBin(instance)
	param := srcTrackProperties[id]
	switch param.Name() {
	case "enabled":
		enabled := s.Pub.IsEnabled()
		val, err := glib.GValue(enabled)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get enabled property value: %v", err))
			return nil
		}
		return val
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property %s", param.Name()))
		return nil
	}
}

func (s *SrcTrack) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := srcTrackProperties[id]
	switch param.Name() {
	case "enabled":
		enabledVal, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get bool value for enabled property: %v", err))
			return
		}
		enabled, ok := enabledVal.(bool)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert enabled property value to bool: %v", enabledVal))
			return
		}
		s.Pub.SetEnabled(enabled)

	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property %s", param.Name()))
	}
}
