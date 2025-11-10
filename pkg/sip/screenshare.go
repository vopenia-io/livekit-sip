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

	// Create BFCP client
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
		s.log.Infow("🖥️ [BFCP] ✅✅✅ Floor GRANTED callback fired - triggering re-INVITE ✅✅✅", "floorID", floorID, "requestID", requestID)

		// Trigger re-INVITE now that BFCP server granted floor control
		if s.onStartCallback != nil {
			s.log.Infow("🖥️ [BFCP] ➡️ Building re-INVITE due to GRANTED callback")
			go func() {
				if err := s.onStartCallback(); err != nil {
					s.log.Errorw("🖥️ [BFCP] ❌ re-INVITE callback failed", err)
				} else {
					s.log.Infow("🖥️ [BFCP] ✅ re-INVITE sent successfully after floor grant")
				}
			}()
		} else {
			s.log.Warnw("🖥️ [BFCP] ⚠️ No onStartCallback set - re-INVITE will NOT be sent!", nil)
		}

		// Call custom floor granted callback if set
		if s.onFloorGranted != nil {
			go s.onFloorGranted()
		}
	}

	s.bfcpClient.OnFloorDenied = func(floorID, requestID uint16, errorCode bfcp.ErrorCode) {
		s.log.Warnw("🖥️ [BFCP] ❌ Floor DENIED", nil, "floorID", floorID, "requestID", requestID, "errorCode", errorCode)
	}

	s.bfcpClient.OnFloorStatus = func(floorID uint16, status bfcp.RequestStatus) {
		statusStr := status.String()
		s.log.Infow("🖥️ [BFCP] 📊 Floor Status Update", "floorID", floorID, "status", statusStr,
			"note", func() string {
				// Note: status.String() returns "Pending", "Granted", "Denied", etc.
				switch statusStr {
				case "Pending":
					return "Request queued - waiting for grant"
				case "Granted":
					return "Floor granted - OnFloorGranted callback should fire next"
				case "Denied":
					return "Floor denied"
				case "Released":
					return "Floor released"
				case "Revoked":
					return "Floor revoked"
				default:
					return "Status: " + statusStr
				}
			}())
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

	// Send Hello message to establish BFCP session (optional per RFC 8855)
	s.log.Infow("🖥️ [ScreenShare] Sending BFCP Hello...")
	if err := s.bfcpClient.Hello(); err != nil {
		s.log.Warnw("🖥️ [ScreenShare] ⚠️ BFCP Hello handshake failed (may not be supported)", err,
			"note", "Proceeding anyway - Hello/HelloAck is optional per RFC 8855")
		// Hello is optional, server might only respond to FloorRequest
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

	// Request floor using BFCP library
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

	// Floor grant will be handled by OnFloorGranted callback

	s.log.Infow("🖥️ [ScreenShare] [RequestFloor] Request pending, waiting for grant")
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

	// Release floor using BFCP library
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

	// Start the screen share flow
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
// Requests BFCP floor first; re-INVITE triggered when floor is granted
func (s *ScreenShareManager) startLocked() error {
	if s.active {
		s.log.Debugw("🖥️ [ScreenShare] Already active")
		return nil
	}

	s.log.Infow("🖥️ [ScreenShare] Starting screen share flow: BFCP floor request → wait for grant → re-INVITE")

	// Request BFCP floor asynchronously (floor grant callback will trigger re-INVITE)
	if s.bfcpClient != nil && s.bfcpClient.IsConnected() {
		s.log.Infow("🖥️ [ScreenShare] Requesting BFCP floor control before re-INVITE")
		ctx := context.Background()
		go func() {
			if err := s.requestFloorLocked(ctx); err != nil {
				s.log.Errorw("🖥️ [ScreenShare] ❌ Failed to request floor", err)
			} else {
				s.log.Infow("🖥️ [ScreenShare] ✅ Floor request sent, waiting for grant callback to trigger re-INVITE")
			}
		}()
	} else {
		s.log.Warnw("🖥️ [ScreenShare] ⚠️ BFCP client not available, skipping floor request", nil)
		return fmt.Errorf("BFCP client not initialized or not connected")
	}

	s.log.Infow("🖥️ [ScreenShare] ✅ Floor request initiated, re-INVITE will be sent after grant")
	return nil
}

// SetupPipelineAfterReInvite completes screen share setup after re-INVITE 200 OK
// Uses actual destination port from SDP answer
func (s *ScreenShareManager) SetupPipelineAfterReInvite(media *sdpv2.SDPMedia) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log.Infow("🖥️ [ScreenShare] Setting up pipeline after re-INVITE 200 OK",
		"rtpPort", media.Port,
		"rtcpPort", media.RTCPPort)

	// Floor should already be granted at this point (granted before re-INVITE was sent)
	if s.floorGranted {
		s.log.Infow("🖥️ [ScreenShare] ✅ BFCP floor already granted (as expected)")
	} else {
		s.log.Warnw("🖥️ [ScreenShare] ⚠️ Floor not granted yet (unexpected)", nil)
	}

	// Setup GStreamer pipeline with codec from SDP answer
	s.log.Infow("🖥️ [ScreenShare] Step 1/4: Setting up GStreamer pipeline")
	if s.pipeline == nil {
		if err := s.SetupGstPipeline(media); err != nil {
			return fmt.Errorf("failed to setup GStreamer pipeline: %w", err)
		}
		s.log.Infow("🖥️ [ScreenShare] ✅ GStreamer pipeline created")
	} else {
		s.log.Infow("🖥️ [ScreenShare] Pipeline already exists")
	}

	// Connect track to pipeline
	s.log.Infow("🖥️ [ScreenShare] Step 2/4: Connecting track to pipeline")
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

		// Connect RTCP writer for PLI/FIR keyframe requests
		rtcpWriter := NewParticipantRtcpWriter(s.screenParticip, s.screenTrack)
		if w := s.webrtcRtcpOut.Swap(rtcpWriter); w != nil {
			_ = w.Close()
		}
		s.log.Infow("🖥️ [ScreenShare] RTCP writer connected for PLI/keyframe requests")

		s.log.Infow("🖥️ [ScreenShare] ✅ Screen share track connected to pipeline")
	} else {
		s.log.Warnw("🖥️ [ScreenShare] ⚠️ No screen share track available", nil)
	}

	// Connect UDP sockets with destination port from SDP answer
	s.log.Infow("🖥️ [ScreenShare] Connecting UDP sockets with destination ports")

	// Set UDP destination from SDP answer
	rtpAddr := netip.AddrPortFrom(s.remote, media.Port)
	s.rtpConn.SetDst(rtpAddr)
	rtcpAddr := netip.AddrPortFrom(s.remote, media.RTCPPort)
	s.rtcpConn.SetDst(rtcpAddr)

	s.log.Infow("🖥️ [ScreenShare] ✅ Set UDP destination from SDP answer",
		"rtpDst", rtpAddr,
		"rtcpDst", rtcpAddr)

	// Screen share is UNIDIRECTIONAL: WebRTC→SIP only (we send transcoded VP8→H.264 to SIP device)
	// We do NOT receive screen share from SIP device, so disable SIP→WebRTC direction
	// This is critical for pipeline preroll - unused branches must be disabled!

	// Connect SIP RTP output (GStreamer H.264 → UDP → SIP device)
	if w := s.sipRtpOut.Swap(s.rtpConn); w != nil {
		s.log.Debugw("🖥️ [ScreenShare] Closing previous SIP RTP out writer")
		_ = w.Close()
	}
	s.log.Infow("🖥️ [ScreenShare] ✅ SIP RTP output connected to UDP socket", "port", s.rtpConn.LocalAddr())

	// Connect SIP RTCP output for sending RTCP packets
	if w := s.sipRtcpOut.Swap(s.rtcpConn); w != nil {
		s.log.Debugw("🖥️ [ScreenShare] Closing previous SIP RTCP out writer")
		_ = w.Close()
	}
	s.log.Infow("🖥️ [ScreenShare] ✅ SIP RTCP output connected to UDP socket", "port", s.rtcpConn.LocalAddr())

	// DISABLE SIP→WebRTC direction (no screen share input from SIP device)
	// This prevents pipeline from waiting for data on the reverse path
	if r := s.sipRtpIn.Swap(nil); r != nil {
		s.log.Debugw("🖥️ [ScreenShare] Closing SIP RTP input (disabling SIP→WebRTC)")
		_ = r.Close()
	}
	if w := s.sipRtcpIn.Swap(nil); w != nil {
		s.log.Debugw("🖥️ [ScreenShare] Closing SIP RTCP input (disabling SIP→WebRTC)")
		_ = w.Close()
	}
	if w := s.webrtcRtpOut.Swap(nil); w != nil {
		s.log.Debugw("🖥️ [ScreenShare] Closing WebRTC RTP output (disabling SIP→WebRTC)")
		_ = w.Close()
	}
	s.log.Infow("🖥️ [ScreenShare] ✅ Disabled SIP→WebRTC direction (screen share is send-only)")

	// Send EOS (end-of-stream) to unused appsrc elements so pipeline doesn't wait for data
	if s.pipeline != nil {
		if sipRtpIn, err := s.pipeline.GetElementByName("sip_rtp_in"); err == nil {
			s.log.Infow("🖥️ [ScreenShare] Sending EOS to sip_rtp_in appsrc (unused direction)")
			sipRtpIn.SendEvent(gst.NewEOSEvent())
		}
		if webrtcRtpOut, err := s.pipeline.GetElementByName("webrtc_rtp_out"); err == nil {
			s.log.Infow("🖥️ [ScreenShare] Sending EOS to webrtc_rtp_out appsink (unused direction)")
			webrtcRtpOut.SendEvent(gst.NewEOSEvent())
		}
	}

	// Start GStreamer pipeline
	s.log.Infow("🖥️ [ScreenShare] Step 3/4: Starting GStreamer pipeline")
	if s.pipeline != nil {
		s.log.Infow("🖥️ [ScreenShare] Setting pipeline state to PLAYING")
		if err := s.pipeline.SetState(gst.StatePlaying); err != nil {
			s.log.Errorw("🖥️ [ScreenShare] ❌ Failed to start GStreamer pipeline", err)
			return fmt.Errorf("failed to set GStreamer pipeline to playing: %w", err)
		}
		s.log.Infow("🖥️ [ScreenShare] ✅ GStreamer pipeline SetState(PLAYING) called")

		// IMPORTANT: Don't wait for PLAYING state here with a long timeout
		// The pipeline will transition from PAUSED→PLAYING automatically once data flows
		// Waiting with a long timeout blocks the re-INVITE flow and causes issues
		// Instead, check with a short timeout just to detect failures
		changeReturn, currentState := s.pipeline.GetState(gst.StatePlaying, gst.ClockTime(100*time.Millisecond))
		if changeReturn == gst.StateChangeFailure {
			s.log.Errorw("🖥️ [ScreenShare] ❌ Pipeline failed to start", nil)
			return fmt.Errorf("GStreamer pipeline failed to start")
		}

		s.log.Infow("🖥️ [ScreenShare] Pipeline state after SetState",
			"changeReturn", changeReturn,
			"currentState", currentState,
			"note", "Pipeline will reach PLAYING once data flows (ASYNC is normal)")
	} else {
		s.log.Errorw("🖥️ [ScreenShare] ❌ Pipeline still nil after setup!", nil)
		return fmt.Errorf("pipeline is nil after setup")
	}

	// Request keyframe after pipeline is playing
	s.log.Infow("🖥️ [ScreenShare] Step 4/4: Requesting initial keyframe from WebRTC")
	if s.screenTrack != nil {
		// Send PLI to request keyframe
		if s.webrtcRtcpOut != nil {
			pli := &rtcp.PictureLossIndication{SenderSSRC: 0, MediaSSRC: 0}
			if pliBytes, err := pli.Marshal(); err == nil {
				s.webrtcRtcpOut.Write(pliBytes)
			}
		}
	}

	s.active = true
	s.log.Infow("🖥️ [ScreenShare] Marked as active")

	s.log.Infow("🖥️ [ScreenShare] ✅ Screen share fully active")
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

	// Release BFCP floor
	ctx := context.Background()
	if err := s.releaseFloorLocked(ctx); err != nil {
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

	// Close RTP connection
	if s.rtpConn != nil {
		if err := s.rtpConn.Close(); err != nil {
			if !strings.Contains(err.Error(), "use of closed network connection") {
				return fmt.Errorf("failed to close RTP connection: %w", err)
			}
			s.log.Debugw("🖥️ [ScreenShare] RTP connection already closed")
		}
		s.rtpConn = nil
	}

	// Close RTCP connection
	if s.rtcpConn != nil {
		if err := s.rtcpConn.Close(); err != nil {
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
