package camera_pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

func NewRtpBins(log logger.Logger, parent *CameraPipeline) *RtpBins {
	return &RtpBins{
		log:      log,
		pipeline: parent,
	}
}

type RtpBins struct {
	pipeline *CameraPipeline
	log      logger.Logger

	SipRtpBin    *gst.Element
	WebrtcRtpBin *gst.Element
}

var _ pipeline.GstChain[*CameraPipeline] = (*RtpBins)(nil)

// Create implements [pipeline.GstChain].
func (r *RtpBins) Create() error {
	var err error
	r.SipRtpBin, err = gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name": "sip_rtp_bin",
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtpbin: %w", err)
	}

	r.WebrtcRtpBin, err = gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name": "webrtc_rtp_bin",
	})
	if err != nil {
		return fmt.Errorf("failed to create WebRTC rtpbin: %w", err)
	}

	return nil
}

// Add implements [pipeline.GstChain].
func (r *RtpBins) Add() error {
	if err := r.pipeline.Pipeline().AddMany(
		r.SipRtpBin,
		r.WebrtcRtpBin,
	); err != nil {
		return fmt.Errorf("failed to add rtpbins to pipeline: %w", err)
	}
	return nil
}

// Link implements [pipeline.GstChain].
func (r *RtpBins) Link() error {
	return nil
}

// Close implements [pipeline.GstChain].
func (r *RtpBins) Close() error {
	if err := r.pipeline.Pipeline().RemoveMany(
		r.SipRtpBin,
		r.WebrtcRtpBin,
	); err != nil {
		return fmt.Errorf("failed to remove rtpbins from pipeline: %w", err)
	}
	return nil
}
