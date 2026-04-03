package sipmanager

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/utils"
	"github.com/vopenia-io/go-pjmedia/pj"
)

type SipMedia struct {
	err      error
	settings *SipSettings

	Type pj.PjMediaType

	rtpConn  *net.UDPConn
	rtcpConn *net.UDPConn

	SrcRtp         *gst.Element
	SrcRtcp        *gst.Element
	SinkRtpFilter  *gst.Element
	SinkRtp        *gst.Element
	SinkRtcpFilter *gst.Element
	SinkRtcp       *gst.Element
}

func (*SipMedia) New() glib.GoObjectSubclass {
	return &SipMedia{}
}

func (*SipMedia) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"sip_media",
		"sink/source",
		"Sends/receives media packets to/from a SIP PeerConnection",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
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
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_rtcp",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewAnyCaps()))

}

func (s *SipMedia) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	class := gst.ToElementClass(self.Class())

	var err error
	s.SrcRtp, err = gst.NewElementWithProperties("udpsrc", map[string]interface{}{
		"name":         "sip-src-rtp",
		"do-timestamp": true,
		"close-socket": false,
		// "format":       int(gst.FormatTime),
		// "is_live":      true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to create udpsrc for RTP: %v", s.err))
		self.Error("failed to create udpsrc for RTP", s.err)
		s.err = fmt.Errorf("failed to create udpsrc for RTP: %w", err)
		return
	}

	s.SrcRtcp, err = gst.NewElementWithProperties("udpsrc", map[string]interface{}{
		"name":         "sip-src-rtcp",
		"do-timestamp": true,
		"close-socket": false,
		// "format":       int(gst.FormatTime),
		// "is_live":      true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to create udpsrc for RTCP: %v", s.err))
		self.Error("failed to create udpsrc for RTCP", s.err)
		s.err = fmt.Errorf("failed to create udpsrc for RTCP: %w", err)
		return
	}

	s.SinkRtpFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"name": "sip-sink-rtp-filter",
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to create capsfilter for RTP: %v", s.err))
		self.Error("failed to create capsfilter for RTP", s.err)
		s.err = fmt.Errorf("failed to create capsfilter for RTP: %w", err)
		return
	}

	s.SinkRtp, err = gst.NewElementWithProperties("udpsink", map[string]interface{}{
		"name":         "sip-sink-rtp",
		"host":         "127.0.0.1",
		"sync":         false,
		"async":        false,
		"close-socket": false,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to create udpsink for RTP: %v", s.err))
		self.Error("failed to create udpsink for RTP", s.err)
		s.err = fmt.Errorf("failed to create udpsink for RTP: %w", err)
		return
	}

	s.SinkRtcpFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"name": "sip-sink-rtcp-filter",
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to create capsfilter for RTCP: %v", s.err))
		self.Error("failed to create capsfilter for RTCP", s.err)
		s.err = fmt.Errorf("failed to create capsfilter for RTCP: %w", err)
		return
	}

	s.SinkRtcp, err = gst.NewElementWithProperties("udpsink", map[string]interface{}{
		"name":         "sip-sink-rtcp",
		"host":         "127.0.0.1",
		"sync":         false,
		"async":        false,
		"close-socket": false,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to create udpsink for RTCP: %v", s.err))
		self.Error("failed to create udpsink for RTCP", s.err)
		s.err = fmt.Errorf("failed to create udpsink for RTCP: %w", err)
		return
	}

	if err := self.AddMany(s.SrcRtp, s.SrcRtcp, s.SinkRtpFilter, s.SinkRtp, s.SinkRtcpFilter, s.SinkRtcp); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to add elements to bin: %v", s.err))
		self.Error("failed to add elements to bin", s.err)
		s.err = fmt.Errorf("failed to add elements to bin: %w", err)
		return
	}

	if err := gst.ElementLinkMany(s.SinkRtpFilter, s.SinkRtp); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to link RTP sink elements: %v", s.err))
		self.Error("failed to link RTP sink elements", s.err)
		s.err = fmt.Errorf("failed to link RTP sink elements: %w", err)
		return
	}

	if err := gst.ElementLinkMany(s.SinkRtcpFilter, s.SinkRtcp); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to link RTCP sink elements: %v", s.err))
		self.Error("failed to link RTCP sink elements", s.err)
		s.err = fmt.Errorf("failed to link RTCP sink elements: %w", err)
		return
	}

	gSrcRtp := gst.NewGhostPadFromTemplate("src", s.SrcRtp.GetStaticPad("src"), class.GetPadTemplate("src"))
	self.AddPad(gSrcRtp.Pad)

	gSrcRtcp := gst.NewGhostPadFromTemplate("src_rtcp", s.SrcRtcp.GetStaticPad("src"), class.GetPadTemplate("src_rtcp"))
	self.AddPad(gSrcRtcp.Pad)

	gSinkRtp := gst.NewGhostPadFromTemplate("sink", s.SinkRtpFilter.GetStaticPad("sink"), class.GetPadTemplate("sink"))
	self.AddPad(gSinkRtp.Pad)

	gSinkRtcp := gst.NewGhostPadFromTemplate("sink_rtcp", s.SinkRtcpFilter.GetStaticPad("sink"), class.GetPadTemplate("sink_rtcp"))
	self.AddPad(gSinkRtcp.Pad)
}

