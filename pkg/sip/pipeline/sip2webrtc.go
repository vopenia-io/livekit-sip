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
}

var _ GstChain = (*SipToWebrtc)(nil)

// Create implements [GstChain].
func (stw *SipToWebrtc) Create() error {
	var err error

	stw.G711OpusDtmf, err = gst.NewElement("g711-opus-dtmf")
	if err != nil {
		return fmt.Errorf("failed to create g711-opus-dtmf element: %w", err)
	}

	return nil
}

func (stw *SipToWebrtc) Add() error {
	if err := stw.pipeline.Pipeline().AddMany(
		stw.G711OpusDtmf,
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
	); err != nil {
		return fmt.Errorf("failed to remove SIP to WebRTC elements from pipeline: %w", err)
	}
	return nil
}
