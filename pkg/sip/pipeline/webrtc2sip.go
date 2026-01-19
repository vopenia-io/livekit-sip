package pipeline

import (
	"fmt"


	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

func NewWebrtcToSipChain(log logger.Logger, parent *Pipeline) *WebrtcToSip {
	return &WebrtcToSip{
		log:      log,
		pipeline: parent,
	}
}

type WebrtcToSip struct {
	pipeline *Pipeline
	log      logger.Logger

	Vp8H264 *gst.Element
}

var _ GstChain = (*WebrtcToSip)(nil)

// Create implements [GstChain].
func (stw *WebrtcToSip) Create() error {
	var err error

	stw.Vp8H264, err = gst.NewElement("vp8-h264")
	if err != nil {
		return fmt.Errorf("failed to create VP8 to H264 element: %w", err)
	}

	return nil
}

func (stw *WebrtcToSip) Add() error {
	if err := stw.pipeline.Pipeline().Add(
		stw.Vp8H264,
	); err != nil {
		return fmt.Errorf("failed to add WebRTC to SIP elements to pipeline: %w", err)
	}
	return nil
}

func (stw *WebrtcToSip) Link() error {
	return nil
}

func (stw *WebrtcToSip) Close() error {
	if err := stw.pipeline.Pipeline().Remove(
		stw.Vp8H264,
	); err != nil {
		return fmt.Errorf("failed to remove WebRTC to SIP elements from pipeline: %w", err)
	}
	return nil
}
