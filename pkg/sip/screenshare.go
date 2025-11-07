package sip

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
	mrtp "github.com/livekit/media-sdk/rtp"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/vopenia/bfcp"
)

// ScreenShareManager manages screen sharing from WebRTC to SIP with BFCP floor control
type ScreenShareManager struct {
	*VideoIO
	log      logger.Logger
	remote   netip.Addr
	opts     *MediaOptions
	room     *Room
	rtpConn  *udpConn
	rtcpConn *udpConn
	pipeline *gst.Pipeline

	// BFCP floor control
	bfcpClient       *bfcp.Client
	bfcpServerAddr   string
	bfcpConferenceID uint32
	bfcpUserID       uint16
	bfcpFloorID      uint16
	bfcpRequestID    uint16
	floorGranted     bool

	// State management
	mu              sync.RWMutex
	active          bool
	screenTrack     *webrtc.TrackRemote
	screenPub       *lksdk.RemoteTrackPublication
	screenParticip  *lksdk.RemoteParticipant
	negotiatedCodec *sdpv2.Codec // Negotiated codec from SDP answer

	// Callbacks
	onFloorGranted  func()
	onFloorRevoked  func()
	onStartCallback func() error // Callback to trigger re-INVITE
	onStopCallback  func() error // Callback to trigger re-INVITE
}

// NewScreenShareManager creates a new screen share manager
func NewScreenShareManager(log logger.Logger, room *Room, remote netip.Addr, opts *MediaOptions, bfcpServerAddr string) (*ScreenShareManager, error) {
	log.Infow("🖥️ [ScreenShare] Creating ScreenShareManager",
		"remoteAddr", remote.String(),
		"bfcpServer", bfcpServerAddr,
	)

	// Allocate RTP/RTCP port pair for screen share stream
	rtpConn, rtcpConn, err := mrtp.ListenUDPPortPair(opts.Ports.Start, opts.Ports.End, opts.IP)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP port pair for screen share RTP/RTCP: %w", err)
	}

	ssm := &ScreenShareManager{
		VideoIO:          NewVideoIO(),
		log:              log.WithComponent("screenshare"),
		room:             room,
		opts:             opts,
		remote:           remote,
		rtpConn:          newUDPConn(log.WithComponent("screenshare-rtp"), rtpConn),
		rtcpConn:         newUDPConn(log.WithComponent("screenshare-rtcp"), rtcpConn),
		bfcpServerAddr:   bfcpServerAddr,
		bfcpConferenceID: 1, // Default, should be extracted from SDP
		bfcpUserID:       1, // Default, should be assigned dynamically
		bfcpFloorID:      1, // Default, typically floor 1 is for presentation
		active:           false,
		floorGranted:     false,
	}

	ssm.log.Infow("🖥️ [ScreenShare] ScreenShareManager created",
		"rtpPort", ssm.RtpPort(),
		"rtcpPort", ssm.RtcpPort(),
	)

	return ssm, nil
}

// SetNegotiatedCodec sets the negotiated codec from SDP answer
func (s *ScreenShareManager) SetNegotiatedCodec(codec *sdpv2.Codec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.negotiatedCodec = codec
	s.log.Infow("🖥️ [ScreenShare] Negotiated codec set",
		"name", codec.Name,
		"payloadType", codec.PayloadType)
}

// RtpPort returns the local RTP port
func (s *ScreenShareManager) RtpPort() int {
	return s.rtpConn.LocalAddr().(*net.UDPAddr).Port
}

// RtcpPort returns the local RTCP port
func (s *ScreenShareManager) RtcpPort() int {
	return s.rtcpConn.LocalAddr().(*net.UDPAddr).Port
}

