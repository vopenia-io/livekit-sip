package lkroom

import (
	"errors"
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

type SrcTrack struct {
	parent *lkroom

	track *webrtc.TrackRemote
	pub   *lksdk.RemoteTrackPublication
	rp    *lksdk.RemoteParticipant

	src *gst.Element
}

func (*SrcTrack) New() glib.GoObjectSubclass {
	return &SrcTrack{}
}

func (*SrcTrack) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"lkroom_srctrack",
		"src",
		"Receives packets from a WebRTC PeerConnection",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
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
}

func (s *SrcTrack) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	class := gst.ToElementClass(self.Class())

	var err error

	s.src, err = gst.NewElement("lkroom_srctrack_rtp")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating srctrack_rtp: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error creating srctrack_rtp", err.Error())
		return
	}

	if err := self.Add(s.src); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding srctrack_rtp: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error adding srctrack_rtp", err.Error())
		return
	}

	gsrcPad := gst.NewGhostPadFromTemplate("src", s.src.GetStaticPad("src"), class.GetPadTemplate("src"))
	self.AddPad(gsrcPad.Pad)

	// rtcp
	rtcpPad := gst.NewPadFromTemplate(class.GetPadTemplate("src_rtcp"), "src_rtcp")
	rtcpPad.UseFixedCaps()
	gst.ToElement(instance).AddPad(rtcpPad)
}

func (s *SrcTrack) open(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "Opening SrcTrack element")

	if s.parent == nil {
		self.Log(CAT, gst.LevelError, "Parent lkroom element is not set")
		self.Error("Parent lkroom element is not set", fmt.Errorf("parent lkroom is nil"))
		return gst.StateChangeFailure
	}

	if obj, ok := gst.SubclassFromElement[*SrcTrackRtp](s.src); ok {
		obj.parent = s
		obj.track = s.track
		obj.pub = s.pub
		obj.rp = s.rp
	} else {
		self.Log(CAT, gst.LevelError, "Error casting srctrack_rtp")
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error casting srctrack_rtp", "Internal error")
		return gst.StateChangeFailure
	}

	// rtcp
	rtcpPad := self.GetStaticPad("src_rtcp")
	if rtcpPad == nil {
		self.Log(CAT, gst.LevelError, "Failed to get src_rtcp pad")
		self.Error("Failed to get src_rtcp pad", errors.New("src_rtcp pad is nil"))
		return gst.StateChangeFailure
	}

	if !rtcpPad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Error activating src_rtcp pad for srcTrack element")
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error activating src_rtcp pad for srcTrack element", "failed to activate src_rtcp pad")
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

	s.pub.OnRTCP(s.onRtcp(self, rtcpPad))

	return gst.StateChangeSuccess
}

func (s *SrcTrack) stop(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "Stopping SrcTrack element")

	s.pub.OnRTCP(nil)

	return gst.StateChangeSuccess
}

func (s *SrcTrack) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("ChangeState: %v", transition))

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
		if ret := s.stop(self); ret != gst.StateChangeSuccess {
			return ret
		}
	}

	ret := self.ParentChangeState(transition)
	if ret == gst.StateChangeFailure {
		return ret
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

func (s *SrcTrack) onRtcp(self *gst.Bin, rtcpPad *gst.Pad) func(p rtcp.Packet) {
	return func(p rtcp.Packet) {
		filtered := filterSSRC(p, uint32(s.track.SSRC()))
		if filtered == nil {
			return
		}

		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Pushing RTCP packet: %T", filtered))

		raw, err := filtered.Marshal()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to marshal RTCP packet: %v", err))
			self.Error("Failed to marshal RTCP packet", err)
			return
		}

		buf := gst.NewBufferFromBytes(raw)
		if ret := rtcpPad.Push(buf); ret != gst.FlowOK {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to push RTCP buffer: %v", ret))
			self.Error("Failed to push RTCP buffer", errors.New("push buffer failed"))
		}
	}
}
