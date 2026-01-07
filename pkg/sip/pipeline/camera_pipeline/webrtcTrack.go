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

	SSRC   uint32
	SelPad *gst.Pad
	BinPad *gst.Pad

	WebrtcRtpIn *gst.Element
	// rtpbin

	WebrtcRtcpIn *gst.Element
}

var _ pipeline.GstChain = (*WebrtcTrack)(nil)

// Create implements GstChain.
func (wt *WebrtcTrack) Create() error {
	var err error

	wt.WebrtcRtpIn, err = gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtp_in_%d", wt.SSRC),
		"caps":         gst.NewCapsFromString(VP8CAPS),
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
		wt.WebrtcRtcpIn,
	); err != nil {
		return fmt.Errorf("failed to add webrtc track elements to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (wt *WebrtcTrack) Link() error {

	if err := pipeline.LinkPad(
		wt.WebrtcRtpIn.GetStaticPad("src"),
		wt.parent.RtpFunnel.GetRequestPad("sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp queue to rtpbin: %w", err)
	}

	rtcpPad := wt.WebrtcRtcpIn.GetStaticPad("src")
	rtcpPad.AddProbe(gst.PadProbeTypeBuffer, NewRtcpSsrcFilter(wt.SSRC))
	if err := pipeline.LinkPad(
		rtcpPad,
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
	wt.BinPad = rtpbinPad
	wt.SelPad = wt.parent.InputSelector.GetRequestPad("sink_%u")
	if err := pipeline.LinkPad(
		wt.BinPad,
		wt.SelPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtpbin pad to depayloader: %w", err)
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
	wt.BinPad = nil

	for _, elem := range []*gst.Element{
		wt.WebrtcRtpIn,
		wt.WebrtcRtcpIn,
	} {
		if err := elem.SetState(gst.StateNull); err != nil {
			wt.log.Errorw("Failed to set webrtc track element to null state", err, "element", elem.GetName())
		}
	}

	wt.parent.pipeline.Pipeline().RemoveMany(
		wt.WebrtcRtpIn,
		wt.WebrtcRtcpIn,
	)

	wt.log.Infow("Closed webrtc track", "ssrc", wt.SSRC)
	wt.parent.Tracks.Delete(wt.SSRC)
	wt.log.Infow("Removed webrtc track from parent", "ssrc", wt.SSRC)
	return nil
}