// SetupBFCP initializes the BFCP client for floor control
func (s *ScreenShareManager) SetupBFCP(ctx context.Context, serverAddr string, conferenceID uint32, userID uint16, floorID uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log.Infow("🖥️ [ScreenShare] Setting up BFCP client",
		"serverAddr", serverAddr,
		"conferenceID", conferenceID,
		"userID", userID,
		"floorID", floorID,
	)

	s.bfcpServerAddr = serverAddr
	s.bfcpConferenceID = conferenceID
	s.bfcpUserID = userID
	s.bfcpFloorID = floorID

	// Create BFCP client using real library
	config := &bfcp.ClientConfig{
		ServerAddress:  serverAddr,
		ConferenceID:   conferenceID,
		UserID:         userID,
		EnableLogging:  true,
		ConnectTimeout: 10 * time.Second,
	}
	s.bfcpClient = bfcp.NewClient(config)

	// Set up event callbacks
	s.bfcpClient.OnConnected = func() {
		s.log.Infow("🖥️ [BFCP] ✅ Connected to BFCP server")
	}

	s.bfcpClient.OnDisconnected = func() {
		s.log.Infow("🖥️ [BFCP] ⚠️ Disconnected from BFCP server")
	}

	s.bfcpClient.OnFloorGranted = func(floorID, requestID uint16) {
		s.mu.Lock()
		s.floorGranted = true
		s.bfcpRequestID = requestID
		s.mu.Unlock()
		s.log.Infow("🖥️ [BFCP] ✅ Floor GRANTED", "floorID", floorID, "requestID", requestID)
		if s.onFloorGranted != nil {
			go s.onFloorGranted()
		}
	}

	s.bfcpClient.OnFloorDenied = func(floorID, requestID uint16, errorCode bfcp.ErrorCode) {
		s.log.Warnw("🖥️ [BFCP] ❌ Floor DENIED", nil, "floorID", floorID, "requestID", requestID, "errorCode", errorCode)
	}

	s.bfcpClient.OnFloorRevoked = func(floorID uint16) {
		s.mu.Lock()
		s.floorGranted = false
		s.mu.Unlock()
		s.log.Infow("🖥️ [BFCP] ⚠️ Floor REVOKED", "floorID", floorID)
		if s.onFloorRevoked != nil {
			go s.onFloorRevoked()
		}
	}

	s.bfcpClient.OnFloorReleased = func(floorID uint16) {
		s.mu.Lock()
		s.floorGranted = false
		s.mu.Unlock()
		s.log.Infow("🖥️ [BFCP] Floor RELEASED", "floorID", floorID)
	}

	s.bfcpClient.OnError = func(err error) {
		s.log.Errorw("🖥️ [BFCP] Error", err)
	}

	// Connect to BFCP server
	if err := s.connectBFCP(ctx); err != nil {
		return fmt.Errorf("failed to connect to BFCP server: %w", err)
	}

	s.log.Infow("🖥️ [ScreenShare] ✅ BFCP client setup complete")
	return nil
}

// connectBFCP establishes connection to BFCP server
func (s *ScreenShareManager) connectBFCP(ctx context.Context) error {
	if s.bfcpClient == nil {
		return fmt.Errorf("BFCP client not initialized")
	}

	s.log.Infow("🖥️ [ScreenShare] Connecting to BFCP server", "addr", s.bfcpServerAddr)

	// Connect to BFCP server
	if err := s.bfcpClient.Connect(); err != nil {
		return fmt.Errorf("BFCP connection failed: %w", err)
	}

	s.log.Infow("🖥️ [ScreenShare] ✅ BFCP TCP connection established")

	// Send Hello message to establish BFCP session (optional - some servers don't support it)
	s.log.Infow("🖥️ [ScreenShare] Sending BFCP Hello...")
	if err := s.bfcpClient.Hello(); err != nil {
		s.log.Warnw("🖥️ [ScreenShare] ⚠️ BFCP Hello handshake failed (may not be supported by server)", err,
			"note", "Proceeding anyway - Hello/HelloAck is optional per RFC 8855")
		// Don't fail - Hello is optional, server might only respond to FloorRequest
	} else {
		s.log.Infow("🖥️ [ScreenShare] ✅ BFCP Hello handshake complete")
	}

	s.log.Infow("🖥️ [ScreenShare] ✅ BFCP connection ready for floor requests")

	return nil
}

