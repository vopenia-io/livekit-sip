package pipeline

import (
	"fmt"
	"sync"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
)

type GstPipeline struct {
	mu       sync.Mutex
	closed   core.Fuse
	Pipeline *gst.Pipeline

	SipToWebRTC       *SipToWebrtc
	SelectorToSip     *SelectorToSip
	WebRTCToSelectors map[string]*WebrtcToSelector
}

type GstPipelineChain = []*gst.Element

func (gp *GstPipeline) Close() error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	if gp.closed.IsBroken() {
		return nil
	}
	gp.closed.Break()

	return gp.Pipeline.SetState(gst.StateNull)
}

func (gp *GstPipeline) SetState(state gst.State) error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	return gp.Pipeline.SetState(state)
}

func (gp *GstPipeline) AddWebRTCSourceToSelector(srcID string) (*WebrtcToSelector, error) {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	if gp.closed.IsBroken() {
		return nil, fmt.Errorf("pipeline is closed")
	}

	if _, exists := gp.WebRTCToSelectors[srcID]; exists {
		return nil, fmt.Errorf("webrtc source with id %s already exists", srcID)
	}

	webRTCToSelector, elems, err := buildWebRTCToSelectorChain(srcID)
	if err != nil {
		return nil, err
	}

	if err := gp.Pipeline.AddMany(elems...); err != nil {
		return nil, err
	}

	if err := gst.ElementLinkMany(elems...); err != nil {
		return nil, err
	}

	if err := webRTCToSelector.link(gp.SelectorToSip.InputSelector); err != nil {
		return nil, err
	}

	gp.WebRTCToSelectors[srcID] = webRTCToSelector

	return webRTCToSelector, nil
}

func (gp *GstPipeline) SwitchWebRTCSelectorSource(srcID string) error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	if gp.closed.IsBroken() {
		return fmt.Errorf("pipeline is closed")
	}

	webRTCToSelector, exists := gp.WebRTCToSelectors[srcID]
	if !exists {
		return fmt.Errorf("webrtc source with id %s does not exist", srcID)
	}

	sel := gp.SelectorToSip.InputSelector

	selPad := webRTCToSelector.SelPad

	if err := sel.SetProperty("active-pad", selPad); err != nil {
		return fmt.Errorf("failed to set active pad on selector: %w", err)
	}

	return nil
}

func (gp *GstPipeline) RemoveWebRTCSourceFromSelector(srcID string) error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	if gp.closed.IsBroken() {
		return nil
	}

	webRTCToSelector, exists := gp.WebRTCToSelectors[srcID]
	if !exists {
		return fmt.Errorf("webrtc source with id %s does not exist", srcID)
	}

	if err := webRTCToSelector.Close(gp.Pipeline); err != nil {
		return fmt.Errorf("failed to close webrtc to selector chain: %w", err)
	}

	delete(gp.WebRTCToSelectors, srcID)

	return nil
}

func (gp *GstPipeline) addlinkChain(chain ...*gst.Element) error {
	if err := gp.Pipeline.AddMany(chain...); err != nil {
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

func NewSipWebRTCPipeline(sipInPayload, sipOutPayload int) (*GstPipeline, error) {
	var err error
	gp := &GstPipeline{
		WebRTCToSelectors: make(map[string]*WebrtcToSelector),
	}

	gp.Pipeline, err = gst.NewPipeline("")
	if err != nil {
		return nil, err
	}

	gp.SipToWebRTC, err = buildSipToWebRTCChain(sipInPayload)
	if err != nil {
		return nil, err
	}
	if err := gp.SipToWebRTC.Link(gp); err != nil {
		return nil, err
	}

	gp.SelectorToSip, err = buildSelectorToSipChain(sipOutPayload)
	if err != nil {
		return nil, err
	}
	if err := gp.SelectorToSip.Link(gp); err != nil {
		return nil, err
	}

	return gp, nil
}
