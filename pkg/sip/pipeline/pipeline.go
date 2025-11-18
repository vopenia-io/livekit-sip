package pipeline

import (
	"fmt"
	"sync"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
)

type GstPipeline struct {
	mu     sync.Mutex
	closed core.Fuse

	Pipeline          *gst.Pipeline
	SipToWebRTC       *SipToWebRTC
	SelectorToSip     *SelectorToSip
	WebRTCToSelectors map[string]*WebRTCToSelector
}

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

func NewSipWebRTCPipeline(sipInPayload, sipOutPayload int) (*GstPipeline, error) {
	// gst.Init(nil) must be called once in your process before this.

	pipeline, err := gst.NewPipeline("")
	if err != nil {
		return nil, err
	}

	sipToWebRTC, sipElems, err := buildSipToWebRTCChain(sipInPayload)
	if err != nil {
		return nil, err
	}

	webrtcToSip, webrtcElems, err := buildWebRTCToSipChain(sipOutPayload)
	if err != nil {
		return nil, err
	}

	allElems := append([]*gst.Element{}, sipElems...)
	allElems = append(allElems, webrtcElems...)

	if err := pipeline.AddMany(allElems...); err != nil {
		return nil, err
	}

	if err := gst.ElementLinkMany(sipElems...); err != nil {
		return nil, err
	}
	if err := gst.ElementLinkMany(webrtcElems...); err != nil {
		return nil, err
	}

	return &GstPipeline{
		Pipeline:          pipeline,
		SipToWebRTC:       sipToWebRTC,
		SelectorToSip:     webrtcToSip,
		WebRTCToSelectors: make(map[string]*WebRTCToSelector),
	}, nil
}