// RequestFloor requests the presentation floor via BFCP (with locking)
func (s *ScreenShareManager) RequestFloor(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requestFloorLocked(ctx)
}

// requestFloorLocked is the internal implementation of RequestFloor without locking
func (s *ScreenShareManager) requestFloorLocked(ctx context.Context) error {
	s.log.Infow("🖥️ [ScreenShare] [RequestFloor] Entry")

	if s.bfcpClient == nil {
		s.log.Errorw("🖥️ [ScreenShare] [RequestFloor] ❌ BFCP client is nil", nil)
		return fmt.Errorf("BFCP client not initialized")
	}

	if !s.bfcpClient.IsConnected() {
		s.log.Errorw("🖥️ [ScreenShare] [RequestFloor] ❌ BFCP client not connected", nil)
		return fmt.Errorf("BFCP client not connected")
	}

	s.log.Infow("🖥️ [ScreenShare] [RequestFloor] BFCP client status OK")

	if s.floorGranted {
		s.log.Debugw("🖥️ [ScreenShare] [RequestFloor] Floor already granted, skipping")
		return nil
	}

	s.log.Infow("🖥️ [ScreenShare] [RequestFloor] Requesting presentation floor via BFCP",
		"floorID", s.bfcpFloorID,
		"userID", s.bfcpUserID,
		"beneficiaryID", s.bfcpUserID,
		"serverAddr", s.bfcpServerAddr,
	)

	// Request floor using real BFCP library
	// beneficiaryID should match userID for requesting on behalf of self
	requestID, err := s.bfcpClient.RequestFloor(s.bfcpFloorID, s.bfcpUserID, bfcp.PriorityNormal)
	if err != nil {
		s.log.Errorw("🖥️ [ScreenShare] [RequestFloor] ❌ Floor request failed", err)
		return fmt.Errorf("floor request failed: %w", err)
	}

	s.bfcpRequestID = requestID
	s.log.Infow("🖥️ [ScreenShare] [RequestFloor] ✅ Floor request sent",
		"floorID", s.bfcpFloorID,
		"requestID", requestID,
	)

	// Note: Floor grant will be handled by OnFloorGranted callback
	// Don't set floorGranted here - wait for actual grant from server

	s.log.Infow("🖥️ [ScreenShare] [RequestFloor] Exit - request pending (waiting for grant)")
	return nil
}

// ReleaseFloor releases the presentation floor via BFCP (with locking)
func (s *ScreenShareManager) ReleaseFloor(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releaseFloorLocked(ctx)
}

// releaseFloorLocked is the internal implementation of ReleaseFloor without locking
func (s *ScreenShareManager) releaseFloorLocked(ctx context.Context) error {
	if s.bfcpClient == nil || !s.bfcpClient.IsConnected() {
		return fmt.Errorf("BFCP client not connected")
	}

	if !s.floorGranted {
		s.log.Debugw("🖥️ [ScreenShare] Floor not granted, nothing to release")
		return nil
	}

	s.log.Infow("🖥️ [ScreenShare] Releasing presentation floor via BFCP",
		"floorID", s.bfcpFloorID,
		"requestID", s.bfcpRequestID,
	)

	// Release floor using real BFCP library
	if err := s.bfcpClient.ReleaseFloor(s.bfcpFloorID); err != nil {
		s.log.Errorw("🖥️ [ScreenShare] Floor release failed", err)
		return fmt.Errorf("floor release failed: %w", err)
	}

	s.floorGranted = false
	s.log.Infow("🖥️ [ScreenShare] ✅ Presentation floor RELEASED", "floorID", s.bfcpFloorID)

	return nil
}

// OnScreenShareTrack is called when a screen share track is detected from WebRTC
func (s *ScreenShareManager) OnScreenShareTrack(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log.Infow("🖥️ [ScreenShare] Screen share track detected",
		"participant", rp.Identity(),
		"trackID", track.ID(),
		"trackName", pub.Name(),
		"streamID", track.StreamID(),
	)

	s.screenTrack = track
	s.screenPub = pub
	s.screenParticip = rp

	// Start the screen share flow (no lock - we already hold it)
	if err := s.startLocked(); err != nil {
		return fmt.Errorf("failed to start screen share: %w", err)
	}

	return nil
}

