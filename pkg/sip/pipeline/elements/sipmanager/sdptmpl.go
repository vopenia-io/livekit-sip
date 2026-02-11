package sipmanager

import (
	"github.com/samber/lo"
	"github.com/vopenia-io/go-pjmedia/pj"
)

const sdpTemplate = `
v=0
o=gateway 999 999 IN IP4 10.0.0.1
s=-
c=IN IP4 1.2.3.4
t=0 0
m=audio 123 RTP/AVP 0 8
a=rtpmap:0 PCMU/8000
a=rtpmap:8 PCMA/8000
m=audio 456 RTP/AVP 0 8 101
a=rtpmap:0 PCMU/8000
a=rtpmap:8 PCMA/8000
a=rtpmap:101 telephone-event/8000
a=fmtp:101 0-15
m=video 789 RTP/AVP 96
a=rtpmap:96 H264/90000
a=fmtp:96 profile-level-id=42e01e;packetization-mode=1
`

func NewSdpTemplate(p *pj.PjPool) *SdpTemplate {
	sdp := lo.Must(p.ParseSDP([]byte(sdpTemplate)))
	return (*SdpTemplate)(sdp)
}

type SdpTemplate pj.PjSdpSession

func (s *SdpTemplate) Audio() *pj.PjSdpMedia {
	return ((*pj.PjSdpSession)(s)).MediaAt(0)
}

func (s *SdpTemplate) AudioDtmf() *pj.PjSdpMedia {
	return ((*pj.PjSdpSession)(s)).MediaAt(1)
}

func (s *SdpTemplate) Video() *pj.PjSdpMedia {
	return ((*pj.PjSdpSession)(s)).MediaAt(2)
}
