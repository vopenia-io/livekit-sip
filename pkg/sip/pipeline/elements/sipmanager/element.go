package sipmanager

import "C"

import (
	"fmt"
	"math"
	"net"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/pion/sdp/v3"
	"github.com/samber/lo"
	"github.com/vopenia-io/go-pjmedia/pj"
)

var (
	ErrSdpParseFailed = fmt.Errorf("failed to parse SDP")
)

var CAT = gst.NewDebugCategory(
	"sipmanager",
	gst.DebugColorFgGreen,
	"sipmanager Element",
)

var properties = []*glib.ParamSpec{
	glib.NewUintParam(
		"port-start",
		"Port Start",
		"Starting port number for the SIP connection",
		1,
		math.MaxUint16,
		1024,
		glib.ParameterWritable,
	),

	glib.NewUintParam(
		"port-end",
		"Port End",
		"Ending port number for the SIP connection",
		2,
		math.MaxUint16,
		math.MaxUint16,
		glib.ParameterWritable,
	),

	glib.NewStringParam(
		"ip",
		"Local IP",
		"Local IP address for the SIP connection",
		&[]string{"0.0.0.0"}[0],
		glib.ParameterReadWrite,
	),
}

type SipSettings struct {
	PortStart uint16
	PortEnd   uint16
	IP        net.IP
}

type SipManager struct {
	settings SipSettings

	pjpool   *pj.PjPool
	neg      *pj.PjSdpNeg
	template *SdpTemplate

	local  *pj.PjSdpSession
	remote *pj.PjSdpSession

	medias []SipMedia
}

func (*SipManager) New() glib.GoObjectSubclass {
	return &SipManager{}
}

func (*SipManager) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"SIP Manager Element",
		"Source/Sink",
		"Wrapper element to handle SIP RTP/RTCP connections and SDP negotiation",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	// Src pad template: ANY caps because we don't know what the reader contains
	class.AddPadTemplate(gst.NewPadTemplate(
		"src_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewCapsFromString("application/x-rtp")))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewCapsFromString("application/x-rtp")))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_rtcp_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewCapsFromString("application/x-rtcp")))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_rtcp_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewCapsFromString("application/x-rtcp")))

	gst.SignalNew(class.Type(),
		"on-remote-offer",
		gst.SignalRunLast,
		glib.TYPE_STRING,
		glib.TYPE_STRING)

	CAT.Log(gst.LevelDebug, "Installing properties")
	class.InstallProperties(properties)
}

func (s *SipManager) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "InstanceInit")

	pool := pj.NewPjPool(pj.UUID31())
	s.pjpool = pool

	s.medias = make([]SipMedia, 0)
	s.template = NewSdpTemplate(s.pjpool)

	sWeak := weak.Make(s)
	self.Connect("on-remote-offer", func(instance *gst.Element, offer string) string {
		self := gst.ToGstBin(instance)
		s := sWeak.Value()
		if s == nil {
			self.Log(CAT, gst.LevelError, "SipManager instance has been garbage collected")
			return ""
		}
		answer, err := s.OnRemoteOffer(self, []byte(offer))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error handling remote offer: %v", err))
			return ""
		}
		return string(answer)
	})
}

func (s *SipManager) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelWarning, fmt.Sprintf("SetProperty id=%d", id))
	param := properties[id]
	switch param.Name() {
	case "port-start":
		gv, _ := value.GoValue()
		val, _ := gv.(uint)
		s.settings.PortStart = uint16(val)
	case "port-end":
		gv, _ := value.GoValue()
		val, _ := gv.(uint)
		s.settings.PortEnd = uint16(val)
	case "ip":
		gv, _ := value.GoValue()
		val, _ := gv.(string)
		ip := net.ParseIP(val)
		if ip == nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid IP address: %s", val))
			return
		}
		self.Log(CAT, gst.LevelDebug, "Setting local IP to "+val)
		s.settings.IP = ip
	}
}

func (s *SipManager) GetProperty(instance *glib.Object, id uint) *glib.Value {
	// self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "ip":
		v, _ := glib.GValue(s.settings.IP.String())
		return v
	}
	return nil
}

const LOCAL_SDP_TEMPLATE = `v=0
o=gateway 999 999 IN IP4 %s
s=-
c=IN IP4 %s
t=0 0
`

func (s *SipManager) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Constructed")

	// self.Log(CAT, gst.LevelDebug, "Generating local SDP with ip "+s.settings.IP.String())

	// localSdp := []byte(fmt.Sprintf(LOCAL_SDP_TEMPLATE, s.settings.IP.String(), s.settings.IP.String()))

}

func (s *SipManager) open(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "open")

	localSdp := lo.Must((&sdp.SessionDescription{
		Version: 0,
		Origin: sdp.Origin{
			Username:       "-",
			SessionID:      12345,
			SessionVersion: 1,
			NetworkType:    "IN",
			AddressType:    "IP4",
			UnicastAddress: s.settings.IP.String(),
		},
		SessionName: "-",
	}).Marshal())

	s.local = lo.Must(s.pjpool.ParseSDP(localSdp))

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Initialized local SDP:\n%s", s.local.String()))

	return gst.StateChangeSuccess
}

