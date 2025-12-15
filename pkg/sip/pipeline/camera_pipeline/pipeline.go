package camera_pipeline

import (
	"fmt"
	"net"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type CameraPipeline struct {
	*pipeline.BasePipeline

	SipToWebrtc *SipToWebrtc
	WebrtcToSip *WebrtcToSip
}

func New(log logger.Logger, rtpConn, rtcpConn net.Conn) (*CameraPipeline, error) {
	cp := &CameraPipeline{}

	p, err := pipeline.New(log)
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	cp.BasePipeline = p

	cp.SipToWebrtc, err = pipeline.CastErr[*SipToWebrtc](cp.AddChain(buildSipToWebRTCChain(log.WithComponent("sip_to_webrtc"), rtpConn, rtcpConn)))
	if err != nil {
		return nil, err
	}

	cp.WebrtcToSip, err = pipeline.CastErr[*WebrtcToSip](cp.AddChain(buildSelectorToSipChain(log.WithComponent("selector_to_sip"), rtpConn, rtcpConn)))
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

func (cp *CameraPipeline) Close() error {
	if err := func() error {
		cp.Mu.Lock()
		defer cp.Mu.Unlock()

		if cp.Closed() {
			return nil
		}

		if err := cp.SipToWebrtc.Close(cp.Pipeline); err != nil {
			return fmt.Errorf("failed to close SIP to WebRTC chain: %w", err)
		}
		if err := cp.WebrtcToSip.Close(cp.Pipeline); err != nil {
			return fmt.Errorf("failed to close WebRTC to SIP chain: %w", err)
		}

		return nil
	}(); err != nil {
		return err
	}
	return cp.BasePipeline.Close()
}
