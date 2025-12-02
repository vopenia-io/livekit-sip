package sip

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"

	lkbfcp "github.com/livekit/sip/pkg/bfcp"
)

type MediaOrchestrator struct {
	mu          sync.Mutex
	opts        *MediaOptions
	log         logger.Logger
	inbound     *sipInbound
	room        *Room
	camera      *CameraManager
	screenshare *ScreenshareManager

	// BFCP floor control
	bfcpManager *lkbfcp.Manager
	bfcpInfo    *sdpv2.SDPBfcp

	// Callback for SIP re-INVITE when content negotiation is needed.
	// Called when WebRTC screenshare track arrives and screenshare not yet set up.
	// Returns the full SDP from the SIP device's 200 OK response.
	OnReInviteNeeded func(ctx context.Context) (answer *sdpv2.SDP, err error)
}

func NewMediaOrchestrator(log logger.Logger, inbound *sipInbound, room *Room, opts *MediaOptions) (*MediaOrchestrator, error) {
	o := &MediaOrchestrator{
		log:     log,
		inbound: inbound,
		room:    room,
		opts:    opts,
	}

	camera, err := NewCameraManager(log.WithComponent("camera"), room, opts)
	if err != nil {
		return nil, fmt.Errorf("could not create video manager: %w", err)
	}
	o.camera = camera
	o.room.OnCameraTrack(func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		ti := NewTrackInput(track, pub, rp)
		o.camera.WebrtcTrackInput(ti, rp.SID(), uint32(track.SSRC()))
	})

	screenshare, err := NewScreenshareManager(log.WithComponent("screenshare"), room, opts)
	if err != nil {
		return nil, fmt.Errorf("could not create screenshare manager: %w", err)
	}
	o.screenshare = screenshare

	// Wire up screenshare lifecycle callbacks for BFCP floor control
	o.screenshare.OnScreenshareStarted = func() {
		o.mu.Lock()
		bfcpMgr := o.bfcpManager
		o.mu.Unlock()

		if bfcpMgr != nil {
			o.log.Infow("WebRTC screenshare started - requesting BFCP floor for virtual client")
			if err := bfcpMgr.RequestFloorForVirtualClient(); err != nil {
				o.log.Errorw("Failed to request BFCP floor for virtual client", err)
			}
		} else {
			o.log.Debugw("WebRTC screenshare started but no BFCP manager available")
		}
	}
	o.screenshare.OnScreenshareStopped = func() {
		o.mu.Lock()
		bfcpMgr := o.bfcpManager
		o.mu.Unlock()

		if bfcpMgr != nil {
			o.log.Infow("WebRTC screenshare stopped - releasing BFCP floor for virtual client")
			if err := bfcpMgr.ReleaseFloorForVirtualClient(); err != nil {
				o.log.Errorw("Failed to release BFCP floor for virtual client", err)
			}
		} else {
			o.log.Debugw("WebRTC screenshare stopped but no BFCP manager available")
		}
	}

	o.room.OnScreenshareTrack(func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		o.log.Infow("WebRTC screenshare track received",
			"participant", rp.Identity(),
			"trackID", track.ID(),
			"ssrc", track.SSRC(),
		)

		// Check if screenshare manager is already set up
		if o.screenshare.IsReady() {
			ti := NewTrackInput(track, pub, rp)
			o.screenshare.WebrtcTrackInput(ti, rp.SID(), uint32(track.SSRC()))
			return
		}

		// Screenshare not set up - need to send re-INVITE to negotiate content port
		if o.OnReInviteNeeded == nil {
			o.log.Warnw("WebRTC screenshare track received but no re-INVITE callback configured", nil)
			return
		}

		o.log.Infow("Screenshare not set up, triggering re-INVITE for content negotiation")

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// Send re-INVITE and get answer SDP with screenshare media
			answer, err := o.OnReInviteNeeded(ctx)
			if err != nil {
				o.log.Errorw("Re-INVITE for screenshare failed", err)
				return
			}

			if answer.Screenshare == nil || answer.Screenshare.Disabled {
				o.log.Warnw("Re-INVITE succeeded but no screenshare in answer", nil)
				return
			}

			// Prepare the screenshare media for sending (codec selection + direction reversal)
			if err := answer.Screenshare.PrepareForSending(); err != nil {
				o.log.Errorw("Failed to prepare screenshare for sending", err)
				return
			}
			// Log the negotiated codec details
			answeredPT := answer.Screenshare.Codec.PayloadType
			const offeredPT uint8 = 109 // Must match the PT offered in inbound.go OnReInviteNeeded
			o.log.Infow("Prepared screenshare for sending",
				"codec", answer.Screenshare.Codec.Name,
				"offeredPT", offeredPT,
				"answeredPT", answeredPT,
				"direction", answer.Screenshare.Direction,
			)
			if answeredPT != offeredPT {
				o.log.Warnw("Screenshare PT mismatch: SDP answer returned different PT than offered",
					nil,
					"offeredPT", offeredPT,
					"answeredPT", answeredPT,
					"usingPT", answeredPT, // We use the answered PT for RTP
				)
			}

			// Set up the screenshare manager with the negotiated media
			if err := o.screenshare.Setup(answer.Addr, answer.Screenshare); err != nil {
				o.log.Errorw("Failed to setup screenshare after re-INVITE", err)
				return
			}

			// Start the screenshare pipeline
			if err := o.screenshare.Start(); err != nil {
				o.log.Errorw("Failed to start screenshare after re-INVITE", err)
				return
			}

			o.log.Infow("Screenshare set up after re-INVITE, connecting WebRTC track",
				"contentPort", answer.Screenshare.Port,
			)

			// Now connect the WebRTC track
			ti := NewTrackInput(track, pub, rp)
			o.screenshare.WebrtcTrackInput(ti, rp.SID(), uint32(track.SSRC()))
		}()
	})

	o.room.OnActiveSpeakersChanged(func(p []lksdk.Participant) {
		if len(p) == 0 {
			o.log.Debugw("no active speakers found")
			return
		}
		sid := p[0].SID()
		if err := o.camera.SwitchActiveWebrtcTrack(sid); err != nil {
			o.log.Warnw("could not switch active webrtc track", err, "sid", sid)
		}
	})

	return o, nil
}

