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

	// Remote address for UpdateRemotePort after re-INVITE
	remoteAddr netip.Addr

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
	sm.log.Infow("Creating screenshare pipeline with negotiated codec from SDP answer",
		"codec", media.Codec.Name,
		"payloadType", media.Codec.PayloadType,
		"direction", media.Direction,
	)
	p, err := screenshare_pipeline.New(sm.log, media.Codec.PayloadType)
	if err != nil {
		return nil, fmt.Errorf("failed to create screenshare pipeline: %w", err)
	}
	// p.Monitor()

	// Setup WebRTC → SIP pipeline
	// Input: WebRTC RTP from sipRtpIn (gets swapped with WebRTC track reader in WebrtcTrackInput)
	webrtcRtpIn, err := NewGstWriter(p.WebrtcToSip.WebrtcRtpAppSrc)
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP input writer: %w", err)
	}
	go func() {
		sm.Copy(webrtcRtpIn, sm.sipRtpIn)
		// Copy returned - WebRTC track has ended (EOF)
		sm.handleWebrtcTrackEnded()
	}()

	// Output: H264 RTP to SIP device via sipRtpOut
	sipRtpOut, err := NewGstReader(p.WebrtcToSip.SipRtpAppSink)
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP RTP output reader: %w", err)
	}
	// Wrap sipRtpOut with debug logging to track actual UDP writes
	go sm.Copy(sm.sipRtpOut, sipRtpOut)

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

// IsReady returns true if the screenshare manager is set up and ready to receive tracks.
func (sm *ScreenshareManager) IsReady() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.status >= VideoStatusReady
}

// SetRemoteAddr stores the remote address for UpdateRemotePort after re-INVITE.
func (sm *ScreenshareManager) SetRemoteAddr(remoteAddr netip.Addr) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.remoteAddr = remoteAddr
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

	sm.log.Infow("screenshare remote port updated from re-INVITE", "rtpAddr", newRtpDst, "rtcpAddr", newRtcpDst)
}

// handleWebrtcTrackEnded is called when the WebRTC→SIP Copy loop terminates (track EOF).
// This triggers BFCP floor release so the Poly switches back to video mode.
// IMPORTANT: We must stop the VideoManager to reset the status to Stopped, so that
// the next screenshare track triggers a re-INVITE and recreates the pipeline + Copy loops.
func (sm *ScreenshareManager) handleWebrtcTrackEnded() {
	if !sm.active.Load() {
		return // Already stopped
	}

	sm.log.Infow("WebRTC screenshare track ended - stopping manager and triggering BFCP floor release")

	// Stop the VideoManager to reset status to Stopped.
	// This is critical for the restart flow: when the next WebRTC track arrives,
	// IsReady() will return false (status=Stopped), triggering a re-INVITE which
	// recreates the pipeline and Copy loops.
	if err := sm.VideoManager.Stop(); err != nil {
		sm.log.Warnw("Failed to stop video manager after track ended", err)
	}

	// Mark as inactive and trigger callback
	if sm.active.Swap(false) {
		sm.floorHeld.Store(false)
		if sm.OnScreenshareStopped != nil {
			sm.OnScreenshareStopped()
		}
	}
}