// Start activates the screen share pipeline and requests floor (with locking)
func (s *ScreenShareManager) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked()
}

// startLocked is the internal implementation of Start without locking
// PHASE 1: Trigger re-INVITE async, pipeline setup happens in Phase 2 after 200 OK
func (s *ScreenShareManager) startLocked() error {
	if s.active {
		s.log.Debugw("🖥️ [ScreenShare] Already active")
		return nil
	}

	s.log.Infow("🖥️ [ScreenShare] ===== PHASE 1: Starting re-INVITE for screen share (async) =====")

	// Trigger re-INVITE to add screen share stream to SDP (async, non-blocking)
	s.log.Infow("🖥️ [ScreenShare] Sending SIP re-INVITE to add screen share m-line")
	if s.onStartCallback != nil {
		s.log.Infow("🖥️ [ScreenShare] Invoking re-INVITE callback (async, non-blocking)")
		// Launch re-INVITE in background goroutine
		// After 200 OK is received, inbound.go will call SetupPipelineAfterReInvite()
		go func() {
			if err := s.onStartCallback(); err != nil {
				s.log.Errorw("🖥️ [ScreenShare] ❌ re-INVITE callback failed", err)
			} else {
				s.log.Infow("🖥️ [ScreenShare] ✅ re-INVITE sent, waiting for 200 OK to trigger Phase 2")
			}
		}()
		s.log.Infow("🖥️ [ScreenShare] re-INVITE launched in background, not blocking track detection")
	} else {
		s.log.Warnw("🖥️ [ScreenShare] ⚠️ No re-INVITE callback registered", nil)
		return fmt.Errorf("no re-INVITE callback registered")
	}

	s.log.Infow("🖥️ [ScreenShare] ===== ✅ PHASE 1 complete (non-blocking): Waiting for 200 OK to trigger Phase 2 =====")
	return nil
}

