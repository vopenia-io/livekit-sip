package camera_pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

func NewWebrtcTrack(log logger.Logger, parent *WebrtcIo, ssrc uint32) *WebrtcTrack {
	return &WebrtcTrack{
		log:    log.WithComponent("webrtc_track").WithValues("ssrc", ssrc),
		parent: parent,
		SSRC:   ssrc,
	}
}

type WebrtcTrack struct {
	log    logger.Logger
	parent *WebrtcIo

	SSRC uint32

	WebrtcRtpIn *gst.Element
	// RtpCapsFilter *gst.Element
	// RtpCapsSetter *gst.Element
	// rtpbin
	Vp8Depay *gst.Element
	RtpQueue *gst.Element

	WebrtcRtcpIn *gst.Element
	// RtcpCapsFilter *gst.Element
	// RtcpCapsSetter *gst.Element

	RtpPad    *gst.Pad
	RtcpPad   *gst.Pad
	SelPad    *gst.Pad
	RtpBinPad *gst.Pad
}

var _ pipeline.GstChain = (*WebrtcTrack)(nil)

// Create implements GstChain.
func (wt *WebrtcTrack) Create() error {
	var err error

	wt.WebrtcRtpIn, err = gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtp_in_%d", wt.SSRC),
		"caps":         gst.NewCapsFromString(VP8CAPS + ",rtcp-fb-nack-pli=(boolean)true,rtcp-fb-nack=(boolean)true,rtcp-fb-ccm-fir=(boolean)true"),
		"do-timestamp": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc rtp sourcereader: %w", err)
	}

	// rtpCaps := VP8CAPS + fmt.Sprintf(",ssrc=(uint)%d", wt.SSRC) + ",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"
	// fmt.Printf("WebRTC RTP Caps: %s\n", rtpCaps)

	// wt.RtpCapsFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
	// 	"caps": gst.NewCapsFromString(rtpCaps),
	// })
	// if err != nil {
	// 	return fmt.Errorf("failed to create webrtc rtp caps filter: %w", err)
	// }

	// wt.RtpCapsSetter, err = gst.NewElementWithProperties("capssetter", map[string]interface{}{
	// 	"caps":    gst.NewCapsFromString(rtpCaps),
	// 	"join":    false,
	// 	"replace": true,
	// })
	// if err != nil {
	// 	return fmt.Errorf("failed to create webrtc rtp caps filter: %w", err)
	// }

	wt.Vp8Depay, err = gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc vp8 depayloader: %w", err)
	}

	wt.RtpQueue, err = gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("failed to create webrtc rtp queue: %w", err)
	}

	wt.WebrtcRtcpIn, err = gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtcp_in_%d", wt.SSRC),
		"caps":         gst.NewCapsFromString("application/x-rtcp"),
		"do-timestamp": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc rtcp appsrc: %w", err)
	}

	rtcpCaps := "application/x-rtcp" + fmt.Sprintf(",ssrc=(uint)%d", wt.SSRC)
	fmt.Printf("WebRTC RTCP Caps: %s\n", rtcpCaps)

	// wt.RtcpCapsFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
	// 	"caps": gst.NewCapsFromString(rtcpCaps),
	// })
	// if err != nil {
	// 	return fmt.Errorf("failed to create webrtc rtcp caps filter: %w", err)
	// }

	// wt.RtcpCapsSetter, err = gst.NewElementWithProperties("capssetter", map[string]interface{}{
	// 	"caps":    gst.NewCapsFromString(rtcpCaps),
	// 	"join":    false,
	// 	"replace": true,
	// })
	// if err != nil {
	// 	return fmt.Errorf("failed to create webrtc rtcp caps filter: %w", err)
	// }

	return nil
}

