package pipeline

import (
	"errors"
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

type GstPipelineChain = []*gst.Element

type GstPipeline struct {
	*SipToWebrtc
	*SelectorToSip
}

func (gp *GstPipeline) Closed() bool {
	return gp.SipToWebrtc.Closed() || gp.SelectorToSip.Closed()
}

func (gp *GstPipeline) Close() error {
	err := errors.Join(
		gp.SipToWebrtc.Close(),
		gp.SelectorToSip.Close(),
	)
	if err != nil {
		return fmt.Errorf("failed to close gst pipeline: %w", err)
	}
	return nil
}

func (gp *GstPipeline) SetState(state gst.State) error {
	var errs []error
	if err := gp.SipToWebrtc.SetState(state); err != nil {
		errs = append(errs, fmt.Errorf("failed to set sip to webrtc pipeline state: %w", err))
	}
	if err := gp.SelectorToSip.SetState(state); err != nil {
		errs = append(errs, fmt.Errorf("failed to set selector to sip pipeline state: %w", err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to set gst pipeline state: %w", errors.Join(errs...))
	}
	return nil
}

// func (gp *GstPipeline) SetStateWait(state gst.State) error {
// 	var errs []error
// 	if err := gp.SipToWebrtc.SetState(state); err != nil {
// 		errs = append(errs, err)
// 	}
// 	if err := gp.SelectorToSip.SetState(state); err != nil {
// 		errs = append(errs, err)
// 	}
// 	if len(errs) > 0 {
// 		return fmt.Errorf("failed to set gst pipeline state: %w", errors.Join(errs...))
// 	}
// }

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
	var err error
	gp := &GstPipeline{}

	gp.SipToWebrtc, err = buildSipToWebRTCChain(sipInPayload)
	if err != nil {
		return nil, err
	}

	gp.SelectorToSip, err = buildSelectorToSipChain(sipOutPayload)
	if err != nil {
		return nil, err
	}

	if err := gp.SipToWebrtc.Link(); err != nil {
		return nil, err
	}

	if err := gp.SelectorToSip.Link(); err != nil {
		return nil, err
	}

	return gp, nil
}