func (o *MediaOrchestrator) Close() error {
	o.StopBFCP()
	return errors.Join(o.camera.Close(), o.screenshare.Close())
}

func (o *MediaOrchestrator) AnswerSDP(offer *sdpv2.SDP) (*sdpv2.SDP, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if offer.Video != nil {
		if err := offer.Video.SelectCodec(); err != nil {
			return nil, fmt.Errorf("could not select video codec: %w", err)
		}
	}

	if offer.Screenshare != nil {
		if err := offer.Screenshare.SelectCodec(); err != nil {
			return nil, fmt.Errorf("could not select screenshare codec: %w", err)
		}
	}

	if err := o.setupSDP(offer); err != nil {
		return nil, fmt.Errorf("could not setup sdp: %w", err)
	}

	// Only include screenshare in initial answer if SIP device offered it.
	// Otherwise, defer screenshare negotiation to re-INVITE after BFCP floor grant.
	// This is required for Poly endpoints which ignore slides m-line in initial 200 OK.
	includeScreenshare := offer.Screenshare != nil
	answer, err := o.offerSDP(offer.Video != nil, includeScreenshare)
	if err != nil {
		return nil, fmt.Errorf("could not create answer sdp: %w", err)
	}

	return answer, nil
}

func (o *MediaOrchestrator) offerSDP(camera bool, screenshare bool) (*sdpv2.SDP, error) {
	builder := (&sdpv2.SDP{}).Builder()

	builder.SetAddress(o.opts.IP)

	if camera {
		builder.SetVideo(func(b *sdpv2.SDPMediaBuilder) (*sdpv2.SDPMedia, error) {
			codec := o.camera.Codec()
			if codec == nil {
				for _, c := range o.camera.SupportedCodecs() {
					b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
						return c, nil
					}, false)
				}
			} else {
				b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
					return codec, nil
				}, true)
			}
			b.SetDisabled(o.camera.Status() != VideoStatusStarted)
			b.SetRTPPort(uint16(o.camera.RtpPort()))
			b.SetRTCPPort(uint16(o.camera.RtcpPort()))
			b.SetDirection(o.camera.Direction())
			return b.Build()
		})
	}

	if screenshare {
		builder.SetScreenshare(func(b *sdpv2.SDPMediaBuilder) (*sdpv2.SDPMedia, error) {
			codec := o.screenshare.Codec()
			if codec == nil {
				for _, c := range o.screenshare.SupportedCodecs() {
					b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
						return c, nil
					}, false)
				}
			} else {
				b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
					return codec, nil
				}, true)
			}
			b.SetDisabled(o.screenshare.Status() != VideoStatusStarted)
			b.SetRTPPort(uint16(o.screenshare.RtpPort()))
			b.SetRTCPPort(uint16(o.screenshare.RtcpPort()))
			b.SetDirection(o.screenshare.Direction())
			return b.Build()
		})
	}

	offer, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("could create a new sdp: %w", err)
	}

	return offer, nil
}

func (o *MediaOrchestrator) OfferSDP() (*sdpv2.SDP, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.offerSDP(true, true)
}

