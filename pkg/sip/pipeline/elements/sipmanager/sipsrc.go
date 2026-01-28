package sipmanager

import (
	"fmt"
	"net"

	"github.com/go-gst/go-gst/gst"
	"github.com/vopenia-io/go-pjmedia/pj"
)

type SipMedia struct {
	rtpConn  *net.UDPConn
	rtcpConn *net.UDPConn

	SrcRtp  *gst.Element
	SrcRtcp *gst.Element
}

func (s *SipMedia) Configure(stream pj.StreamInfoCommon) error {
	var caps *gst.Caps
	var err error

	switch v := stream.(type) {
	case *pj.StreamInfo:
		caps, err = s.configureCapsAudio(v)
	case *pj.VidStreamInfo:
		caps, err = s.configureCapsVideo(v)
	default:
		return fmt.Errorf("unsupported stream info type: %T", v)
	}

	if err != nil {
		return fmt.Errorf("failed to configure caps: %w", err)
	}

	if err := s.SrcRtp.SetProperty("caps", caps); err != nil {
		return fmt.Errorf("failed to set RTP caps: %w", err)
	}
	return nil
}

func (s *SipMedia) configureCapsCommon(capsStr string, stream pj.StreamInfoCommon) (string, error) {
	capsStr += fmt.Sprintf(", payload=(int)%d", stream.TxPt())
	return capsStr, nil
}

func (s *SipMedia) configureCapsAudio(stream *pj.StreamInfo) (*gst.Caps, error) {
	capsStr := "application/x-rtp, media=(string)audio"
	capsStr += fmt.Sprintf(", clock-rate=(int)%d", stream.Fmt().ClockRate())
	capsStr += fmt.Sprintf(", encoding-name=(string)%s", stream.Fmt().EncodingName())
	capsStr, err := s.configureCapsCommon(capsStr, stream)
	if err != nil {
		return nil, err
	}

	caps := gst.NewCapsFromString(capsStr)
	return caps, nil
}

func (s *SipMedia) configureCapsVideo(stream *pj.VidStreamInfo) (*gst.Caps, error) {
	capsStr := "application/x-rtp, media=(string)video"
	capsStr += fmt.Sprintf(", clock-rate=(int)%d", stream.CodecInfo().ClockRate())
	capsStr += fmt.Sprintf(", encoding-name=(string)%s", stream.CodecInfo().EncodingName())
	capsStr, err := s.configureCapsCommon(capsStr, stream)
	if err != nil {
		return nil, err
	}

	caps := gst.NewCapsFromString(capsStr)
	return caps, nil
}