func (s *SipManager) close(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "close")

	for _, src := range s.medias {
		if src.rtpConn != nil {
			src.rtpConn.Close()
		}
		if src.rtcpConn != nil {
			src.rtcpConn.Close()
		}
	}

	s.medias = make([]SipMedia, 0)

	return gst.StateChangeSuccess
}

func (s *SipManager) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("ChangeState: %v", transition))
	self.Log(CAT, gst.LevelWarning, fmt.Sprintf("ip=%s, port-start=%d, port-end=%d",
		s.settings.IP.String(),
		s.settings.PortStart,
		s.settings.PortEnd,
	))

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

func (s *SipManager) OpenStream(self *gst.Bin) (SipMedia, error) {
	self.Log(CAT, gst.LevelDebug, "OpenStream")

	rtpconn, rtcpconn, err := NewUDPConnPair(s.settings.PortStart, s.settings.PortEnd, s.settings.IP)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating UDP connection pair: %v", err))
		return SipMedia{}, err
	}

	gRtpSock, err := GSocketFromUDPConn(rtpconn)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating GSocket from RTP UDPConn: %v", err))
		return SipMedia{}, err
	}

	gRtcpSock, err := GSocketFromUDPConn(rtcpconn)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating GSocket from RTCP UDPConn: %v", err))
		return SipMedia{}, err
	}

	rtpSrc, err := gst.NewElementWithProperties("udpsrc", map[string]interface{}{
		"socket": gRtpSock,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating RTP udpsrc element: %v", err))
		return SipMedia{}, err
	}

	rtcpSrc, err := gst.NewElementWithProperties("udpsrc", map[string]interface{}{
		"socket": gRtcpSock,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating RTCP udpsrc element: %v", err))
		return SipMedia{}, err
	}

	return SipMedia{
		rtpConn:  rtpconn,
		rtcpConn: rtcpconn,
		SrcRtp:   rtpSrc,
		SrcRtcp:  rtcpSrc,
	}, nil

}

func (s *SipManager) OnRemoteOffer(self *gst.Bin, offer []byte) (answer []byte, err error) {
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("OnRemoteOffer: %s", string(offer)))

	remote, err := s.pjpool.ParseSDP(offer)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse remote SDP offer: %v", err))
		return nil, ErrSdpParseFailed
	}

	if s.neg == nil {
		neg, err := pj.PjSdpNegCreateWRemoteOffer(s.pjpool, nil, remote)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create SDP negotiator: %v", err))
			return nil, err
		}
		s.neg = neg
	} else {
		if err := s.neg.SetRemoteOffer(s.pjpool, remote); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set remote SDP offer: %v", err))
			return nil, err
		}
	}

	//TODO: add requested streams from remote into local
	for i, m := range remote.Medias() {
		if i < len(s.medias) {
			continue
		}
		switch m.DescMedia() {
		case "audio", "video":
			sipSrc, err := s.OpenStream(self)
			if err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to open stream for media %d: %v", i, err))
				return nil, err
			}
			if err := self.AddMany(sipSrc.SrcRtp, sipSrc.SrcRtcp); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add RTP and RTCP elements to bin: %v", err))
				return nil, err
			}
			for _, e := range [](*gst.Element){sipSrc.SrcRtp, sipSrc.SrcRtcp} {
				if !e.SyncStateWithParent() {
					self.Log(CAT, gst.LevelError, "Failed to sync element state with parent")
					return nil, err
				}
			}
			var tmpl *pj.PjSdpMedia
			if m.DescMedia() == "audio" {
				tmpl = s.template.Audio()
			} else {
				tmpl = s.template.Video()
			}

			tmpl.SetDescPort(uint16(sipSrc.rtpConn.LocalAddr().(*net.UDPAddr).Port))

			if err := s.local.SetMedia(s.pjpool, tmpl, uint(i)); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set local media for media %d: %v", i, err))
				return nil, err
			}
			s.medias = append(s.medias, sipSrc)
		default:
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unsupported media type: %s", m.DescMedia()))
			ghostMedia := pj.NewGhostPjSdpMedia(*s.pjpool, m)
			if err := s.local.SetMedia(s.pjpool, ghostMedia, uint(i)); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set ghost media for media %d: %v", i, err))
				return nil, err
			}
		}
	}

	if err := s.neg.SetLocalAnswer(s.pjpool, s.local); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set local SDP answer: %v", err))
		return nil, err
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Negotiating SDP with local:\n%s\nremote:\n%s",
		s.local.String(),
		remote.String(),
	))

	if err := s.neg.Negotiate(s.pjpool, false); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to negotiate SDP: %v", err))
		return nil, err
	}

	answerSDP, err := s.neg.GetActiveLocal()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get local SDP answer: %v", err))
		return nil, err
	}

	answer = answerSDP.Bytes()

	streams, err := pj.StreamInfoFromSdpAll(s.pjpool, pj.DefaultPjEndpt(), answerSDP, remote)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get stream info from SDP: %v", err))
		return nil, err
	}

	var errs []error
	for i, stream := range streams {
		if i >= len(s.medias) {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No source available for stream %d", i))
			break
		}
		if stream == nil {
			continue
		}
		if err := s.medias[i].Configure(stream); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to configure source for stream %d: %v", i, err))
			errs = append(errs, err)
		}
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Generated local SDP answer: %s", string(answer)))

	return answer, nil
}