// Add implements GstChain.
func (wt *WebrtcTrack) Add() error {
	if err := wt.parent.pipeline.Pipeline().AddMany(
		wt.WebrtcRtpIn,
		// wt.RtpCapsFilter,
		// wt.RtpCapsSetter,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.WebrtcRtcpIn,
		// wt.RtcpCapsFilter,
		// wt.RtcpCapsSetter,
	); err != nil {
		return fmt.Errorf("failed to add webrtc track elements to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (wt *WebrtcTrack) Link() error {
	// if err := gst.ElementLinkMany(
	// 	wt.WebrtcRtpIn,
	// 	wt.RtpCapsFilter,
	// 	wt.RtpCapsSetter,
	// ); err != nil {
	// 	return fmt.Errorf("failed to link webrtc rtp in elements: %w", err)
	// }

	wt.RtpPad = wt.WebrtcRtpIn.GetStaticPad("src")
	if err := pipeline.LinkPad(
		wt.RtpPad,
		wt.parent.RtpFunnel.GetRequestPad("sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp queue to rtpbin: %w", err)
	}

	// if err := gst.ElementLinkMany(
	// 	wt.WebrtcRtcpIn,
	// 	wt.RtcpCapsFilter,
	// 	wt.RtcpCapsSetter,
	// ); err != nil {
	// 	return fmt.Errorf("failed to link webrtc rtcp in elements: %w", err)
	// }

	wt.RtcpPad = wt.WebrtcRtcpIn.GetStaticPad("src")
	wt.RtcpPad.AddProbe(gst.PadProbeTypeBuffer, NewRtcpSsrcFilter(wt.SSRC))
	if err := pipeline.LinkPad(
		wt.RtcpPad,
		wt.parent.RtcpFunnel.GetRequestPad("sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp queue to rtcp funnel: %w", err)
	}

	return pipeline.SyncElements(
		wt.WebrtcRtpIn,
		wt.WebrtcRtcpIn,
	)
}

func (wt *WebrtcTrack) LinkParent(rtpbinPad *gst.Pad) error {
	wt.RtpBinPad = rtpbinPad
	if err := pipeline.LinkPad(
		wt.RtpBinPad,
		wt.Vp8Depay.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtpbin pad to depayloader: %w", err)
	}

	if err := gst.ElementLinkMany(
		wt.Vp8Depay,
		wt.RtpQueue,
	); err != nil {
		return fmt.Errorf("failed to link webrtc track rtp elements: %w", err)
	}

	wt.SelPad = wt.parent.InputSelector.GetRequestPad("sink_%u")
	if err := pipeline.LinkPad(
		wt.RtpQueue.GetStaticPad("src"),
		wt.SelPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp queue to input selector: %w", err)
	}

	if err := pipeline.SyncElements(
		wt.Vp8Depay,
		wt.RtpQueue,
	); err != nil {
		return fmt.Errorf("failed to sync webrtc track elements: %w", err)
	}

	if err := wt.parent.pipeline.DirtySwitchWebrtcInput(wt.SSRC); err != nil {
		return fmt.Errorf("failed to switch webrtc input to ssrc %d: %w", wt.SSRC, err)
	}
	return nil
}

// Close implements GstChain.
func (wt *WebrtcTrack) Close() error {
	wt.parent.InputSelector.ReleaseRequestPad(wt.SelPad)
	wt.SelPad = nil
	wt.parent.RtpFunnel.ReleaseRequestPad(wt.RtpPad)
	wt.RtpPad = nil
	wt.parent.RtcpFunnel.ReleaseRequestPad(wt.RtcpPad)
	wt.RtcpPad = nil

	for _, elem := range []*gst.Element{
		wt.WebrtcRtpIn,
		// wt.RtpCapsFilter,
		// wt.RtpCapsSetter,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.WebrtcRtcpIn,
		// wt.RtcpCapsFilter,
		// wt.RtcpCapsSetter,
	} {
		if err := elem.SetState(gst.StateNull); err != nil {
			wt.log.Errorw("Failed to set webrtc track element to null state", err, "element", elem.GetName())
		}
	}

	wt.parent.pipeline.Pipeline().RemoveMany(
		wt.WebrtcRtpIn,
		// wt.RtpCapsFilter,
		// wt.RtpCapsSetter,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.WebrtcRtcpIn,
		// wt.RtcpCapsFilter,
		// wt.RtcpCapsSetter,
	)

	wt.log.Infow("Closed webrtc track", "ssrc", wt.SSRC)
	delete(wt.parent.Tracks, wt.SSRC)
	wt.log.Infow("Removed webrtc track from parent", "ssrc", wt.SSRC)
	return nil
}