func (s *SipMedia) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	if s.err != nil {
		if transition == gst.StateChangeReadyToNull {
			return self.ParentChangeState(transition)
		}
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Cannot change state due to previous error: %v", s.err))
		self.Error("Cannot change state due to previous error", s.err)
		return gst.StateChangeFailure
	}

	switch transition {
	case gst.StateChangeNullToReady:
		if ret := s.open(self); ret != gst.StateChangeSuccess {
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

func (s *SipMedia) open(self *gst.Bin) gst.StateChangeReturn {
	rtpconn, rtcpconn, err := NewUDPConnPair(s.settings.PortStart, s.settings.PortEnd, s.settings.IP)
	if err != nil {
		var fallbackErr error
		rtpconn, rtcpconn, fallbackErr = NewUDPConnPair(s.settings.PortStart, s.settings.PortEnd, netip.AddrFrom4([4]byte{0, 0, 0, 0}).AsSlice())
		if fallbackErr != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to create UDP connections: %v", err))
			self.Error("failed to create UDP connections", err)
			return gst.StateChangeFailure
		}
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("failed to bind UDP connections to IP %s, falling back to 0.0.0.0", s.settings.IP))
	}
	s.rtpConn = rtpconn
	s.rtcpConn = rtcpconn

	gRtpSock, err := GSocketFromUDPConn(rtpconn)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating GSocket from RTP UDPConn: %v", err))
		self.Error("Error creating GSocket from RTP UDPConn", err)
		return gst.StateChangeFailure
	}

	gRtcpSock, err := GSocketFromUDPConn(rtcpconn)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating GSocket from RTCP UDPConn: %v", err))
		self.Error("Error creating GSocket from RTCP UDPConn", err)
		return gst.StateChangeFailure
	}

	if err := utils.ElementSetPropertyMany(s.SrcRtp, map[string]interface{}{
		"socket": gRtpSock,
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to set RTP src properties: %v", err))
		self.Error("failed to set RTP src properties", err)
		return gst.StateChangeFailure
	}

	if err := utils.ElementSetPropertyMany(s.SrcRtcp, map[string]interface{}{
		"socket": gRtcpSock,
		"caps":   gst.NewCapsFromString("application/x-rtcp"),
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to set RTCP src properties: %v", err))
		self.Error("failed to set RTCP src properties", err)
		return gst.StateChangeFailure
	}

	if err := utils.ElementSetPropertyMany(s.SinkRtcpFilter, map[string]interface{}{
		"caps": gst.NewCapsFromString("application/x-rtcp"),
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to set RTCP sink filter properties: %v", err))
		self.Error("failed to set RTCP sink filter properties", err)
		return gst.StateChangeFailure
	}

	if err := utils.ElementSetPropertyMany(s.SinkRtp, map[string]interface{}{
		"socket": gRtpSock,
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to set RTP sink properties: %v", err))
		self.Error("failed to set RTP sink properties", err)
		return gst.StateChangeFailure
	}

	if err := utils.ElementSetPropertyMany(s.SinkRtcp, map[string]interface{}{
		"socket": gRtcpSock,
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to set RTCP sink properties: %v", err))
		self.Error("failed to set RTCP sink properties", err)
		return gst.StateChangeFailure
	}

	return gst.StateChangeSuccess
}

func (s *SipMedia) close(self *gst.Bin) gst.StateChangeReturn {
	if s.rtpConn != nil {
		if err := s.rtpConn.Close(); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("failed to close RTP UDPConn: %v", err))
			self.Error("failed to close RTP UDPConn", err)
		}
		s.rtpConn = nil
	}

	if s.rtcpConn != nil {
		if err := s.rtcpConn.Close(); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("failed to close RTCP UDPConn: %v", err))
			self.Error("failed to close RTCP UDPConn", err)
		}
		s.rtcpConn = nil
	}

	s.SrcRtp = nil
	s.SrcRtcp = nil
	s.SinkRtpFilter = nil
	s.SinkRtp = nil
	s.SinkRtcpFilter = nil
	s.SinkRtcp = nil

	s.settings = nil

	return gst.StateChangeSuccess
}

