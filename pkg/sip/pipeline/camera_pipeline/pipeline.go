package camera_pipeline

import (
	"fmt"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type GstPipeline struct {
	*pipeline.BasePipeline

	SipToWebrtc *SipToWebrtc
	WebrtcToSip *WebrtcToSip
}

func New(log logger.Logger, sipPt uint8) (*GstPipeline, error) {
	p, err := pipeline.New(log)
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	gp := &GstPipeline{
		BasePipeline: p,
	}

	gp.SipToWebrtc, err = pipeline.CastErr[*SipToWebrtc](gp.AddChain(buildSipToWebRTCChain(log.WithComponent("sip_to_webrtc"), int(sipPt))))
	if err != nil {
		return nil, err
	}

	gp.WebrtcToSip, err = pipeline.CastErr[*WebrtcToSip](gp.AddChain(buildSelectorToSipChain(log.WithComponent("selector_to_sip"), int(sipPt))))
	if err != nil {
		return nil, err
	}

	if err := gp.setupAutoSwitching(); err != nil {
		return nil, err
	}

	return gp, nil
}