// BuildReInviteSDPBytes builds and marshals a complete SDP for a re-INVITE that includes
// screenshare content negotiation with BFCP. This is specifically designed for Poly endpoints.
// Returns the raw SDP bytes ready to send in a SIP INVITE request.
func (o *MediaOrchestrator) BuildReInviteSDPBytes(screenshareMedia *sdpv2.SDPMedia) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Get camera codec - required for main video
	cameraCodec := o.camera.Codec()
	if cameraCodec == nil {
		return nil, errors.New("camera codec not negotiated")
	}

	o.log.Infow("Building re-INVITE SDP for screenshare",
		"localIP", o.opts.IP,
		"cameraPort", o.camera.RtpPort(),
		"cameraCodecPT", cameraCodec.PayloadType,
		"screensharePort", screenshareMedia.Port,
		"screenshareRTCPPort", screenshareMedia.RTCPPort,
		"screenshareLabel", screenshareMedia.Label,
	)

	// Build re-INVITE config using the media-sdk API
	cfg := sdpv2.NewReInviteConfigForPoly(o.opts.IP).
		WithVideo(cameraCodec, uint16(o.camera.RtpPort()), uint16(o.camera.RtcpPort()), o.camera.Direction()).
		WithScreenshare(screenshareMedia.Codec, screenshareMedia.Port, screenshareMedia.RTCPPort, sdpv2.DirectionSendOnly)

	// Add BFCP if available (required for Poly content sharing)
	if o.bfcpManager != nil && o.bfcpInfo != nil {
		bfcpPort := o.bfcpManager.Port()
		if bfcpPort > 0 {
			cfg.WithBFCP(
				bfcpPort,
				o.bfcpInfo.Proto,
				o.bfcpInfo.ConfID,
				o.bfcpInfo.UserID,
				o.bfcpInfo.FloorID,
				screenshareMedia.Label, // Link to screenshare label (typically 3)
			)
			o.log.Infow("Including BFCP in re-INVITE",
				"bfcpPort", bfcpPort,
				"floorID", o.bfcpInfo.FloorID,
				"mstreamID", screenshareMedia.Label,
			)
		}
	}

	// Build and marshal the SDP
	sdpBytes, err := cfg.Build()
	if err != nil {
		return nil, fmt.Errorf("build re-INVITE SDP: %w", err)
	}

	o.log.Infow("Generated re-INVITE SDP",
		"sdpLength", len(sdpBytes),
		"sdp", string(sdpBytes),
	)

	return sdpBytes, nil
}

func (o *MediaOrchestrator) setupSDP(sdp *sdpv2.SDP) error {
	if err := o.camera.Stop(); err != nil {
		return fmt.Errorf("could not stop video manager: %w", err)
	}
	if err := o.room.StopCamera(); err != nil {
		return fmt.Errorf("could not stop room camera: %w", err)
	}
	if err := o.screenshare.Stop(); err != nil {
		return fmt.Errorf("could not stop screenshare manager: %w", err)
	}

	if sdp.Video != nil && !sdp.Video.Disabled {
		if err := o.camera.Setup(sdp.Addr, sdp.Video); err != nil {
			return fmt.Errorf("could not setup video sdp: %w", err)
		}
	}

	// Setup screenshare if offered by SIP device.
	if sdp.Screenshare != nil && !sdp.Screenshare.Disabled {
		if err := o.screenshare.Setup(sdp.Addr, sdp.Screenshare); err != nil {
			return fmt.Errorf("could not setup screenshare sdp: %w", err)
		}
	}
	// Always store remote address for re-INVITE (triggered on BFCP floor grant).
	o.screenshare.SetRemoteAddr(sdp.Addr)

	return nil
}

// REMOVED: createDefaultScreenshareMedia - no longer needed since fallback mode is removed.
// Re-INVITE will negotiate the real content port when BFCP floor is granted.

func (o *MediaOrchestrator) SetupSDP(sdp *sdpv2.SDP) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.setupSDP(sdp)
}

func (o *MediaOrchestrator) Start() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.start()
}

func (o *MediaOrchestrator) start() error {
	if o.camera.Status() == VideoStatusReady {
		if err := o.camera.Start(); err != nil {
			return fmt.Errorf("could not start video manager: %w", err)
		}
		to, err := o.room.StartCamera()
		if err != nil {
			return fmt.Errorf("could not start room camera: %w", err)
		}
		o.camera.WebrtcTrackOutput(to)
	}

	if o.screenshare.Status() == VideoStatusReady {
		if err := o.screenshare.Start(); err != nil {
			return fmt.Errorf("could not start screenshare manager: %w", err)
		}
		// Note: screenshare doesn't need room track output since it's WebRTC→SIP only
		// The WebRTC track input comes via OnScreenshareTrack callback
	}

	return nil
}

