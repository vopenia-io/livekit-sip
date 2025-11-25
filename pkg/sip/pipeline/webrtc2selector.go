package pipeline

import (
	"fmt"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

type WebrtcToSelector struct {
	ID            uint64
	linked        core.Fuse
	WebrtcRtpSrc  *gst.Element
	WebrtcRtcpSrc *gst.Element
	RtpVp8Depay   *gst.Element
	RtpQueue      *gst.Element
	RtcpQueue     *gst.Element

	WebrtcRtpAppSrc  *app.Source
	WebrtcRtcpAppSrc *app.Source

	WebrtcRtpSelectorPad *gst.Pad
	WebrtcRtcpFunnelPad  *gst.Pad
}

func buildWebRTCToSelectorChain(id uint64) (*WebrtcToSelector, error) {
	webrtcRtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtp_in_%d", id),
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
		"name":         fmt.Sprintf("webrtc_rtcp_in_%d", id),
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
	// 	return nil, fmt.Errorf("failed to create WebRTC RTP jitter buffer: %w", err)
	// }

	depay, err := gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP depayloader: %w", err)
	}

	RtpQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP queue: %w", err)
	}

	RtcpQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTCP queue: %w", err)
	}

	return &WebrtcToSelector{
		ID:               id,
		WebrtcRtpSrc:     webrtcRtpSrc,
		WebrtcRtcpSrc:    webrtcRtcpSrc,
		RtpVp8Depay:      depay,
		RtpQueue:         RtpQueue,
		RtcpQueue:        RtcpQueue,
		WebrtcRtpAppSrc:  app.SrcFromElement(webrtcRtpSrc),
		WebrtcRtcpAppSrc: app.SrcFromElement(webrtcRtcpSrc),
	}, nil
}

func (wts *WebrtcToSelector) link(pipeline *gst.Pipeline, sts *SelectorToSip) error {
	if !wts.linked.Break() {
		return fmt.Errorf("webrtc to selector already linked")
	}

	if err := addlinkChain(pipeline,
		wts.WebrtcRtpSrc,
	); err != nil {
		return fmt.Errorf("failed to link webrtc to selector chain: %w", err)
	}

	if err := linkPad(
		wts.WebrtcRtpSrc.GetStaticPad("src"),
		sts.RecvRtpBin.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d", wts.ID)),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp src to rtpbin: %w", err)
	}

	if err := addlinkChain(pipeline,
		wts.WebrtcRtcpSrc,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp to selector chain: %w", err)
	}

	if err := linkPad(
		wts.WebrtcRtcpSrc.GetStaticPad("src"),
		sts.RecvRtpBin.GetRequestPad(fmt.Sprintf("recv_rtcp_sink_%d", wts.ID)),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp src to rtpbin: %w", err)
	}

	if err := addlinkChain(pipeline,
		wts.RtpVp8Depay,
		wts.RtpQueue,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp depay to queue: %w", err)
	}

	wts.WebrtcRtpSelectorPad = sts.RtpInputSelector.GetRequestPad(fmt.Sprintf("sink_%d", wts.ID))
	if err := linkPad(
		wts.RtpQueue.GetStaticPad("src"),
		wts.WebrtcRtpSelectorPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp queue to selector: %w", err)
	}

	if err := addlinkChain(pipeline,
		wts.RtcpQueue,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp queue: %w", err)
	}

	if err := linkPad(
		sts.RecvRtpBin.GetRequestPad(fmt.Sprintf("send_rtcp_src_%d", wts.ID)),
		wts.RtcpQueue.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link rtpbin rtcp src to webrtc rtcp queue: %w", err)
	}

	wts.WebrtcRtcpFunnelPad = sts.RtcpFunnel.GetRequestPad(fmt.Sprintf("sink_%d", wts.ID))
	if err := linkPad(
		wts.RtcpQueue.GetStaticPad("src"),
		wts.WebrtcRtcpFunnelPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp queue to rtcp funnel: %w", err)
	}

	for _, elem := range []*gst.Element{
		wts.WebrtcRtpSrc,
		wts.RtpVp8Depay,
		wts.RtpQueue,
		wts.WebrtcRtcpSrc,
		wts.RtcpQueue,
	} {
		if ok := elem.SyncStateWithParent(); !ok {
			return fmt.Errorf("failed to sync state for %s", elem.GetName())
		}
	}

	return nil
}

func (wts *WebrtcToSelector) Close(pipeline *gst.Pipeline) error {
	wts.linked.Break()

	if wts.WebrtcRtpSelectorPad != nil {
		wts.WebrtcRtpSelectorPad.GetParentElement().ReleaseRequestPad(wts.WebrtcRtpSelectorPad)
		wts.WebrtcRtpSelectorPad = nil
	}
	if wts.WebrtcRtcpFunnelPad != nil {
		wts.WebrtcRtcpFunnelPad.GetParentElement().ReleaseRequestPad(wts.WebrtcRtcpFunnelPad)
		wts.WebrtcRtcpFunnelPad = nil
	}

	if err := pipeline.RemoveMany(
		wts.WebrtcRtpSrc,
		wts.RtpQueue,
		wts.WebrtcRtcpSrc,
		wts.RtcpQueue,
	); err != nil {
		return fmt.Errorf("failed to remove elements from pipeline: %w", err)
	}

	return nil
}
