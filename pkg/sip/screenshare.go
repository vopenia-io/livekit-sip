package sip

import (
	"fmt"
	"net/netip"
	"sync/atomic"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/screenshare_pipeline"
)

type ScreenshareManager struct {
	*VideoManager
	active    atomic.Bool
	floorHeld atomic.Bool // BFCP floor is held by remote SIP participant

	// Fallback tracking for re-INVITE negotiation
	usingFallback bool       // True if using main video port as fallback (no content port from initial SDP)
	remoteAddr    netip.Addr // Remote SIP device address for UpdateRemotePort

	// Callbacks for screenshare lifecycle events
	OnScreenshareStarted func() // Called when WebRTC screenshare track starts
	OnScreenshareStopped func() // Called when WebRTC screenshare track stops
}

func NewScreenshareManager(log logger.Logger, room *Room, opts *MediaOptions) (*ScreenshareManager, error) {
	sm := &ScreenshareManager{}

	vm, err := NewVideoManager(log, room, opts, sm)
	if err != nil {
		return nil, err
	}
	sm.VideoManager = vm

	return sm, nil
}

func (sm *ScreenshareManager) NewPipeline(media *sdpv2.SDPMedia) (pipeline.GspPipeline, error) {
	p, err := screenshare_pipeline.New(sm.log, media.Codec.PayloadType)
	if err != nil {
		return nil, fmt.Errorf("failed to create screenshare pipeline: %w", err)
	}
	// p.Monitor()

	// Start data flow monitoring for debugging
	// Pass a function to get the current RTP destination address
	p.WebrtcToSip.StartDataFlowMonitor(func() string {
		if ptr := sm.rtpConn.dst.Load(); ptr != nil && ptr.IsValid() {
			return ptr.String()
		}
		return "<not set>"
	})

	// Setup WebRTC → SIP pipeline
	// Input: WebRTC RTP from sipRtpIn (gets swapped with WebRTC track reader in WebrtcTrackInput)
	webrtcRtpIn, err := NewGstWriter(p.WebrtcToSip.RtpAppSrc)
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP input writer: %w", err)
	}
	go sm.Copy(webrtcRtpIn, sm.sipRtpIn)

	// Output: H264 RTP to SIP device via sipRtpOut
	sipRtpOut, err := NewGstReader(p.WebrtcToSip.RtpAppSink)
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP RTP output reader: %w", err)
	}
	// Wrap sipRtpOut with debug logging to track actual UDP writes
	go sm.CopyWithDebug(sm.sipRtpOut, sipRtpOut, "screenshare→SIP")

	return p, nil
}

func (sm *ScreenshareManager) WebrtcTrackInput(ti *TrackInput, sid string, ssrc uint32) {
	if sm.active.Swap(true) {
		sm.log.Warnw("WebRTC→SIP screenshare pipeline already active, ignoring additional track input", nil)
		return
	}

	// Capture callback before taking lock to avoid deadlock.
	// The OnScreenshareStarted callback may trigger BFCP floor grant,
	// which in turn calls NeedsReInvite() that also needs sm.mu.
	var notifyStarted func()

	sm.mu.Lock()
	if sm.status != VideoStatusStarted {
		sm.log.Warnw("video manager not started, cannot add WebRTC track input", nil, "status", sm.status)
		sm.mu.Unlock()
		return
	}

	sm.log.Infow("WebRTC screenshare track started - connecting WebRTC→SIP pipeline",
		"hasRtpIn", ti.RtpIn != nil,
		"hasRtcpIn", ti.RtcpIn != nil,
		"sid", sid,
		"ssrc", ssrc)

	if r := sm.sipRtpIn.Swap(ti.RtpIn); r != nil {
		_ = r.Close()
	}

	notifyStarted = sm.OnScreenshareStarted
	sm.mu.Unlock()

	// Notify that screenshare has started (triggers BFCP floor request).
	// Called OUTSIDE the lock to avoid deadlock with NeedsReInvite().
	if notifyStarted != nil {
		notifyStarted()
	}
}

func (sm *ScreenshareManager) Stop() error {
	wasActive := sm.active.Swap(false)

	if err := sm.VideoManager.Stop(); err != nil {
		return err
	}
	sm.floorHeld.Store(false)

	// Notify that screenshare has stopped (triggers BFCP floor release)
	if wasActive && sm.OnScreenshareStopped != nil {
		sm.OnScreenshareStopped()
	}
	return nil
}

// SetFloorHeld updates the BFCP floor state for screenshare.
// When floor is granted, the SIP device is allowed to send content.
func (sm *ScreenshareManager) SetFloorHeld(held bool) {
	sm.floorHeld.Store(held)
	sm.log.Infow("BFCP floor state changed", "floorHeld", held)
}

// HasFloor returns true if the remote SIP participant holds the floor.
func (sm *ScreenshareManager) HasFloor() bool {
	return sm.floorHeld.Load()
}

// SetFallbackMode marks whether using video port fallback (no content port from initial SDP).
// This is called when the SIP device's initial SDP has no screenshare m=video line.
func (sm *ScreenshareManager) SetFallbackMode(fallback bool, remoteAddr netip.Addr) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.usingFallback = fallback
	sm.remoteAddr = remoteAddr
	sm.log.Infow("screenshare fallback mode set", "fallback", fallback, "remoteAddr", remoteAddr)
}

// NeedsReInvite returns true if content port needs negotiation via SIP re-INVITE.
func (sm *ScreenshareManager) NeedsReInvite() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.usingFallback
}

// UpdateRemotePort updates the RTP/RTCP destination after re-INVITE success.
// This is called when we receive the content port from the SIP device's 200 OK.
func (sm *ScreenshareManager) UpdateRemotePort(port uint16) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	newRtpDst := netip.AddrPortFrom(sm.remoteAddr, port)
	newRtcpDst := netip.AddrPortFrom(sm.remoteAddr, port+1)

	sm.rtpConn.SetDst(newRtpDst)
	sm.rtcpConn.SetDst(newRtcpDst)
	sm.usingFallback = false

	sm.log.Infow("screenshare remote port updated from re-INVITE", "rtpAddr", newRtpDst, "rtcpAddr", newRtcpDst)
}
