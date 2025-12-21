package camera_pipeline

import (
	"fmt"
	"io"
	"net"
	"runtime/cgo"

	"github.com/go-gst/go-gst/gst"
)

func (cp *CameraPipeline) checkReady() error {

	if cp.Pipeline().GetCurrentState() != gst.StatePaused {
		// Already playing
		return nil
	}

	checkHandle := func(elem *gst.Element) bool {
		hasHandleVal, err := elem.GetProperty("has-handle")
		if err != nil {
			return false
		}
		hasHandle, ok := hasHandleVal.(bool)
		return ok && hasHandle
	}

	ready := true
	ready = ready && checkHandle(cp.SipRtpIn)
	// ready = ready && checkHandle(cp.SipRtcpIn)
	// ready = ready && checkHandle(cp.SipRtcpOut)
	ready = ready && checkHandle(cp.WebrtcRtpOut)

	if ready {
		cp.Log().Infow("All handles ready, setting pipeline to PLAYING")
		if err := cp.SetState(gst.StatePlaying); err != nil {
			return fmt.Errorf("failed to set camera pipeline to playing: %w", err)
		}
	}
	return nil
}

func (cp *CameraPipeline) SipIO(rtp, rtcp net.Conn, pt uint8) error {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()
	rtcpHandle := cgo.NewHandle(rtcp)
	defer rtcpHandle.Delete()

	if err := cp.SipRtpIn.SetProperty("caps",
		gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
			pt,
		)),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
	}

	if err := cp.SipRtpIn.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return fmt.Errorf("failed to set rtp in handle: %w", err)
	}

	if err := cp.SipRtcpIn.SetProperty("handle", uint64(rtcpHandle)); err != nil {
		return fmt.Errorf("failed to set rtcp in handle: %w", err)
	}

	if err := cp.SipRtcpOut.SetProperty("handle", uint64(rtcpHandle)); err != nil {
		return fmt.Errorf("failed to set rtcp out handle: %w", err)
	}

	cp.checkReady()

	return nil
}

func (cp *CameraPipeline) WebrtcOutput(rtp, rtcp io.WriteCloser) error {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()
	// rtcpHnd := cgo.NewHandle(rtcp)
	// defer rtcpHnd.Delete()

	if err := cp.WebrtcRtpOut.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return fmt.Errorf("failed to set webrtc rtp out handle: %w", err)
	}

	cp.checkReady()

	return nil
}
