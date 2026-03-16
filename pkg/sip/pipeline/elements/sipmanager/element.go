package sipmanager

import "C"

import (
	"fmt"
	"math"
	"net"
	"net/netip"
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

type GstSipMedia struct {
	*SipMedia
	Element *gst.Element
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

	local *pj.PjSdpSession

	medias []*GstSipMedia

	ptMap map[uint8]*gst.Caps
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
		"src_audio_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_audio_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_rtcp_audio_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_rtcp_audio_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_video_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_video_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_rtcp_video_%u",
		gst.PadDirectionSource,
		gst.PadPresenceRequest,
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_rtcp_video_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewAnyCaps()))

	gst.SignalNew(class.Type(),
		"on-remote-offer",
		gst.SignalRunLast,
		glib.TYPE_STRING,
		glib.TYPE_STRING)

	gst.SignalNew(class.Type(),
		"pt-map",
		gst.SignalRunLast,
		gst.TypeCaps,
		glib.TYPE_UINT)

	CAT.Log(gst.LevelDebug, "Installing properties")
	class.InstallProperties(properties)
}

func (s *SipManager) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "InstanceInit")
	s.ptMap = make(map[uint8]*gst.Caps)

	pool := pj.NewPjPool(pj.UUID31())
	s.pjpool = pool

	s.medias = make([]*GstSipMedia, 0)
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

	self.Connect("pt-map", func(instance *gst.Element, pt uint) *gst.Caps {
		self := gst.ToGstBin(instance)
		s := sWeak.Value()
		if s == nil {
			self.Log(CAT, gst.LevelError, "SipManager instance has been garbage collected")
			return nil
		}
		return s.PtMap(self, pt)
	})
}

func (s *SipManager) PtMap(self *gst.Bin, pt uint) *gst.Caps {
	if pt > math.MaxUint8 {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid PT value: %d", pt))
		return nil
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Received PT map request for PT %d", pt))

	caps, ok := s.ptMap[uint8(pt)]
	if ok {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Found caps for PT %d: %s", pt, caps.String()))
		return caps.Copy()
	}
	self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No caps found for PT %d", pt))
	return nil
}

func (s *SipManager) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
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
	param := properties[id]
	switch param.Name() {
	case "ip":
		v, _ := glib.GValue(s.settings.IP.String())
		return v
	}
	return nil
}

func (s *SipManager) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Constructed")
}

func (s *SipManager) open(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "open")

	localSdp := lo.Must((&sdp.SessionDescription{
		Version: 0,
		Origin: sdp.Origin{
			Username:       "sipmanager",
			SessionID:      12345,
			SessionVersion: 1,
			NetworkType:    "IN",
			AddressType:    lo.Ternary(netip.MustParseAddr(s.settings.IP.String()).Is4(), "IP4", "IP6"),
			UnicastAddress: s.settings.IP.String(),
		},
		ConnectionInformation: &sdp.ConnectionInformation{
			NetworkType: "IN",
			AddressType: lo.Ternary(netip.MustParseAddr(s.settings.IP.String()).Is4(), "IP4", "IP6"),
			Address: &sdp.Address{
				Address: s.settings.IP.String(),
			},
		},
		SessionName: "-",
	}).Marshal())

	s.local = lo.Must(s.pjpool.ParseSDP(localSdp))

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Initialized local SDP:\n%s", s.local.String()))

	return gst.StateChangeSuccess
}

func (s *SipManager) close(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "close")

	for i, media := range s.medias {
		if media == nil {
			continue
		}
		if err := s.CloseMedia(self, uint(i)); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to close media %d: %v", i, err))
		}
	}

	s.medias = make([]*GstSipMedia, 0)

	return gst.StateChangeSuccess
}

func (s *SipManager) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("ChangeState: %v", transition))

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

func (s *SipManager) OpenStream(self *gst.Bin, id uint) (*GstSipMedia, error) {
	self.Log(CAT, gst.LevelDebug, "OpenStream")

	sipMediaElem, err := gst.NewElement("sip_media")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create sipmedia element: %v", err))
		return nil, err
	}

	sipMedia, ok := gst.SubclassFromElement[*SipMedia](sipMediaElem)
	if !ok {
		self.Log(CAT, gst.LevelError, "Failed to cast element to SipMedia")
		return nil, fmt.Errorf("failed to cast element to SipMedia")
	}

	sipMedia.settings = &s.settings

	if err := self.Add(sipMediaElem); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add sipmedia element to bin: %v", err))
		return nil, err
	}

	media := &GstSipMedia{
		SipMedia: sipMedia,
		Element:  sipMediaElem,
	}

	return media, nil
}