// SetupPipelineAfterReInvite is PHASE 2: Called after re-INVITE 200 OK with real port
// This completes the screen share setup with actual destination port from SDP answer
func (s *ScreenShareManager) SetupPipelineAfterReInvite(media *sdpv2.SDPMedia) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log.Infow("🖥️ [ScreenShare] ===== PHASE 2: Setting up pipeline after re-INVITE 200 OK =====",
		"rtpPort", media.Port,
		"rtcpPort", media.RTCPPort)

	// Request BFCP floor BEFORE setting up pipeline (Poly requires floor grant first)
	if s.bfcpClient != nil && s.bfcpClient.IsConnected() {
		s.log.Infow("🖥️ [ScreenShare] Step 1/4: Requesting BFCP floor control")
		ctx := context.Background()
		if err := s.requestFloorLocked(ctx); err != nil {
			s.log.Errorw("🖥️ [ScreenShare] ❌ Failed to request floor", err)
			return fmt.Errorf("failed to request BFCP floor: %w", err)
		}

		// Wait for floor grant (with timeout)
		// IMPORTANT: Release lock while waiting to allow OnFloorGranted callback to run
		s.log.Infow("🖥️ [ScreenShare] Waiting for floor grant from Poly...")
		s.mu.Unlock()

		grantTimeout := time.NewTimer(10 * time.Second)
		defer grantTimeout.Stop()

		checkInterval := time.NewTicker(100 * time.Millisecond)
		defer checkInterval.Stop()

		var floorGranted bool
		for !floorGranted {
			select {
			case <-grantTimeout.C:
				s.mu.Lock() // Re-acquire lock before returning
				s.log.Errorw("🖥️ [ScreenShare] ❌ Timeout waiting for floor grant", nil)
				return fmt.Errorf("timeout waiting for BFCP floor grant")
			case <-checkInterval.C:
				s.mu.Lock()
				floorGranted = s.floorGranted
				s.mu.Unlock()
				if floorGranted {
					s.log.Infow("🖥️ [ScreenShare] ✅ Floor granted! Proceeding with screen share setup")
				}
			}
		}

		s.mu.Lock() // Re-acquire lock to continue with pipeline setup
	} else {
		s.log.Warnw("🖥️ [ScreenShare] ⚠️ BFCP client not available, proceeding without floor control", nil)
	}

	// Setup GStreamer pipeline with codec from SDP answer
	// Reuse video pipeline setup from video_pipeline.go (100% code reuse)
	s.log.Infow("🖥️ [ScreenShare] Step 2/4: Setting up GStreamer pipeline")
	if s.pipeline == nil {
		// Create a temporary VideoManager-like structure to reuse SetupGstPipeline
		// This reuses the exact same VP8→H264 transcoding pipeline as video
		vm := &VideoManager{
			VideoIO:  s.VideoIO,
			log:      s.log,
			pipeline: nil,
		}

		if err := vm.SetupGstPipeline(media); err != nil {
			return fmt.Errorf("failed to setup GStreamer pipeline: %w", err)
		}

		s.pipeline = vm.pipeline
		s.log.Infow("🖥️ [ScreenShare] ✅ GStreamer pipeline created using video_pipeline.go")
	} else {
		s.log.Infow("🖥️ [ScreenShare] Pipeline already exists")
	}

	// Connect track to pipeline
	s.log.Infow("🖥️ [ScreenShare] Step 3/4: Connecting track to pipeline")
	if s.screenTrack != nil {
		s.log.Infow("🖥️ [ScreenShare] Creating TrackInput",
			"trackID", s.screenTrack.ID(),
			"trackKind", s.screenTrack.Kind().String(),
			"codec", s.screenTrack.Codec().MimeType,
		)

		ti := NewTrackInput(s.screenTrack, s.screenPub, s.screenParticip, nil)

		if r := s.webrtcRtpIn.Swap(ti.RtpIn); r != nil {
			_ = r.Close()
		}
		if w := s.webrtcRtcpIn.Swap(ti.RtcpIn); w != nil {
			_ = w.Close()
		}

		// Connect RTCP writer to send PLI/FIR back to WebRTC track sender
		rtcpWriter := NewParticipantRtcpWriter(s.screenParticip, s.screenTrack)
		if w := s.webrtcRtcpOut.Swap(rtcpWriter); w != nil {
			_ = w.Close()
		}
		s.log.Infow("🖥️ [ScreenShare] RTCP writer connected for PLI/keyframe requests")

		s.log.Infow("🖥️ [ScreenShare] ✅ Screen share track connected to pipeline")
	} else {
		s.log.Warnw("🖥️ [ScreenShare] ⚠️ No screen share track available", nil)
	}

	// Connect UDP sockets with REAL destination port from SDP answer
	s.log.Infow("🖥️ [ScreenShare] Connecting UDP sockets with real destination ports")

	// Set REAL UDP destination from SDP answer (not port 0!)
	rtpAddr := netip.AddrPortFrom(s.remote, media.Port)
	s.rtpConn.SetDst(rtpAddr)
	rtcpAddr := netip.AddrPortFrom(s.remote, media.RTCPPort)
	s.rtcpConn.SetDst(rtcpAddr)

	s.log.Infow("🖥️ [ScreenShare] ✅ Set REAL UDP destination from SDP answer",
		"rtpDst", rtpAddr,
		"rtcpDst", rtcpAddr)

	// Connect SIP RTP output (GStreamer → UDP → Poly)
	if w := s.sipRtpOut.Swap(s.rtpConn); w != nil {
		s.log.Debugw("🖥️ [ScreenShare] Closing previous SIP RTP out writer")
		_ = w.Close()
	}
	s.log.Infow("🖥️ [ScreenShare] ✅ SIP RTP output connected to UDP socket", "port", s.rtpConn.LocalAddr())

	// Connect SIP RTCP output (for sending RTCP packets to Poly)
	if w := s.sipRtcpOut.Swap(s.rtcpConn); w != nil {
		s.log.Debugw("🖥️ [ScreenShare] Closing previous SIP RTCP out writer")
		_ = w.Close()
	}
	s.log.Infow("🖥️ [ScreenShare] ✅ SIP RTCP output connected to UDP socket", "port", s.rtcpConn.LocalAddr())

	// Connect SIP RTCP input (for receiving RTCP from Poly)
	if r := s.sipRtcpIn.Swap(s.rtcpConn); r != nil {
		s.log.Debugw("🖥️ [ScreenShare] Closing previous SIP RTCP in reader")
		_ = r.Close()
	}
	s.log.Infow("🖥️ [ScreenShare] ✅ SIP RTCP input connected to UDP socket")

	// Start GStreamer pipeline
	s.log.Infow("🖥️ [ScreenShare] Step 4/4: Starting GStreamer pipeline")
	if s.pipeline != nil {
		s.log.Infow("🖥️ [ScreenShare] Setting pipeline state to PLAYING")
		if err := s.pipeline.SetState(gst.StatePlaying); err != nil {
			s.log.Errorw("🖥️ [ScreenShare] ❌ Failed to start GStreamer pipeline", err)
			return fmt.Errorf("failed to set GStreamer pipeline to playing: %w", err)
		}
		s.log.Infow("🖥️ [ScreenShare] ✅ GStreamer pipeline started and PLAYING")
	} else {
		s.log.Errorw("🖥️ [ScreenShare] ❌ Pipeline still nil after setup!", nil)
		return fmt.Errorf("pipeline is nil after setup")
	}

	// Request keyframe after pipeline is playing
	if s.screenTrack != nil {
		s.log.Infow("🖥️ [ScreenShare] Requesting initial keyframe from WebRTC")
		// Send PLI (Picture Loss Indication) to request keyframe
		if s.webrtcRtcpOut != nil {
			pli := &rtcp.PictureLossIndication{SenderSSRC: 0, MediaSSRC: 0}
			if pliBytes, err := pli.Marshal(); err == nil {
				s.webrtcRtcpOut.Write(pliBytes)
			}
		}
	}

	s.active = true
	s.log.Infow("🖥️ [ScreenShare] Marked as active")

	s.log.Infow("🖥️ [ScreenShare] ===== ✅ PHASE 2 complete: Screen share FULLY ACTIVE =====")
	return nil
}

