package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

type WebRTCToSelector struct {
	Src          *gst.Element
	JitterBuffer *gst.Element
	Depay        *gst.Element
	Queue        *gst.Element

	AppSrc *app.Source
	SelPad *gst.Pad
}

func buildWebRTCToSelectorChain(srcID string) (*WebRTCToSelector, []*gst.Element, error) {
	src, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtp_in_%s", srcID),
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(3),
		"max-bytes":    uint64(2_000_000),
		"block":        false,
		"caps": gst.NewCapsFromString(
			"application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96",
		),
	})
	if err != nil {
		return nil, nil, err
	}

	jb, err := gst.NewElementWithProperties("rtpjitterbuffer", map[string]interface{}{
		"latency":           uint(100),
		"do-lost":           true,
		"do-retransmission": false,
		"drop-on-latency":   false,
	})
	if err != nil {
		return nil, nil, err
	}

	depay, err := gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, nil, err
	}

	queue, err := gst.NewElement("queue")
	if err != nil {
		return nil, nil, err
	}

	chainElems := []*gst.Element{src, jb, depay, queue}

	return &WebRTCToSelector{
		Src:          src,
		JitterBuffer: jb,
		Depay:        depay,
		Queue:        queue,
		AppSrc:       app.SrcFromElement(src),
	}, chainElems, nil
}

func (wts *WebRTCToSelector) link(selector *gst.Element) error {
	queuePad := wts.Queue.GetStaticPad("src")
	if queuePad == nil {
		return fmt.Errorf("failed to get queue src pad")
	}

	selPad := selector.GetRequestPad("sink_%u")
	if selPad == nil {
		return fmt.Errorf("failed to request selector sink pad")
	}

	if r := queuePad.Link(selPad); r != gst.PadLinkOK {
		return fmt.Errorf("failed to link queue to selector: %s", r.String())
	}
	wts.SelPad = selPad

	return nil
}

func (wts *WebRTCToSelector) Close(pipeline *gst.Pipeline) error {
	if wts.SelPad != nil {
		wts.SelPad.GetParentElement().ReleaseRequestPad(wts.SelPad)
		wts.SelPad = nil
	}

	if err := pipeline.RemoveMany(wts.Src, wts.JitterBuffer, wts.Depay, wts.Queue); err != nil {
		return fmt.Errorf("failed to remove elements from pipeline: %w", err)
	}

	return nil
}