func (s *SipManager) AddMedia(self *gst.Bin, media *pj.PjSdpMedia, id uint) error {
	switch media.DescMedia() {
	case "audio", "video":
		sipMedia, err := s.OpenStream(self, uint(id))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to open stream for media %d: %v", id, err))
			return err
		}

		var tmpl *pj.PjSdpMedia
		switch media.DescMedia() {
		case "audio":
			if id == 0 {
				tmpl = s.template.AudioDtmf()
			} else {
				tmpl = s.template.Audio()
			}
			sipMedia.Type = pj.PJMEDIA_TYPE_AUDIO
		case "video":
			tmpl = s.template.Video()
			sipMedia.Type = pj.PJMEDIA_TYPE_VIDEO
		default:
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported media type: %s", media.DescMedia()))
			return fmt.Errorf("unsupported media type: %s", media.DescMedia())
		}

		if err := s.mediaGhostPadAddSrc(self, sipMedia, id); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pads for media %d: %v", id, err))
			return err
		}

		if !sipMedia.Element.SyncStateWithParent() {
			self.Log(CAT, gst.LevelError, "Failed to sync sipmedia element state with parent")
			return fmt.Errorf("failed to sync sipmedia element state with parent")
		}

		tmpl.SetDescPort(uint16(sipMedia.RtpPort()))

		if err := s.local.SetMedia(s.pjpool, tmpl, uint(id)); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set local media for media %d: %v", id, err))
			return err
		}

		s.medias = append(s.medias, sipMedia)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unsupported media type: %s", media.DescMedia()))
		ghostMedia := pj.NewGhostPjSdpMedia(*s.pjpool, media)
		if err := s.local.SetMedia(s.pjpool, ghostMedia, uint(id)); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set ghost media for media %d: %v", id, err))
			return err
		}
		s.medias = append(s.medias, nil)
	}
	return nil
}

func (s *SipManager) CloseMedia(self *gst.Bin, id uint) error {
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("CloseMedia %d", id))

	if int(id) >= len(s.medias) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Media %d does not exist", id))
		return nil
	}

	media := s.medias[id]
	if media == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Media %d is nil", id))
		return nil
	}

	if err := media.Element.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set media element %d state to NULL: %v", id, err))
		return err
	}

	if err := self.Remove(media.Element); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove media element %d from bin: %v", id, err))
		return err
	}

	if err := s.mediaGhostPadRemove(self, id); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove ghost pads for media %d: %v", id, err))
		return err
	}

	s.medias[id] = nil

	return nil
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

	for i, m := range remote.Medias() {
		if i < len(s.medias) {
			continue
		}
		if err := s.AddMedia(self, m, uint(i)); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add media %d: %v", i, err))
			return nil, err
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

	if err := s.Reconcile(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to reconcile SDP: %v", err))
		return nil, err
	}

	answerSDP, err := s.neg.GetActiveLocal()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get local SDP answer: %v", err))
		return nil, err
	}

	answer = answerSDP.Bytes()

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Generated local SDP answer: %s", string(answer)))

	return answer, nil
}

func (s *SipManager) OnRemoteAnswer(self *gst.Bin, answer []byte) error {
	if s.neg == nil {
		self.Log(CAT, gst.LevelError, "SDP negotiator is not initialized")
		return fmt.Errorf("sdp negotiator is not initialized")
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("OnRemoteAnswer: %s", string(answer)))

	remote, err := s.pjpool.ParseSDP(answer)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse remote SDP answer: %v", err))
		return ErrSdpParseFailed
	}

	if err := s.neg.SetRemoteAnswer(s.pjpool, remote); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set remote SDP answer: %v", err))
		return err
	}

	if err := s.Reconcile(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to reconcile SDP: %v", err))
		return err
	}

	return nil
}

func (s *SipManager) Reconcile(self *gst.Bin) error {
	if s.neg == nil {
		self.Log(CAT, gst.LevelError, "SDP negotiator is not initialized")
		return fmt.Errorf("sdp negotiator is not initialized")
	}

	if err := s.neg.Negotiate(s.pjpool, false); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to negotiate SDP: %v", err))
		return err
	}

	local, err := s.neg.GetActiveLocal()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get local SDP answer: %v", err))
		return err
	}

	remote, err := s.neg.GetActiveRemote()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get remote SDP answer: %v", err))
		return err
	}

	streams, err := pj.StreamInfoFromSdpAll(s.pjpool, pj.DefaultPjEndpt(), local, remote)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get stream info from SDP: %v", err))
		return err
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Reconciling %d streams", len(streams)))

	var errs []error
	for i, stream := range streams {
		if i >= len(s.medias) {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No source available for stream %d", i))
			break
		}
		if stream == nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No stream info for stream %d", i))
			continue
		}
		media := s.medias[i]
		if media == nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No source configured for stream %d", i))
			continue
		}

		if stream.Dir() == pj.PJMEDIA_DIR_NONE {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Stream %d has direction NONE, closing it", i))
			if err := s.CloseMedia(self, uint(i)); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to close media for stream %d: %v", i, err))
				errs = append(errs, err)
			}
			continue
		}

		if err := media.Configure(stream, s.ptMap); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to configure source for stream %d: %v", i, err))
			errs = append(errs, err)
		} else {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Successfully configured source for stream %d", i))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to reconcile some streams: %v", errs)
	}

	return nil
}
