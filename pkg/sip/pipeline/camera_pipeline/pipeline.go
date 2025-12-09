package camera_pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst/app"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type CameraPipeline struct {
	*pipeline.BasePipeline

	SipToWebrtc *SipToWebrtc
	WebrtcToSip *WebrtcToSip
}

func New(log logger.Logger) (*CameraPipeline, error) {
	cp := &CameraPipeline{}

	p, err := pipeline.New(log)
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	cp.BasePipeline = p

	cp.SipToWebrtc, err = pipeline.CastErr[*SipToWebrtc](cp.AddChain(buildSipToWebRTCChain(log.WithComponent("sip_to_webrtc"))))
	if err != nil {
		return nil, err
	}

	cp.WebrtcToSip, err = pipeline.CastErr[*WebrtcToSip](cp.AddChain(buildSelectorToSipChain(log.WithComponent("selector_to_sip"))))
	if err != nil {
		return nil, err
	}

	if err := cp.setupAutoSwitching(); err != nil {
		return nil, err
	}

	return cp, nil
}

func (cp *CameraPipeline) Configure(media *sdpv2.SDPMedia) error {
	if err := cp.SipToWebrtc.Configure(media); err != nil {
		return fmt.Errorf("failed to configure SIP to WebRTC chain: %w", err)
	}
	if err := cp.WebrtcToSip.Configure(media); err != nil {
		return fmt.Errorf("failed to configure WebRTC to SIP chain: %w", err)
	}
	return nil
}

// RtpSink implements sip.SipPipeline.
func (gp *CameraPipeline) RtpSink() *app.Sink {
	return gp.WebrtcToSip.SipRtpAppSink
}

// RtpSrc implements sip.SipPipeline.
func (gp *CameraPipeline) RtpSrc() *app.Source {
	return gp.SipToWebrtc.SipRtpAppSrc
}

// RtcpSink implements sip.SipPipeline.
func (gp *CameraPipeline) RtcpSink() *app.Sink {
	return gp.SipToWebrtc.SipRtcpAppSink
}

// RtcpSrc implements sip.SipPipeline.
func (gp *CameraPipeline) RtcpSrc() *app.Source {
	return gp.SipToWebrtc.SipRtcpAppSrc
}
