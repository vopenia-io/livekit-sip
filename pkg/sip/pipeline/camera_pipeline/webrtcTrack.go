package camera_pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type WebrtcTrack struct {
	log    logger.Logger
	parent *WebrtcToSip

	SSRC uint32

	RtpSrc *gst.Element
	// rtpbin
	Vp8Depay *gst.Element
	RtpQueue *gst.Element

	RtcpSrc *gst.Element

	RtpAppSrc  *app.Source
	RtcpAppSrc *app.Source
	RtpPad     *gst.Pad
	RtcpPad    *gst.Pad
	RtpBinPad  *gst.Pad
	RtpSelPad  *gst.Pad

	// Closed flag to prevent double-cleanup
	closed bool
}

var _ pipeline.GstChain = (*WebrtcTrack)(nil)

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

	vp8depay, err := gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc vp8 depayloader: %w", err)
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

	return &WebrtcTrack{
		log:    log,
		parent: parent,

		SSRC: ssrc,

		RtpSrc: rtpSrc,
		// rtpbin
		Vp8Depay: vp8depay,
		RtpQueue: rtpQueue,

		RtcpSrc: rtcpSrc,

		RtpAppSrc:  app.SrcFromElement(rtpSrc),
		RtcpAppSrc: app.SrcFromElement(rtcpSrc),
	}, nil
}

// Add implements GstChain.
func (wt *WebrtcTrack) Add(pipeline *gst.Pipeline) error {
	if err := pipeline.AddMany(
		wt.RtpSrc,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.RtcpSrc,
	); err != nil {
		return fmt.Errorf("failed to add webrtc track elements to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (wt *WebrtcTrack) Link(p *gst.Pipeline) error {
	// if err := gst.ElementLinkMany(
	// 	wt.RtpSrc,
	// 	wt.RtpQueue,
	// ); err != nil {
	// 	return fmt.Errorf("failed to link webrtc track rtp elements: %w", err)
	// }

	wt.RtpPad = wt.parent.RtpFunnel.GetRequestPad("sink_%u")
	if err := pipeline.LinkPad(
		wt.RtpSrc.GetStaticPad("src"),
		wt.RtpPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp queue to rtpbin: %w", err)
	}

	if err := gst.ElementLinkMany(
		wt.Vp8Depay,
		wt.RtpQueue,
	); err != nil {
		return fmt.Errorf("failed to link webrtc track rtp elements: %w", err)
	}

	// if err := gst.ElementLinkMany(
	// 	wt.RtcpSrc,
	// 	wt.RtcpQueue,
	// ); err != nil {
	// 	return fmt.Errorf("failed to link webrtc track rtcp elements: %w", err)
	// }

	wt.RtcpPad = wt.parent.RtcpFunnel.GetRequestPad("sink_%u")
	if err := pipeline.LinkPad(
		wt.RtcpSrc.GetStaticPad("src"),
		wt.RtcpPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp queue to rtcp funnel: %w", err)
	}

	return wt.sync()
}

func (wt *WebrtcTrack) LinkParent(wts *WebrtcToSip, pad *gst.Pad) error {
	if err := pipeline.LinkPad(
		pad,
		wt.Vp8Depay.GetStaticPad("sink"),
	); err != nil {
		wt.log.Errorw("Failed to link rtpbin pad to depayloader", err)
		return err
	}

	wt.RtpSelPad = wts.InputSelector.GetRequestPad("sink_%u")
	if err := pipeline.LinkPad(
		wt.RtpQueue.GetStaticPad("src"),
		wt.RtpSelPad,
	); err != nil {
		wt.RtpSelPad = nil
		wt.log.Errorw("Failed to link rtpbin pad to depayloader", err)
		return err
	}
	wt.RtpBinPad = pad

	return wt.sync()
}

func (wt *WebrtcTrack) sync() error {
	for _, elem := range []*gst.Element{
		wt.RtpSrc,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.RtcpSrc,
	} {
		if ok := elem.SyncStateWithParent(); !ok {
			return fmt.Errorf("failed to sync state for %s", elem.GetName())
		}
	}
	return nil
}

// Close implements GstChain.
func (wt *WebrtcTrack) Close(p *gst.Pipeline) error {
	// 1. Check/set closed flag to prevent double-cleanup
	if wt.closed {
		return nil
	}
	wt.closed = true

	// 2. Unlink and release request pads BEFORE setting elements to NULL
	// RtpPad is a request pad on RtpFunnel
	if wt.RtpPad != nil {
		wt.RtpPad.SetActive(false)
		peer := wt.RtpPad.GetPeer()
		if peer != nil {
			peer.Unlink(wt.RtpPad)
		}
	}
	// RtcpPad is a request pad on RtcpFunnel
	if wt.RtcpPad != nil {
		wt.RtcpPad.SetActive(false)
		peer := wt.RtcpPad.GetPeer()
		if peer != nil {
			peer.Unlink(wt.RtcpPad)
		}
	}
	// RtpSelPad is a request pad on InputSelector
	if wt.RtpSelPad != nil {
		wt.RtpSelPad.SetActive(false)
		peer := wt.RtpSelPad.GetPeer()
		if peer != nil {
			peer.Unlink(wt.RtpSelPad)
		}
	}

	// 3. Release all request pads (while parent elements still valid)
	pipeline.ReleasePad(wt.RtpPad)
	pipeline.ReleasePad(wt.RtcpPad)
	pipeline.ReleasePad(wt.RtpSelPad)
	wt.RtpPad = nil
	wt.RtcpPad = nil
	wt.RtpSelPad = nil
	wt.RtpBinPad = nil // Dynamic pad from rtpbin, not a request pad

	// 4. Define elements to clean up
	elements := []*gst.Element{
		wt.RtpSrc,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.RtcpSrc,
	}

	// 5. Set elements to NULL state before removal
	for _, elem := range elements {
		if elem != nil {
			elem.SetState(gst.StateNull)
		}
	}

	// 6. Remove elements from pipeline
	if p != nil {
		if err := p.RemoveMany(elements...); err != nil {
			wt.log.Warnw("failed to remove webrtc track elements from pipeline", err, "ssrc", wt.SSRC)
		}
	}

	// 7. Unref each element
	pipeline.UnrefElements(elements...)

	// 8. Nil out struct fields
	wt.RtpSrc = nil
	wt.Vp8Depay = nil
	wt.RtpQueue = nil
	wt.RtcpSrc = nil
	wt.RtpAppSrc = nil
	wt.RtcpAppSrc = nil

	wt.log.Infow("Closed webrtc track", "ssrc", wt.SSRC)
	return nil
}
