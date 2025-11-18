package sip

import (
	"fmt"
	"io"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

func Copy(dst io.WriteCloser, src io.ReadCloser) {
	n, err := io.Copy(dst, src)
	fmt.Printf("Copied %d bytes with err=%v\n", n, err)
	src.Close()
	dst.Close()
}

func (v *VideoManager) SetupGstPipeline(media *sdpv2.SDPMedia) error {
	pipeline, err := pipeline.NewSipWebRTCPipeline(int(media.Codec.PayloadType), int(media.Codec.PayloadType))
	if err != nil {
		return fmt.Errorf("failed to create SIP WebRTC pipeline: %w", err)
	}
	pipeline.Monitor()

	sipRtpIn, err := NewGstWriter(pipeline.SipToWebRTC.AppSrc)
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP reader: %w", err)
	}
	go Copy(sipRtpIn, v.sipRtpIn)

	sipRtpOut, err := NewGstReader(pipeline.SelectorToSip.AppSink)
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP writer: %w", err)
	}
	go Copy(v.sipRtpOut, sipRtpOut)

	webrtcRtpOut, err := NewGstReader(pipeline.SipToWebRTC.AppSink)
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP writer: %w", err)
	}
	go Copy(v.webrtcRtpOut, webrtcRtpOut)

	v.pipeline = pipeline

	return nil
}
