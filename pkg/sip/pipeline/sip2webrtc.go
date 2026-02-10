package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

func NewSipToWebrtcChain(log logger.Logger, parent *Pipeline) *SipToWebrtc {
	return &SipToWebrtc{
		log:      log,
		pipeline: parent,
	}
}

type SipToWebrtc struct {
	pipeline *Pipeline
	log      logger.Logger

	G711OpusDtmf *gst.Element
	H264Vp8      *gst.Element
}

var _ GstChain = (*SipToWebrtc)(nil)

// Create implements [GstChain].
func (stw *SipToWebrtc) Create() error {
	var err error

	stw.G711OpusDtmf, err = gst.NewElement("g711-opus-dtmf")
	if err != nil {
		return fmt.Errorf("failed to create g711-opus-dtmf element: %w", err)
	}

	stw.H264Vp8, err = gst.NewElement("h264-vp8")
	if err != nil {
		return fmt.Errorf("failed to create h264-vp8 element: %w", err)
	}

	return nil
}

func (stw *SipToWebrtc) Add() error {
	if err := stw.pipeline.Pipeline().AddMany(
		stw.G711OpusDtmf,
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
	if err := stw.pipeline.Pipeline().RemoveMany(
		stw.G711OpusDtmf,
		stw.H264Vp8,
	); err != nil {
		return fmt.Errorf("failed to remove SIP to WebRTC elements from pipeline: %w", err)
	}
	return nil
}
