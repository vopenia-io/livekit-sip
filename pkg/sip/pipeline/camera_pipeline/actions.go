package camera_pipeline

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"runtime"
	"runtime/cgo"

	"github.com/go-gst/go-gst/gst"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/event"
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
	ready = ready && checkHandle(cp.WebrtcRtpOut)
	ready = ready && checkHandle(cp.WebrtcRtcpOut)

	if ready {
		cp.Log().Infow("All handles ready, setting pipeline to PLAYING")
		if err := cp.SetState(gst.StatePlaying); err != nil {
			return fmt.Errorf("failed to set camera pipeline to playing: %w", err)
		}

		for _, e := range []*gst.Element{
			cp.WebrtcRtpOut,
			cp.WebrtcRtcpOut,
		} {
			if !e.SyncStateWithParent() {
				return fmt.Errorf("failed to sync state with parent for element %s", e.GetName())
			}
		}
	}
	return nil
}

func (cp *CameraPipeline) Configure(remote netip.Addr, media *sdpv2.SDPMedia) error {
	pt := media.Codec.PayloadType

	h264Caps := fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
		pt)

	cp.Log().Infow("Setting SIP config",
		"caps", h264Caps,
	)

	if _, err := cp.SipRtpBin.Connect("request-pt-map", event.RegisterCallback(context.TODO(), cp.Loop(), func(self *gst.Element, session uint, sipPt uint) *gst.Caps {
		if sipPt == uint(pt) {
			return gst.NewCapsFromString(h264Caps)
		}
		return nil
	})); err != nil {
		return fmt.Errorf("failed to connect to rtpbin request-pt-map signal: %w", err)
	}

	// if err := cp.WebrtcToSip.Vp8H264.SetProperty("caps",
	// 	gst.NewCapsFromString(h264Caps+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
	// ); err != nil {
	// 	return fmt.Errorf("failed to set webrtc to sip caps filter caps (pt: %d): %w", pt, err)
	// }

	if err := cp.SipConn.SetProperty("caps",
		gst.NewCapsFromString(h264Caps), //+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
	}
	if err := cp.SipConn.SetProperty("caps",
		gst.NewCapsFromString(h264Caps), //+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
	}
	if err := cp.SipConn.SetProperty("caps",
		gst.NewCapsFromString(h264Caps), //+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
	}
	if err := cp.SipConn.SetProperty("caps",
		gst.NewCapsFromString(h264Caps), //+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
	}
	if err := cp.SipConn.SetProperty("caps",
		gst.NewCapsFromString(h264Caps), //+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
	}
	if err := cp.SipConn.SetProperty("caps",
		gst.NewCapsFromString(h264Caps+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
	}
	if err := cp.SipConn.SetProperty("caps",
		gst.NewCapsFromString(h264Caps), //+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
	}
	if err := cp.SipConn.SetProperty("caps",
		gst.NewCapsFromString(h264Caps), //+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
	}

	if err := cp.SipConn.SetProperty("remote-ip", remote.String()); err != nil {
		return fmt.Errorf("failed to set webrtc remote ip: %w", err)
	}

	if err := cp.SipConn.SetProperty("remote-rtp-port", int(media.Port)); err != nil {
		return fmt.Errorf("failed to set sip remote rtp port: %w", err)
	}

	if err := cp.SipConn.SetProperty("remote-rtcp-port", int(media.RTCPPort)); err != nil {
		return fmt.Errorf("failed to set sip remote rtcp port: %w", err)
	}

	cp.checkReady()

	return nil
}

func (cp *CameraPipeline) SipRtpPort() uint16 {
	sipConnPortVal, err := cp.SipConn.GetProperty("rtp-port")
	if err != nil {
		cp.Log().Errorw("failed to get sip rtp port", err)
		return 0
	}
	sipConnPort, ok := sipConnPortVal.(int)
	if !ok {
		cp.Log().Errorw("sip rtp port property is not uint16", nil, "value", sipConnPortVal)
		return 0
	}
	return uint16(sipConnPort)
}

func (cp *CameraPipeline) SipRtcpPort() uint16 {
	sipConnPortVal, err := cp.SipConn.GetProperty("rtcp-port")
	if err != nil {
		cp.Log().Errorw("failed to get sip rtcp port", err)
		return 0
	}
	sipConnPort, ok := sipConnPortVal.(int)
	if !ok {
		cp.Log().Errorw("sip rtcp port property is not uint16", nil, "value", sipConnPortVal)
		return 0
	}
	return uint16(sipConnPort)
}

func (cp *CameraPipeline) WebrtcOutput(rtp, rtcp io.WriteCloser) error {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()
	rtcpHnd := cgo.NewHandle(rtcp)
	defer rtcpHnd.Delete()

	if err := cp.WebrtcRtpOut.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return fmt.Errorf("failed to set webrtc rtp out handle: %w", err)
	}

	if err := cp.WebrtcRtcpOut.SetProperty("handle", uint64(rtcpHnd)); err != nil {
		return fmt.Errorf("failed to set webrtc rtcp out handle: %w", err)
	}

	cp.checkReady()

	return nil
}