// Stop deactivates the screen share pipeline and releases floor
func (s *ScreenShareManager) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.active {
		s.log.Debugw("🖥️ [ScreenShare] Not active")
		return nil
	}

	s.log.Infow("🖥️ [ScreenShare] Stopping screen share")

	// Stop GStreamer pipeline
	if s.pipeline != nil {
		if err := s.pipeline.SetState(gst.StateNull); err != nil {
			s.log.Errorw("🖥️ [ScreenShare] Failed to stop GStreamer pipeline", err)
		}
	}

	// Disconnect UDP sockets from VideoIO
	s.log.Infow("🖥️ [ScreenShare] Disconnecting UDP sockets from video pipeline")

	if w := s.sipRtpOut.Swap(nil); w != nil {
		s.log.Debugw("🖥️ [ScreenShare] Closing SIP RTP out writer")
		_ = w.Close()
	}

	if w := s.sipRtcpOut.Swap(nil); w != nil {
		s.log.Debugw("🖥️ [ScreenShare] Closing SIP RTCP out writer")
		_ = w.Close()
	}

	if r := s.sipRtcpIn.Swap(nil); r != nil {
		s.log.Debugw("🖥️ [ScreenShare] Closing SIP RTCP in reader")
		_ = r.Close()
	}

	// Release BFCP floor (only needed for SIP→WebRTC, but keep for symmetry)
	ctx := context.Background()
	if err := s.releaseFloorLocked(ctx); err != nil {
		// Don't log error if client not connected (expected for WebRTC→SIP)
		if s.bfcpClient != nil && s.bfcpClient.IsConnected() {
			s.log.Errorw("🖥️ [ScreenShare] Failed to release floor", err)
		}
	}

	s.active = false
	s.screenTrack = nil
	s.screenPub = nil
	s.screenParticip = nil

	// Trigger re-INVITE to remove screen share stream from SDP
	if s.onStopCallback != nil {
		s.log.Infow("🖥️ [ScreenShare] Triggering re-INVITE to remove screen share stream")
		go func() {
			if err := s.onStopCallback(); err != nil {
				s.log.Errorw("🖥️ [ScreenShare] re-INVITE failed", err)
			}
		}()
	}

	s.log.Infow("🖥️ [ScreenShare] Screen share STOPPED")
	return nil
}

