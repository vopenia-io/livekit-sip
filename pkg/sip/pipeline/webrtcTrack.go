package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/protocol/logger"
)

type WebrtcTrack struct {
	log    logger.Logger
	parent *WebrtcToSip

	SSRC uint32

	RtpSrc    *gst.Element
	RtcpSrc   *gst.Element
	RtpQueue  *gst.Element
	RtcpQueue *gst.Element

	RtpAppSrc  *app.Source
	RtcpAppSrc *app.Source
	RtpPad     *gst.Pad
	RtcpPad    *gst.Pad
}

var _ GstChain = (*WebrtcTrack)(nil)

const VP8CAPS = "application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96,rtcp-fb-nack-pli=true"

func buildWebrtcTrack(log logger.Logger, parent *WebrtcToSip, ssrc uint32) (*WebrtcTrack, error) {
	log = log.WithValues("ssrc", ssrc)

	rtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtp_in_%d", ssrc),
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(gst.FormatTime),
		"max-bytes":    uint64(2_000_000),
		"block":        false,
		"caps":         gst.NewCapsFromString(VP8CAPS),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp appsrc: %w", err)
	}

	rtpQueue, err := gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp queue: %w", err)
	}

	rtcpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtcp_in_%d", ssrc),
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(gst.FormatTime),
		"max-bytes":    uint64(500_000),
		"block":        false,
		"caps":         gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp appsrc: %w", err)
	}

	rtcpQueue, err := gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp queue: %w", err)
	}

	return &WebrtcTrack{
		log:    log,
		parent: parent,

		SSRC: ssrc,

		RtpSrc:    rtpSrc,
		RtcpSrc:   rtcpSrc,
		RtpQueue:  rtpQueue,
		RtcpQueue: rtcpQueue,

		RtpAppSrc:  app.SrcFromElement(rtpSrc),
		RtcpAppSrc: app.SrcFromElement(rtcpSrc),
	}, nil
}

// Add implements GstChain.
func (wt *WebrtcTrack) Add(pipeline *gst.Pipeline) error {
	if err := pipeline.AddMany(
		wt.RtpSrc,
		wt.RtcpSrc,
		wt.RtpQueue,
		wt.RtcpQueue,
	); err != nil {
		return fmt.Errorf("failed to add webrtc track elements to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (wt *WebrtcTrack) Link(pipeline *gst.Pipeline) error {
	if err := gst.ElementLinkMany(
		wt.RtpSrc,
		wt.RtpQueue,
	); err != nil {
		return fmt.Errorf("failed to link webrtc track rtp elements: %w", err)
	}

	wt.RtpPad = wt.parent.RtpFunnel.GetRequestPad("sink_%u")
	if err := linkPad(
		wt.RtpQueue.GetStaticPad("src"),
		wt.RtpPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp queue to rtpbin: %w", err)
	}

	if err := gst.ElementLinkMany(
		wt.RtcpSrc,
		wt.RtcpQueue,
	); err != nil {
		return fmt.Errorf("failed to link webrtc track rtcp elements: %w", err)
	}

	wt.RtcpPad = wt.parent.RtcpFunnel.GetRequestPad("sink_%u")
	if err := linkPad(
		wt.RtcpQueue.GetStaticPad("src"),
		wt.RtcpPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp queue to rtcp funnel: %w", err)
	}

	return wt.sync()
}

func (wt *WebrtcTrack) sync() error {
	for _, elem := range []*gst.Element{
		wt.RtpSrc,
		wt.RtcpSrc,
		wt.RtpQueue,
		wt.RtcpQueue,
	} {
		if ok := elem.SyncStateWithParent(); !ok {
			return fmt.Errorf("failed to sync state for %s", elem.GetName())
		}
	}
	return nil
}

// Close implements GstChain.
func (wt *WebrtcTrack) Close(pipeline *gst.Pipeline) error {
	releasePad(wt.RtpPad)
	releasePad(wt.RtcpPad)

	pipeline.RemoveMany(
		wt.RtpSrc,
		wt.RtcpSrc,
		wt.RtpQueue,
		wt.RtcpQueue,
	)
	return nil
}
