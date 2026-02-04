package pipeline

import (
	"fmt"
	"runtime/cgo"

	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// func (p *Pipeline) Configure(remote netip.Addr, media *sdpv2.SDPMedia) error {
// 	pt := media.Codec.PayloadType

// 	h264Caps := fmt.Sprintf(
// 		"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
// 		pt)

// 	p.Log.Infow("Setting SIP config",
// 		"caps", h264Caps,
// 	)

// 	if _, err := p.SipRtpBin.Connect("request-pt-map", func(self *gst.Element, session uint, sipPt uint) *gst.Caps {
// 		if sipPt == uint(pt) {
// 			return gst.NewCapsFromString(h264Caps)
// 		}
// 		return nil
// 	}); err != nil {
// 		return fmt.Errorf("failed to connect to rtpbin request-pt-map signal: %w", err)
// 	}

// 	if err := p.SipConn.SetProperty("caps",
// 		gst.NewCapsFromString(h264Caps), //+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
// 	); err != nil {
// 		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
// 	}

// 	if err := p.Vp8H264.SetProperty("h264-caps",
// 		gst.NewCapsFromString(h264Caps),
// 	); err != nil {
// 		return fmt.Errorf("failed to set vp8h264 h264 caps filter caps (pt: %d): %w", pt, err)
// 	}

// 	if err := p.SipConn.SetProperty("remote-ip", remote.String()); err != nil {
// 		return fmt.Errorf("failed to set webrtc remote ip: %w", err)
// 	}

// 	if err := p.SipConn.SetProperty("remote-rtp-port", int(media.Port)); err != nil {
// 		return fmt.Errorf("failed to set sip remote rtp port: %w", err)
// 	}

// 	if err := p.SipConn.SetProperty("remote-rtcp-port", int(media.RTCPPort)); err != nil {
// 		return fmt.Errorf("failed to set sip remote rtcp port: %w", err)
// 	}

// 	return nil
// }

// func (p *Pipeline) SipRtpPort() uint16 {
// 	sipConnPortVal, err := p.SipConn.GetProperty("rtp-port")
// 	if err != nil {
// 		p.Log.Errorw("failed to get sip rtp port", err)
// 		return 0
// 	}
// 	sipConnPort, ok := sipConnPortVal.(int)
// 	if !ok {
// 		p.Log.Errorw("sip rtp port property is not uint16", nil, "value", sipConnPortVal)
// 		return 0
// 	}
// 	return uint16(sipConnPort)
// }

// func (p *Pipeline) SipRtcpPort() uint16 {
// 	sipConnPortVal, err := p.SipConn.GetProperty("rtcp-port")
// 	if err != nil {
// 		p.Log.Errorw("failed to get sip rtcp port", err)
// 		return 0
// 	}
// 	sipConnPort, ok := sipConnPortVal.(int)
// 	if !ok {
// 		p.Log.Errorw("sip rtcp port property is not uint16", nil, "value", sipConnPortVal)
// 		return 0
// 	}
// 	return uint16(sipConnPort)
// }

func (p *Pipeline) SetRoomCallbacks(callbacks *lksdk.RoomCallback) error {
	cbHandle := cgo.NewHandle(callbacks)
	defer cbHandle.Delete()
	if err := p.WebrtcIo.LkRoom.SetProperty("callbacks", uint64(uintptr(cbHandle))); err != nil {
		p.Log.Errorw("failed to set room callbacks", err)
		return fmt.Errorf("failed to set room callbacks: %w", err)
	}
	return nil
}

func (p *Pipeline) GetRoom() (*lksdk.Room, error) {
	roomHnd, err := p.WebrtcIo.LkRoom.GetProperty("room")
	if err != nil {
		p.Log.Errorw("failed to get room property", err)
		return nil, fmt.Errorf("failed to get room property: %w", err)
	}
	roomHndUint, ok := roomHnd.(uint64)
	if !ok {
		p.Log.Errorw("room property is not uint64", nil, "value", roomHnd)
		return nil, fmt.Errorf("room property is not uint64")
	}
	h := cgo.Handle(uintptr(roomHndUint))
	if h == 0 {
		p.Log.Errorw("room handle is invalid", nil, "value", roomHndUint)
		return nil, fmt.Errorf("room handle is invalid")
	}
	obj := h.Value()
	room, ok := obj.(*lksdk.Room)
	if !ok {
		p.Log.Errorw("room handle value is not *lksdk.Room", nil, "value", obj)
		return nil, fmt.Errorf("room handle value is not *lksdk.Room")
	}
	return room, nil
}

func (p *Pipeline) SetRoomOptions(wsUrl, token string, opts ...lksdk.ConnectOption) error {
	p.Log.Infow("Setting room options", "wsUrl", wsUrl)
	if err := p.WebrtcIo.LkRoom.SetProperty("ws-url", wsUrl); err != nil {
		return fmt.Errorf("failed to set ws-url property: %w", err)
	}
	if err := p.WebrtcIo.LkRoom.SetProperty("token", token); err != nil {
		return fmt.Errorf("failed to set token property: %w", err)
	}

	optsHandle := cgo.NewHandle(opts)
	defer optsHandle.Delete()
	if err := p.WebrtcIo.LkRoom.SetProperty("connect-options", uint64(uintptr(optsHandle))); err != nil {
		return fmt.Errorf("failed to set connect-options property: %w", err)
	}

	res, err := p.WebrtcIo.LkRoom.Emit("join-room")
	if err != nil {
		return fmt.Errorf("failed to emit join-room signal: %v", err)
	}
	if success, ok := res.(bool); !ok || !success {
		return fmt.Errorf("join-room signal reported failure")
	}
	p.Log.Infow("Joined room successfully", "wsUrl", wsUrl)

	p.SetState(gst.StatePlaying)

	return nil
}
