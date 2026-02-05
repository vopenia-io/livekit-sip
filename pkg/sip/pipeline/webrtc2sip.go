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

	OpusG711 *gst.Element
}

var _ GstChain = (*WebrtcToSip)(nil)

// Create implements [GstChain].
func (stw *WebrtcToSip) Create() error {
	var err error

	stw.OpusG711, err = gst.NewElement("opus-g711")
	if err != nil {
		return fmt.Errorf("failed to create opus-g711 element: %w", err)
	}

	return nil
}

func (stw *WebrtcToSip) Add() error {
	if err := stw.pipeline.Pipeline().AddMany(
		stw.OpusG711,
	); err != nil {
		return fmt.Errorf("failed to add WebRTC to SIP elements to pipeline: %w", err)
	}
	return nil
}

func (stw *WebrtcToSip) Link() error {
	return nil
}

func (stw *WebrtcToSip) Close() error {
	if err := stw.pipeline.Pipeline().RemoveMany(
		stw.OpusG711,
	); err != nil {
		return fmt.Errorf("failed to remove WebRTC to SIP elements from pipeline: %w", err)
	}
	return nil
}