func (s *SipMedia) RtpPort() int {
	return s.rtpConn.LocalAddr().(*net.UDPAddr).Port
}

func (s *SipMedia) RtcpPort() int {
	return s.rtcpConn.LocalAddr().(*net.UDPAddr).Port
}

func (s *SipMedia) Configure(stream pj.StreamInfoCommon, ptmap map[uint8]*gst.Caps) (err error) {
	var caps *gst.Caps

	switch v := stream.(type) {
	case *pj.StreamInfo:
		caps, err = s.configureCapsAudio(v, ptmap)
		s.Type = pj.PJMEDIA_TYPE_AUDIO
	case *pj.VidStreamInfo:
		caps, err = s.configureCapsVideo(v, ptmap)
		s.Type = pj.PJMEDIA_TYPE_VIDEO
	default:
		s.Type = pj.PJMEDIA_TYPE_UNKNOWN
		return fmt.Errorf("unsupported stream info type: %T", v)
	}
	if err != nil {
		return fmt.Errorf("failed to configure caps: %w", err)
	}

	fmt.Printf("Configured caps: %s\n", caps.String())

	if err := s.SrcRtp.SetProperty("caps", caps); err != nil {
		return fmt.Errorf("failed to set RTP caps: %w", err)
	}

	if err := s.SinkRtpFilter.SetProperty("caps", caps); err != nil {
		return fmt.Errorf("failed to set RTP sink filter caps: %w", err)
	}

	rtpDest := stream.RemoteAddr().GoAddrPort()
	if err := utils.ElementSetPropertyMany(s.SinkRtp, map[string]interface{}{
		"host": rtpDest.Addr().Unmap().String(),
		"port": int(rtpDest.Port()),
	}); err != nil {
		return fmt.Errorf("failed to set RTP sink properties: %w", err)
	}

	rtcpDest := stream.RemoteRtcpAddr().GoAddrPort()
	if err := utils.ElementSetPropertyMany(s.SinkRtcp, map[string]interface{}{
		"host": rtcpDest.Addr().Unmap().String(),
		"port": int(rtcpDest.Port()),
	}); err != nil {
		return fmt.Errorf("failed to set RTCP sink properties: %w", err)
	}

	return nil
}

func (s *SipMedia) configureCapsCommon(capsStr string, stream pj.StreamInfoCommon) (string, uint8, error) {
	capsStr += fmt.Sprintf(", payload=(int)%d", stream.TxPt())
	return capsStr, stream.TxPt(), nil
}

func (s *SipMedia) configureCapsAudio(stream *pj.StreamInfo, ptmap map[uint8]*gst.Caps) (*gst.Caps, error) {
	capsStr := "application/x-rtp, media=(string)audio"
	capsStr += fmt.Sprintf(", clock-rate=(int)%d", stream.Fmt().ClockRate())
	capsStr += fmt.Sprintf(", encoding-name=(string)%s", stream.Fmt().EncodingName())
	capsStr, pt, err := s.configureCapsCommon(capsStr, stream)
	if err != nil {
		return nil, err
	}

	caps := gst.NewCapsFromString(capsStr)
	ptmap[pt] = caps.Copy() // TODO: do we need to check for conflicts or can we assume pjmedia won't let us reuse payload types across different codecs?
	if stream.RxEventPt() >= 96 {
		telCaps := gst.NewCapsFromString(fmt.Sprintf("application/x-rtp, media=(string)audio, clock-rate=(int)%d, encoding-name=(string)TELEPHONE-EVENT, payload=(int)%d",
			stream.Fmt().ClockRate(), stream.RxEventPt()))
		ptmap[uint8(stream.RxEventPt())] = telCaps
		caps = gst.NewCapsFromString(fmt.Sprintf("application/x-rtp, media=(string)audio, clock-rate=(int)%d", stream.Fmt().ClockRate())) // loose caps because pt will be different
	}
	return caps, nil
}

func (s *SipMedia) configureCapsVideo(stream *pj.VidStreamInfo, ptmap map[uint8]*gst.Caps) (*gst.Caps, error) {
	capsStr := "application/x-rtp, media=(string)video"
	capsStr += fmt.Sprintf(", clock-rate=(int)%d", stream.CodecInfo().ClockRate())
	capsStr += fmt.Sprintf(", encoding-name=(string)%s", stream.CodecInfo().EncodingName())
	capsStr, pt, err := s.configureCapsCommon(capsStr, stream)
	if err != nil {
		return nil, err
	}

	caps := gst.NewCapsFromString(capsStr)
	ptmap[pt] = caps.Copy()
	return caps, nil
}
