package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

func NewWebrtcToSipChain(log logger.Logger, parent *Pipeline) *WebrtcToSip {
	return &WebrtcToSip{
		log:      log,
		pipeline: parent,
	}
}

type WebrtcToSip struct {
	pipeline *Pipeline
	log      logger.Logger

	// Vp8H264 *gst.Element
	RtpOpusDepay  *gst.Element
	OpusDec       *gst.Element
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	MuLawEnc      *gst.Element
	RtpPcmuPay    *gst.Element
}

var _ GstChain = (*WebrtcToSip)(nil)

// Create implements [GstChain].
func (stw *WebrtcToSip) Create() error {
	var err error

	// stw.Vp8H264, err = gst.NewElement("vp8-h264")
	// if err != nil {
	// 	return fmt.Errorf("failed to create VP8 to H264 element: %w", err)
	// }

	stw.RtpOpusDepay, err = gst.NewElement("rtpopusdepay")
	if err != nil {
		return fmt.Errorf("failed to create RTP Opus depay element: %w", err)
	}

	stw.OpusDec, err = gst.NewElement("opusdec")
	if err != nil {
		return fmt.Errorf("failed to create Opus decoder element: %w", err)
	}

	stw.AudioConvert, err = gst.NewElement("audioconvert")
	if err != nil {
		return fmt.Errorf("failed to create audio convert element: %w", err)
	}

	stw.AudioResample, err = gst.NewElement("audioresample")
	if err != nil {
		return fmt.Errorf("failed to create audio resample element: %w", err)
	}

	stw.MuLawEnc, err = gst.NewElement("mulawenc")
	if err != nil {
		return fmt.Errorf("failed to create MuLaw encoder element: %w", err)
	}

	stw.RtpPcmuPay, err = gst.NewElement("rtppcmupay")
	if err != nil {
		return fmt.Errorf("failed to create RTP PCMU pay element: %w", err)
	}

	return nil
}

func (stw *WebrtcToSip) Add() error {
	if err := stw.pipeline.Pipeline().AddMany(
		// stw.Vp8H264,
		stw.RtpOpusDepay,
		stw.OpusDec,
		stw.AudioConvert,
		stw.AudioResample,
		stw.MuLawEnc,
		stw.RtpPcmuPay,
	); err != nil {
		return fmt.Errorf("failed to add WebRTC to SIP elements to pipeline: %w", err)
	}
	return nil
}

func (stw *WebrtcToSip) Link() error {
	if err := gst.ElementLinkMany(
		stw.RtpOpusDepay,
		stw.OpusDec,
		stw.AudioConvert,
		stw.AudioResample,
		stw.MuLawEnc,
		stw.RtpPcmuPay,
	); err != nil {
		return fmt.Errorf("failed to link WebRTC to SIP elements: %w", err)
	}

	return nil
}

func (stw *WebrtcToSip) Close() error {
	if err := stw.pipeline.Pipeline().RemoveMany(
		// stw.Vp8H264,
		stw.RtpOpusDepay,
		stw.OpusDec,
		stw.AudioConvert,
		stw.AudioResample,
		stw.MuLawEnc,
		stw.RtpPcmuPay,
	); err != nil {
		return fmt.Errorf("failed to remove WebRTC to SIP elements from pipeline: %w", err)
	}
	return nil
}
