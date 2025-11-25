package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

type GstPipelineChain = []*gst.Element

type GstPipeline struct {
	*basePipeline
	*SipToWebrtc
	*SelectorToSip
}

func addlinkChain(pipeline *gst.Pipeline, chain ...*gst.Element) error {
	if err := pipeline.AddMany(chain...); err != nil {
		return fmt.Errorf("failed to add elements to pipeline: %w", err)
	}
	if err := gst.ElementLinkMany(chain...); err != nil {
		return fmt.Errorf("failed to link elements: %w", err)
	}

	return nil
}

func linkPad(src, dst *gst.Pad) error {
	if src == nil {
		return fmt.Errorf("source pad is nil")
	}
	if dst == nil {
		return fmt.Errorf("destination pad is nil")
	}
	if r := src.Link(dst); r != gst.PadLinkOK {
		return fmt.Errorf("failed to link pads: %s", r.String())
	}
	return nil
}

func NewGstPipeline(sipInPayload, sipOutPayload int) (*GstPipeline, error) {
	pipeline, err := newBasePipeline()
	if err != nil {
		return nil, err
	}

	gp := &GstPipeline{
		basePipeline: pipeline,
	}

	gp.SipToWebrtc, err = buildSipToWebRTCChain(sipInPayload)
	if err != nil {
		return nil, err
	}

	gp.SelectorToSip, err = buildSelectorToSipChain(sipOutPayload)
	if err != nil {
		return nil, err
	}

	if err := gp.SipToWebrtc.Link(gp.Pipeline); err != nil {
		return nil, err
	}

	if err := gp.SelectorToSip.Link(gp.Pipeline); err != nil {
		return nil, err
	}

	return gp, nil
}