func (cp *CameraPipeline) AddWebrtcTrack(ssrc uint32, rtp, rtcp io.ReadCloser) (*WebrtcTrack, error) {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()
	rtcpHnd := cgo.NewHandle(rtcp)
	defer rtcpHnd.Delete()

	cp.Log().Infow("Adding WebRTC track", "ssrc", ssrc)

	track, err := pipeline.AddChain(cp, NewWebrtcTrack(cp.Log(), cp.WebrtcIo, ssrc))
	if err != nil {
		return nil, fmt.Errorf("failed to add webrtc track chain: %w", err)
	}

	cp.WebrtcIo.Tracks.Store(ssrc, track)

	if err := track.WebrtcRtpIn.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return nil, fmt.Errorf("failed to set webrtc rtp in handle: %w", err)
	}

	if err := track.WebrtcRtcpIn.SetProperty("handle", uint64(rtcpHnd)); err != nil {
		return nil, fmt.Errorf("failed to set webrtc rtcp in handle: %w", err)
	}

	if err := pipeline.LinkChains(cp, track); err != nil {
		return nil, fmt.Errorf("failed to link webrtc track chain: %w", err)
	}

	return track, nil
}

func (cp *CameraPipeline) RemoveWebrtcTrack(ssrc uint32) error {
	track, ok := cp.WebrtcIo.Tracks.Load(ssrc)
	if !ok {
		return fmt.Errorf("webrtc track with ssrc %d not found", ssrc)
	}

	if err := track.Close(); err != nil {
		return fmt.Errorf("failed to close webrtc track with ssrc %d: %w", ssrc, err)
	}

	var newTrack *WebrtcTrack
	for s, t := range cp.WebrtcIo.Tracks.All() {
		if s != ssrc {
			newTrack = t
			break
		}
	}

	if newTrack != nil {
		if err := cp.DirtySwitchWebrtcInput(newTrack.SSRC); err != nil {
			return fmt.Errorf("failed to switch webrtc input to ssrc %d: %w", newTrack.SSRC, err)
		}
	}

	return nil
}

func (cp *CameraPipeline) DirtySwitchWebrtcInput(ssrc uint32) error {
	track, ok := cp.WebrtcIo.Tracks.Load(ssrc)
	if !ok {
		return fmt.Errorf("webrtc track with ssrc %d not found", ssrc)
	}

	if track.SelPad == nil {
		return fmt.Errorf("webrtc track with ssrc %d has no sel pad", ssrc)
	}

	active, err := cp.WebrtcIo.InputSelector.GetProperty("active-pad")
	if err == nil {
		activePad, ok := active.(*gst.Pad)
		if ok {
			if activePad.GetName() == track.SelPad.GetName() {
				cp.Log().Infow("webrtc input already set to desired ssrc", "ssrc", ssrc)
				return nil
			}
		} else {
			cp.Log().Errorw("active webrtc input pad is not a gst.Pad", nil,
				"pad", active,
			)
		}
	} else {
		cp.Log().Errorw("failed to get active pad from webrtc input selector", err)
	}

	cp.Log().Infow("switching webrtc input to new ssrc", "ssrc", ssrc)

	if err := cp.WebrtcIo.InputSelector.SetProperty("active-pad",
		track.SelPad,
	); err != nil {
		return fmt.Errorf("failed to switch webrtc input to ssrc %d: %w", ssrc, err)
	} else {
		cp.Log().Infow("switched webrtc input to new ssrc", "ssrc", ssrc)
		if err := cp.RequestTrackKeyframe(track); err != nil {
			return fmt.Errorf("failed to request keyframe for webrtc track ssrc %d: %w", ssrc, err)
		}
	}

	return nil
}

func (cp *CameraPipeline) RequestTrackKeyframe(wt *WebrtcTrack) error {
	cp.Log().Infow("Requesting keyframe for webrtc track", "ssrc", wt.SSRC)

	fkuStruct := gst.NewStructure("GstForceKeyUnit")
	runtime.SetFinalizer(fkuStruct, nil)
	fkuStruct.SetValue("ssrc", wt.SSRC)
	// fkuStruct.SetValue("payload", uint(96))
	fkuStruct.SetValue("running-time", gst.ClockTimeNone)
	fkuStruct.SetValue("all-headers", false)
	fkuStruct.SetValue("count", uint(0))

	fkuEvent := gst.NewCustomEvent(gst.EventTypeCustomUpstream, fkuStruct)

	srcPad := wt.BinPad
	if srcPad == nil {
		cp.Log().Warnw("Cannot send GstForceKeyUnit event upstream, bin pad is nil", nil, "ssrc", wt.SSRC)
		return nil
	}

	if srcPad.SendEvent(fkuEvent) {
		cp.Log().Infow("Sent GstForceKeyUnit event upstream", "ssrc", wt.SSRC)
	} else {
		cp.Log().Warnw("Failed to send GstForceKeyUnit event upstream", nil, "ssrc", wt.SSRC)
	}

	return nil
}
