package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

func NewSipToWebrtcChain(log logger.Logger, parent *Pipeline) *SipToWebrtc {
	return &SipToWebrtc{
		log:      log,
		pipeline: parent,
	}
}

type SipToWebrtc struct {
	pipeline *Pipeline
	log      logger.Logger

	// H264Vp8 *gst.Element

	RtpPcmuDepay  *gst.Element
	MuLawDec      *gst.Element
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	OpusEnc       *gst.Element
	RtpOpusPay    *gst.Element
}

var _ GstChain = (*SipToWebrtc)(nil)

// Create implements [GstChain].
func (stw *SipToWebrtc) Create() error {
	var err error

	// stw.H264Vp8, err = gst.NewElement("h264-vp8")
	// if err != nil {
	// 	return fmt.Errorf("failed to create H264 to VP8 element: %w", err)
	// }

	stw.RtpPcmuDepay, err = gst.NewElement("rtppcmudepay")
	if err != nil {
		return fmt.Errorf("failed to create RTP PCMU depay element: %w", err)
	}

	stw.MuLawDec, err = gst.NewElement("mulawdec")
	if err != nil {
		return fmt.Errorf("failed to create MuLaw decoder element: %w", err)
	}

	stw.AudioConvert, err = gst.NewElement("audioconvert")
	if err != nil {
		return fmt.Errorf("failed to create audio convert element: %w", err)
	}

	stw.AudioResample, err = gst.NewElement("audioresample")
	if err != nil {
		return fmt.Errorf("failed to create audio resample element: %w", err)
	}

	stw.OpusEnc, err = gst.NewElement("opusenc")
	if err != nil {
		return fmt.Errorf("failed to create Opus encoder element: %w", err)
	}

	stw.RtpOpusPay, err = gst.NewElementWithProperties("rtpopuspay", map[string]interface{}{
		"pt": 111,
	})
	if err != nil {
		return fmt.Errorf("failed to create RTP Opus pay element: %w", err)
	}

	return nil
}

func (stw *SipToWebrtc) Add() error {
	if err := stw.pipeline.Pipeline().AddMany(
		// stw.H264Vp8,
		stw.RtpPcmuDepay,
		stw.MuLawDec,
		stw.AudioConvert,
		stw.AudioResample,
		stw.OpusEnc,
		stw.RtpOpusPay,
	); err != nil {
		return fmt.Errorf("failed to add SIP to WebRTC elements to pipeline: %w", err)
	}
	return nil
}

func (stw *SipToWebrtc) Link() error {
	if err := gst.ElementLinkMany(
		stw.RtpPcmuDepay,
		stw.MuLawDec,
		stw.AudioConvert,
		stw.AudioResample,
		stw.OpusEnc,
		stw.RtpOpusPay,
	); err != nil {
		return fmt.Errorf("failed to link SIP to WebRTC elements: %w", err)
	}
	return nil
}

func (stw *SipToWebrtc) Close() error {
	if err := stw.pipeline.Pipeline().RemoveMany(
		// stw.H264Vp8,
		stw.RtpPcmuDepay,
		stw.MuLawDec,
		stw.AudioConvert,
		stw.AudioResample,
		stw.OpusEnc,
		stw.RtpOpusPay,
	); err != nil {
		return fmt.Errorf("failed to remove SIP to WebRTC elements from pipeline: %w", err)
	}
	return nil
}
