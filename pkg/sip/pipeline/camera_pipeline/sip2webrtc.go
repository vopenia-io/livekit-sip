package camera_pipeline

import (
	"fmt"

	"github.com/livekit/sip/pkg/sip/pipeline"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

func NewSipToWebrtcChain(log logger.Logger, parent *CameraPipeline) *SipToWebrtc {
	return &SipToWebrtc{
		log:      log,
		pipeline: parent,
	}
}

type SipToWebrtc struct {
	pipeline *CameraPipeline
	log      logger.Logger

	H264Vp8 *gst.Element
}

var _ pipeline.GstChain = (*SipToWebrtc)(nil)

// Create implements [pipeline.GstChain].
func (stw *SipToWebrtc) Create() error {
	var err error

	stw.H264Vp8, err = gst.NewElement("h264-vp8")
	if err != nil {
		return fmt.Errorf("failed to create H264 to VP8 element: %w", err)
	}

	return nil
}

func (stw *SipToWebrtc) Add() error {
	if err := stw.pipeline.Pipeline().Add(
		stw.H264Vp8,
	); err != nil {
		return fmt.Errorf("failed to add SIP to WebRTC elements to pipeline: %w", err)
	}
	return nil
}

func (stw *SipToWebrtc) Link() error {
	return nil
}

func (stw *SipToWebrtc) Close() error {
	if err := stw.pipeline.Pipeline().Remove(
		stw.H264Vp8,
	); err != nil {
		return fmt.Errorf("failed to remove SIP to WebRTC elements from pipeline: %w", err)
	}
	return nil
}