// IsActive returns whether screen share is currently active
func (s *ScreenShareManager) IsActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

// Close cleans up the screen share manager
func (s *ScreenShareManager) Close() error {
	s.log.Debugw("🖥️ [ScreenShare] Closing ScreenShareManager")

	if err := s.Stop(); err != nil {
		s.log.Errorw("🖥️ [ScreenShare] Error stopping screen share", err)
	}

	if s.pipeline != nil {
		if err := s.pipeline.SetState(gst.StateNull); err != nil {
			return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
		}
	}

	if err := s.VideoIO.Close(); err != nil {
		return fmt.Errorf("failed to close video IO: %w", err)
	}

	// Close RTP connection with idempotency check to prevent double-close
	if s.rtpConn != nil {
		if err := s.rtpConn.Close(); err != nil {
			// Only log as debug if already closed, not as error
			if !strings.Contains(err.Error(), "use of closed network connection") {
				return fmt.Errorf("failed to close RTP connection: %w", err)
			}
			s.log.Debugw("🖥️ [ScreenShare] RTP connection already closed")
		}
		s.rtpConn = nil
	}

	// Close RTCP connection with idempotency check to prevent double-close
	if s.rtcpConn != nil {
		if err := s.rtcpConn.Close(); err != nil {
			// Only log as debug if already closed, not as error
			if !strings.Contains(err.Error(), "use of closed network connection") {
				return fmt.Errorf("failed to close RTCP connection: %w", err)
			}
			s.log.Debugw("🖥️ [ScreenShare] RTCP connection already closed")
		}
		s.rtcpConn = nil
	}

	// Disconnect BFCP client
	if s.bfcpClient != nil && s.bfcpClient.IsConnected() {
		if err := s.bfcpClient.Disconnect(); err != nil {
			s.log.Errorw("🖥️ [ScreenShare] Failed to disconnect BFCP client", err)
		}
	}

	s.log.Infow("🖥️ [ScreenShare] ScreenShareManager closed")
	return nil
}

// SetOnStartCallback sets the callback to trigger re-INVITE when screen share starts
func (s *ScreenShareManager) SetOnStartCallback(cb func() error) {
	s.onStartCallback = cb
}

// SetOnStopCallback sets the callback to trigger re-INVITE when screen share stops
func (s *ScreenShareManager) SetOnStopCallback(cb func() error) {
	s.onStopCallback = cb
}

// SetOnFloorGrantedCallback sets the callback when floor is granted
func (s *ScreenShareManager) SetOnFloorGrantedCallback(cb func()) {
	s.onFloorGranted = cb
}

// SetOnFloorRevokedCallback sets the callback when floor is revoked
func (s *ScreenShareManager) SetOnFloorRevokedCallback(cb func()) {
	s.onFloorRevoked = cb
}

// IsScreenShareTrack determines if a track is a screen share track
func IsScreenShareTrack(pub *lksdk.RemoteTrackPublication) bool {
	// Check if track source is screen share
	if pub.Source() == livekit.TrackSource_SCREEN_SHARE {
		return true
	}

	// Fallback: check track name for common screen share patterns
	name := pub.Name()
	return name == "screen" || name == "screenshare" || name == "presentation"
}
