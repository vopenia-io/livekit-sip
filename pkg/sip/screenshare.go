package sip

import (
	"context"
	"fmt"
	"net/netip"
	"sync/atomic"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline/screenshare_pipeline"
)

type ScreenshareManager struct {
	*VideoManager
	active    atomic.Bool
	floorHeld atomic.Bool // BFCP floor is held by remote SIP participant

	// Remote address for UpdateRemotePort after re-INVITE
	remoteAddr netip.Addr

	// pendingMedia is used during Reconcile to pass media to CreateVideoPipeline
	// before the base VideoManager sets v.Media
	pendingMedia *sdpv2.SDPMedia

	// Callbacks for screenshare lifecycle events
	OnScreenshareStarted func() // Called when WebRTC screenshare track starts
	OnScreenshareStopped func() // Called when WebRTC screenshare track stops
}

func NewScreenshareManager(log logger.Logger, ctx context.Context, opts *MediaOptions) (*ScreenshareManager, error) {
	sm := &ScreenshareManager{}

	vm, err := NewVideoManager(log.WithComponent("screenshare"), ctx, opts, sm)
	if err != nil {
		return nil, err
	}
	sm.VideoManager = vm

	return sm, nil
}

func (sm *ScreenshareManager) CreateVideoPipeline(opt *MediaOptions) (SipPipeline, error) {
	// Use pendingMedia which is set in Reconcile before base VideoManager.Reconcile() is called
	media := sm.pendingMedia
	if media == nil || media.Codec == nil {
		return nil, fmt.Errorf("screenshare media not configured")
	}

	pipeline, err := screenshare_pipeline.New(sm.ctx, sm.log, media.Codec.PayloadType)
	if err != nil {
		return nil, fmt.Errorf("failed to create screenshare pipeline: %w", err)
	}

	// Set up EOS callback to handle track end
	pipeline.OnTrackEnded = sm.handleWebrtcTrackEnded

	return pipeline, nil
}

func (sm *ScreenshareManager) Reconcile(remote netip.Addr, media *sdpv2.SDPMedia) (ReconcileStatus, error) {
	// Store remote address for later use
	sm.remoteAddr = remote

	// Store pendingMedia for CreateVideoPipeline to use during resetPipeline().
	sm.pendingMedia = media

	isRealScreenshare := media.Content == sdpv2.ContentTypeSlides && media.Port > 0

	// If screenshare pipeline is already running/ready and we get real screenshare media
	// from re-INVITE, just update the RTP destination without resetting the pipeline.
	// This handles the case where initial INVITE set up pipeline with placeholder port,
	// then re-INVITE provides the actual destination.
	if isRealScreenshare && sm.status >= VideoStatusReady {
		sm.log.Infow("screenshare.reconcile: updating RTP destination from re-INVITE (pipeline already active)",
			"status", sm.status,
			"rtpDst", netip.AddrPortFrom(remote, media.Port),
			"rtcpDst", netip.AddrPortFrom(remote, media.RTCPPort))
		sm.rtpConn.SetDst(netip.AddrPortFrom(remote, media.Port))
		sm.rtcpConn.SetDst(netip.AddrPortFrom(remote, media.RTCPPort))
		// Update Media to reflect the new port, but keep the pipeline running
		sm.Media = media
		sm.pendingMedia = nil
		return ReconcileStatusUpdated, nil
	}

	// Create a copy of media for screenshare.
	// For initial INVITE (no screenshare port yet), we use a placeholder port (1)
	// so that the base VideoManager creates the pipeline. The real destination
	// will be set when re-INVITE provides the actual screenshare port.
	screenshareMedia := *media
	if !isRealScreenshare {
		// Use placeholder port (1) instead of 0 to force pipeline creation.
		// Port 0 would make isMedia() return false and skip pipeline creation.
		screenshareMedia.Port = 1
		screenshareMedia.RTCPPort = 1
		sm.log.Debugw("screenshare.reconcile: using placeholder port for initial setup, real port will come from re-INVITE")
	} else {
		sm.log.Debugw("screenshare.reconcile: using screenshare media with port", "port", media.Port, "rtcpPort", media.RTCPPort)
	}

	rs, err := sm.VideoManager.Reconcile(remote, &screenshareMedia)
	if err != nil {
		return rs, err
	}

	// Clear pendingMedia after reconcile completes
	sm.pendingMedia = nil

	return rs, nil
}

