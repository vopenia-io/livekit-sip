package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

type WebrtcToSelector struct {
	WebrtcRtpSrc  *gst.Element
	WebrtcRtcpSrc *gst.Element
	RtpQueue      *gst.Element
	RtcpQueue     *gst.Element

	WebrtcRtpAppSrc  *app.Source
	WebrtcRtcpAppSrc *app.Source

	WebrtcRtpSelPad *gst.Pad
}

func buildWebRTCToSelectorChain(srcID string) (*WebrtcToSelector, error) {
	webrtcRtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtp_in_%s", srcID),
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(gst.FormatTime),
		"max-bytes":    uint64(2_000_000),
		"block":        false,
		"caps": gst.NewCapsFromString(
			"application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96",
		),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP appsrc: %w", err)
	}

	webrtcRtcpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtcp_in_%s", srcID),
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(gst.FormatTime),
		"max-bytes":    uint64(500_000),
		"block":        false,
		"caps":         gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTCP appsrc: %w", err)
	}

	// jb, err := gst.NewElementWithProperties("rtpjitterbuffer", map[string]interface{}{
	// 	"latency":           uint(200),
	// 	"do-lost":           true,
	// 	"do-retransmission": false,
	// 	"drop-on-latency":   false,
	// })
	// if err != nil {
	// 	return nil, nil, err
	// }

	// depay, err := gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
	// 	"request-keyframe": true,
	// })
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create WebRTC RTP depayloader: %w", err)
	// }

	RtpQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP queue: %w", err)
	}

	RtcpQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTCP queue: %w", err)
	}

	return &WebrtcToSelector{
		WebrtcRtpSrc:  webrtcRtpSrc,
		WebrtcRtcpSrc: webrtcRtcpSrc,
		// JitterBuffer:    jb,
		// Depay:            depay,
		RtpQueue:         RtpQueue,
		RtcpQueue:        RtcpQueue,
		WebrtcRtpAppSrc:  app.SrcFromElement(webrtcRtpSrc),
		WebrtcRtcpAppSrc: app.SrcFromElement(webrtcRtcpSrc),
	}, nil
}

func (wts *WebrtcToSelector) link(pipeline *gst.Pipeline, rtpSelector *gst.Element, rtcpFunnel *gst.Element) error {
	if err := addlinkChain(pipeline,
		wts.WebrtcRtpSrc,
		wts.RtpQueue,
	); err != nil {
		return fmt.Errorf("failed to link webrtc to selector chain: %w", err)
	}

	if err := addlinkChain(pipeline,
		wts.WebrtcRtcpSrc,
		wts.RtcpQueue,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp src: %w", err)
	}

	if err := linkPad(
		wts.RtcpQueue.GetStaticPad("src"),
		rtcpFunnel.GetRequestPad("sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp to funnel: %w", err)
	}

	wts.WebrtcRtpSelPad = rtpSelector.GetRequestPad("sink_%u")
	if err := linkPad(
		wts.RtpQueue.GetStaticPad("src"),
		wts.WebrtcRtpSelPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp to selector: %w", err)
	}

	return nil
}

func (wts *WebrtcToSelector) Close(pipeline *gst.Pipeline) error {
	if wts.WebrtcRtpSelPad != nil {
		wts.WebrtcRtpSelPad.GetParentElement().ReleaseRequestPad(wts.WebrtcRtpSelPad)
		wts.WebrtcRtpSelPad = nil
	}

	if err := pipeline.RemoveMany(wts.WebrtcRtpSrc, wts.RtpQueue); err != nil {
		return fmt.Errorf("failed to remove elements from pipeline: %w", err)
	}

	return nil
}