// SetupBFCP configures BFCP from a parsed SDP and starts the BFCP server asynchronously.
// This is non-blocking - the server starts in a goroutine.
func (o *MediaOrchestrator) SetupBFCP(offer *sdpv2.SDP) {
	if offer.BFCP == nil {
		return // No BFCP in offer
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	o.bfcpInfo = offer.BFCP
	o.log.Debugw("BFCP offer detected",
		"port", o.bfcpInfo.Port,
		"proto", o.bfcpInfo.Proto,
		"setup", o.bfcpInfo.Setup,
		"connection", o.bfcpInfo.Connection,
		"floorctrl", o.bfcpInfo.FloorCtrl,
		"confid", o.bfcpInfo.ConfID,
		"userid", o.bfcpInfo.UserID,
		"floorid", o.bfcpInfo.FloorID,
		"mstreamid", o.bfcpInfo.MStreamID,
	)

	// Get sipCallID for logging correlation
	var sipCallID string
	if o.inbound != nil {
		sipCallID = o.inbound.SIPCallID()
	}

	// Create BFCP server config
	bfcpCfg := &lkbfcp.Config{
		ListenAddr:     ":0", // Let OS pick a port
		ConferenceID:   o.bfcpInfo.ConfID,
		ContentFloorID: o.bfcpInfo.FloorID,
		AutoGrant:      true, // Auto-grant for now (1:1 calls)
		SIPCallID:      sipCallID,
	}

	bfcpMgr, err := lkbfcp.NewManager(o.log, bfcpCfg)
	if err != nil {
		o.log.Warnw("Failed to create BFCP manager", err)
		return
	}
	o.bfcpManager = bfcpMgr

	// Set up callbacks for floor events
	o.bfcpManager.OnFloorGranted = func(floorID, userID uint16) {
		o.log.Infow("BFCP floor granted - screenshare can start",
			"floorID", floorID,
			"userID", userID,
		)
		o.screenshare.SetFloorHeld(true)
		// Note: re-INVITE is triggered by WebRTC screenshare track, not by BFCP floor grant
	}
	o.bfcpManager.OnFloorReleased = func(floorID, userID uint16) {
		o.log.Infow("BFCP floor released - screenshare stopped",
			"floorID", floorID,
			"userID", userID,
		)
		o.screenshare.SetFloorHeld(false)
	}

	// Start BFCP server synchronously - this binds the port immediately
	// so Port() returns the correct value for the SDP answer
	if err := o.bfcpManager.Start(); err != nil {
		o.log.Warnw("Failed to start BFCP manager", err)
		o.bfcpManager = nil
	} else {
		o.log.Infow("BFCP server started",
			"addr", o.bfcpManager.Addr(),
			"port", o.bfcpManager.Port(),
			"confID", o.bfcpInfo.ConfID,
			"floorID", o.bfcpInfo.FloorID,
		)
	}
}

// BFCPAnswerBytes returns the BFCP m-line to append to SDP answer, or nil if no BFCP.
func (o *MediaOrchestrator) BFCPAnswerBytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.bfcpManager == nil || o.bfcpInfo == nil {
		return nil
	}

	// Port is available immediately since Start() binds synchronously
	port := o.bfcpManager.Port()
	if port == 0 {
		o.log.Errorw("BFCP server port is 0 - server start may have failed", nil)
		return nil
	}

	bfcpAnswerCfg := &sdpv2.SDPBfcpAnswerConfig{
		Port: port,
	}
	bfcpAnswer := o.bfcpInfo.Answer(bfcpAnswerCfg)
	bfcpStr, err := bfcpAnswer.Marshal()
	if err != nil {
		o.log.Warnw("Failed to create BFCP answer", err)
		return nil
	}

	o.log.Debugw("BFCP answer created",
		"port", port,
		"setup", o.bfcpInfo.Setup.Reverse(),
		"floorctrl", o.bfcpInfo.FloorCtrl.Reverse(),
	)

	return []byte(bfcpStr)
}

// BFCPMediaStreamID returns the BFCP media stream ID (mstrm) for the content floor.
// This is used as the label attribute on the screenshare m-line to link with BFCP floorid.
func (o *MediaOrchestrator) BFCPMediaStreamID() uint16 {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.bfcpInfo == nil {
		return 0
	}
	return o.bfcpInfo.MStreamID
}

// StopBFCP stops the BFCP server if running.
func (o *MediaOrchestrator) StopBFCP() {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.bfcpManager != nil {
		if err := o.bfcpManager.Stop(); err != nil {
			o.log.Warnw("Failed to stop BFCP manager", err)
		}
		o.bfcpManager = nil
	}
}