func (sm *ScreenshareManager) Start() error {
	return sm.VideoManager.Start()
}

// WebrtcTrackInput sets the WebRTC screenshare track input.
// NO GOROUTINES - sets the io.Reader handle on the sourcereader element.
func (sm *ScreenshareManager) WebrtcTrackInput(ti *TrackInput, sid string, ssrc uint32) error {
	if sm.active.Swap(true) {
		sm.log.Warnw("screenshare already active", nil)
		return fmt.Errorf("screenshare already active")
	}

	if sm.status != VideoStatusStarted {
		sm.log.Warnw("video manager not started", nil, "status", sm.status)
		sm.active.Store(false)
		return fmt.Errorf("video manager not started")
	}

	sm.log.Debugw("screenshare.webrtc.track_input", "sid", sid, "ssrc", ssrc)

	p, ok := sm.pipeline.(*screenshare_pipeline.ScreensharePipeline)
	if !ok {
		sm.active.Store(false)
		return fmt.Errorf("invalid pipeline type for screenshare")
	}

	// Set the WebRTC input on the sourcereader element - NO GOROUTINES
	if err := p.SetWebrtcInput(ti.RtpIn); err != nil {
		sm.log.Errorw("failed to set WebRTC input", err)
		sm.active.Store(false)
		return fmt.Errorf("failed to set WebRTC input: %w", err)
	}

	// Notify that screenshare has started
	if sm.OnScreenshareStarted != nil {
		sm.OnScreenshareStarted()
	}

	return nil
}

func (sm *ScreenshareManager) Stop() error {
	wasActive := sm.active.Swap(false)

	if err := sm.VideoManager.stop(); err != nil {
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
func (sm *ScreenshareManager) SetFloorHeld(held bool) {
	sm.floorHeld.Store(held)
	sm.log.Debugw("screenshare.floor_state", "held", held)
}

// HasFloor returns true if the remote SIP participant holds the floor.
func (sm *ScreenshareManager) HasFloor() bool {
	return sm.floorHeld.Load()
}

// IsReady returns true if the screenshare manager is set up and ready to receive tracks.
func (sm *ScreenshareManager) IsReady() bool {
	return sm.status >= VideoStatusReady
}

// IsActive returns true if a screenshare track is currently active.
func (sm *ScreenshareManager) IsActive() bool {
	return sm.active.Load()
}

// SetRemoteAddr stores the remote address for UpdateRemotePort after re-INVITE.
func (sm *ScreenshareManager) SetRemoteAddr(remoteAddr netip.Addr) {
	sm.remoteAddr = remoteAddr
}

// UpdateRemotePort updates the RTP destination after re-INVITE success.
func (sm *ScreenshareManager) UpdateRemotePort(port uint16) {
	newRtpDst := netip.AddrPortFrom(sm.remoteAddr, port)
	sm.rtpConn.SetDst(newRtpDst)
	sm.log.Debugw("screenshare.remote_port_updated", "rtpAddr", newRtpDst)
}

// handleWebrtcTrackEnded is called when the WebRTC track ends (EOS from sourcereader).
// This is called from GStreamer's thread via the EOS callback - NO GOROUTINES needed.
func (sm *ScreenshareManager) handleWebrtcTrackEnded() {
	if !sm.active.Load() {
		return
	}

	sm.log.Debugw("screenshare.webrtc.track_ended")

	if err := sm.VideoManager.stop(); err != nil {
		sm.log.Warnw("failed to stop video manager", err)
	}

	if sm.active.Swap(false) {
		sm.floorHeld.Store(false)
		if sm.OnScreenshareStopped != nil {
			sm.OnScreenshareStopped()
		}
	}
}
