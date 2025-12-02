// Package bfcp provides BFCP (Binary Floor Control Protocol) integration for livekit-sip.
package bfcp

import (
	"fmt"
	"net"
	"strconv"
	"sync"

	"github.com/livekit/protocol/logger"
	"github.com/vopenia/bfcp"
)

// Config holds configuration for the BFCP Manager.
type Config struct {
	ListenAddr     string // TCP address for BFCP server (e.g., ":5070")
	ConferenceID   uint32 // BFCP conference ID
	ContentFloorID uint16 // Floor ID for screenshare/content (from SDP floorid)
	AutoGrant      bool   // Auto-grant floor requests (for 1:1 calls)
	SIPCallID      string // SIP Call-ID for logging correlation
}

// BFCPFloorState tracks the floor state for both WebRTC and SIP sides
type BFCPFloorState struct {
	WebRTCHasFloor bool // WebRTC/virtual client holds floor
	PolyHasFloor   bool // Poly/real BFCP client holds floor
}

// VirtualClientUserID is the user ID used for the virtual BFCP client
// representing WebRTC participants. This is used when WebRTC shares screen.
const VirtualClientUserID uint16 = 65534

// Manager wraps a BFCP server with livekit-sip integration.
type Manager struct {
	server *bfcp.Server
	config *Config
	log    logger.Logger

	mu      sync.RWMutex
	running bool

	// Track virtual client floor state
	virtualFloorHeld bool
	virtualRequestID uint16

	// Floor state tracking for logging
	floorState BFCPFloorState

	// Callbacks
	OnFloorGranted     func(floorID, userID uint16)
	OnFloorReleased    func(floorID, userID uint16)
	OnFloorDenied      func(floorID, userID uint16)
	OnClientConnect    func(remoteAddr string, userID uint16)
	OnClientDisconnect func(remoteAddr string, userID uint16)
}

// logBFCP logs BFCP events at Debug level with consistent context fields
func (m *Manager) logBFCP(msg string, fields ...interface{}) {
	base := []interface{}{
		"sipCallID", m.config.SIPCallID,
		"conferenceID", m.config.ConferenceID,
		"floorID", m.config.ContentFloorID,
	}
	m.log.Debugw(msg, append(base, fields...)...)
}

// logBFCPInfo logs BFCP events at Info level with consistent context fields
func (m *Manager) logBFCPInfo(msg string, fields ...interface{}) {
	base := []interface{}{
		"sipCallID", m.config.SIPCallID,
		"conferenceID", m.config.ConferenceID,
		"floorID", m.config.ContentFloorID,
	}
	m.log.Infow(msg, append(base, fields...)...)
}

// NewManager creates a new BFCP Manager.
func NewManager(log logger.Logger, cfg *Config) (*Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if cfg.ListenAddr == "" {
		return nil, fmt.Errorf("listen address is required")
	}

	m := &Manager{
		config: cfg,
		log:    log,
	}

	// Create BFCP server config
	serverCfg := bfcp.DefaultServerConfig(cfg.ListenAddr, cfg.ConferenceID)
	serverCfg.AutoGrant = cfg.AutoGrant
	serverCfg.EnableLogging = true

	m.server = bfcp.NewServer(serverCfg)

	// Create the content floor
	if cfg.ContentFloorID > 0 {
		m.server.CreateFloor(cfg.ContentFloorID)
		m.logBFCP("bfcp.floor.created")
	}

	// Set up server callbacks
	m.server.OnFloorRequest = m.handleFloorRequest
	m.server.OnFloorGranted = m.handleFloorGranted
	m.server.OnFloorReleased = m.handleFloorReleased
	m.server.OnFloorDenied = m.handleFloorDenied
	m.server.OnClientConnect = m.handleClientConnect
	m.server.OnClientDisconnect = m.handleClientDisconnect
	m.server.OnError = m.handleError

	// Set up message logging callbacks
	m.server.OnMessageIn = func(remote, primitive string, transactionID, conferenceID uint32, userID, floorID uint16) {
		m.logBFCPInfo("bfcp.msg.in",
			"remote", remote,
			"primitive", primitive,
			"transactionID", transactionID,
			"msgConferenceID", conferenceID,
			"userID", userID,
			"msgFloorID", floorID,
		)
	}
	m.server.OnMessageOut = func(remote, primitive string, transactionID, conferenceID uint32, userID, floorID uint16) {
		m.logBFCPInfo("bfcp.msg.out",
			"remote", remote,
			"primitive", primitive,
			"transactionID", transactionID,
			"msgConferenceID", conferenceID,
			"userID", userID,
			"msgFloorID", floorID,
		)
	}

	m.logBFCPInfo("bfcp.manager.created",
		"listenAddr", cfg.ListenAddr,
		"autoGrant", cfg.AutoGrant,
	)

	return m, nil
}

// Start starts the BFCP server synchronously (binds port) then starts
// accepting connections in a goroutine. The port is available immediately
// after Start() returns successfully.
func (m *Manager) Start() error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("BFCP manager already running")
	}
	m.mu.Unlock()

	m.logBFCPInfo("bfcp.server.listen",
		"addr", m.config.ListenAddr,
	)

	// Bind the port synchronously - this ensures Port() returns the correct value
	// immediately after Start() returns
	if err := m.server.Listen(); err != nil {
		return fmt.Errorf("BFCP server listen failed: %w", err)
	}

	m.mu.Lock()
	m.running = true
	m.mu.Unlock()

	// Log the actual bound address (important when using :0)
	m.logBFCPInfo("bfcp.server.bound",
		"boundAddr", m.server.Addr().String(),
		"port", m.Port(),
	)

	// Start accepting connections in background
	m.server.Serve()

	return nil
}

// Stop stops the BFCP server.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return nil
	}

	m.logBFCPInfo("bfcp.server.stop",
		"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
		"PolyHasFloor", m.floorState.PolyHasFloor,
	)

	// Log session summary before stopping
	m.logBFCPInfo("bfcp.session.summary",
		"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
		"PolyHasFloor", m.floorState.PolyHasFloor,
	)

	m.running = false
	return m.server.Close()
}

// GrantFloor grants the specified floor to a user.
func (m *Manager) GrantFloor(floorID, userID uint16) error {
	m.logBFCP("bfcp.floor.grant",
		"targetFloorID", floorID,
		"userID", userID,
	)
	return m.server.GrantFloor(floorID, userID)
}

// RevokeFloor revokes the specified floor.
func (m *Manager) RevokeFloor(floorID uint16) {
	m.logBFCP("bfcp.floor.revoke",
		"targetFloorID", floorID,
	)
	m.server.ReleaseFloor(floorID)
}

// CreateFloor creates a new floor with the given ID.
func (m *Manager) CreateFloor(floorID uint16) {
	m.server.CreateFloor(floorID)
	m.logBFCP("bfcp.floor.created",
		"targetFloorID", floorID,
	)
}

// Addr returns the server's listen address.
func (m *Manager) Addr() string {
	if m.server.Addr() != nil {
		return m.server.Addr().String()
	}
	return m.config.ListenAddr
}

// Port returns the server's listen port.
func (m *Manager) Port() uint16 {
	if addr := m.server.Addr(); addr != nil {
		// Extract port from net.Addr (format: "host:port")
		_, portStr, _ := net.SplitHostPort(addr.String())
		if port, err := strconv.ParseUint(portStr, 10, 16); err == nil {
			return uint16(port)
		}
	}
	return 0
}

// handleFloorRequest handles incoming floor requests from BFCP clients (Poly).
// Returns true to grant, false to deny.
func (m *Manager) handleFloorRequest(floorID, userID, requestID uint16) bool {
	m.logBFCPInfo("bfcp.poly.floor_request",
		"requestedFloorID", floorID,
		"userID", userID,
		"requestID", requestID,
		"autoGrant", m.config.AutoGrant,
		"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
		"PolyHasFloor", m.floorState.PolyHasFloor,
	)

	// Auto-grant if configured (for 1:1 calls)
	if m.config.AutoGrant {
		return true
	}

	// Otherwise deny by default (requires explicit grant)
	return false
}

// handleFloorGranted handles floor granted events from the BFCP server.
func (m *Manager) handleFloorGranted(floorID, userID, requestID uint16) {
	// Update floor state based on who was granted
	if userID == VirtualClientUserID {
		m.floorState.WebRTCHasFloor = true
	} else {
		m.floorState.PolyHasFloor = true
	}

	m.logBFCPInfo("bfcp.poly.floor_granted",
		"grantedFloorID", floorID,
		"userID", userID,
		"requestID", requestID,
		"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
		"PolyHasFloor", m.floorState.PolyHasFloor,
	)

	// Check if content is ready (both sides have floor)
	if m.floorState.WebRTCHasFloor && m.floorState.PolyHasFloor {
		m.logBFCPInfo("bfcp.content_ready",
			"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
			"PolyHasFloor", m.floorState.PolyHasFloor,
		)
	}

	if m.OnFloorGranted != nil {
		m.OnFloorGranted(floorID, userID)
	}
}

// handleFloorReleased handles floor released events.
func (m *Manager) handleFloorReleased(floorID, userID uint16) {
	// Update floor state based on who released
	if userID == VirtualClientUserID {
		m.floorState.WebRTCHasFloor = false
	} else {
		m.floorState.PolyHasFloor = false
	}

	m.logBFCPInfo("bfcp.poly.floor_released",
		"releasedFloorID", floorID,
		"userID", userID,
		"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
		"PolyHasFloor", m.floorState.PolyHasFloor,
	)

	// Check if content is stopped (neither side has floor)
	if !m.floorState.WebRTCHasFloor && !m.floorState.PolyHasFloor {
		m.logBFCPInfo("bfcp.content_stopped",
			"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
			"PolyHasFloor", m.floorState.PolyHasFloor,
		)
	}

	if m.OnFloorReleased != nil {
		m.OnFloorReleased(floorID, userID)
	}
}

// handleFloorDenied handles floor denied events.
func (m *Manager) handleFloorDenied(floorID, userID, requestID uint16) {
	m.logBFCPInfo("bfcp.poly.floor_denied",
		"deniedFloorID", floorID,
		"userID", userID,
		"requestID", requestID,
		"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
		"PolyHasFloor", m.floorState.PolyHasFloor,
	)

	if m.OnFloorDenied != nil {
		m.OnFloorDenied(floorID, userID)
	}
}

// handleClientConnect handles client connection events (Poly connects to our BFCP server).
func (m *Manager) handleClientConnect(remoteAddr string, userID uint16) {
	m.logBFCPInfo("bfcp.server.accept",
		"remote", remoteAddr,
		"userID", userID,
	)

	if m.OnClientConnect != nil {
		m.OnClientConnect(remoteAddr, userID)
	}
}

// handleClientDisconnect handles client disconnection events.
func (m *Manager) handleClientDisconnect(remoteAddr string, userID uint16) {
	// Update floor state - if Poly disconnects, they no longer have floor
	if userID != VirtualClientUserID {
		m.floorState.PolyHasFloor = false
	}

	m.logBFCPInfo("bfcp.server.close",
		"remote", remoteAddr,
		"userID", userID,
		"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
		"PolyHasFloor", m.floorState.PolyHasFloor,
	)

	if m.OnClientDisconnect != nil {
		m.OnClientDisconnect(remoteAddr, userID)
	}
}

// handleError handles server errors.
func (m *Manager) handleError(err error) {
	m.log.Errorw("bfcp.server.error", err,
		"sipCallID", m.config.SIPCallID,
		"conferenceID", m.config.ConferenceID,
		"floorID", m.config.ContentFloorID,
	)
}

// RequestFloorForVirtualClient requests the content floor on behalf of the
// virtual BFCP client (representing WebRTC participant starting screenshare).
// This simulates a FloorRequest from the virtual client and grants it.
func (m *Manager) RequestFloorForVirtualClient() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.virtualFloorHeld {
		m.logBFCP("bfcp.webrtc.floor_already_held",
			"userID", VirtualClientUserID,
		)
		return nil
	}

	m.logBFCPInfo("bfcp.webrtc.floor_requested",
		"userID", VirtualClientUserID,
		"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
		"PolyHasFloor", m.floorState.PolyHasFloor,
	)

	// Get the floor state machine
	floor, exists := m.server.GetFloor(m.config.ContentFloorID)
	if !exists {
		m.logBFCP("bfcp.floor.not_found_creating")
		floor = m.server.CreateFloor(m.config.ContentFloorID)
	}

	// Log current floor state
	m.logBFCP("bfcp.floor.state_before_request",
		"state", floor.GetState().String(),
		"owner", floor.GetOwner(),
		"isAvailable", floor.IsAvailable(),
		"isGranted", floor.IsGranted(),
	)

	// Request the floor for virtual client
	m.virtualRequestID++
	status, err := floor.Request(VirtualClientUserID, m.virtualRequestID, bfcp.PriorityNormal)
	if err != nil {
		m.log.Errorw("bfcp.webrtc.floor_request_failed", err,
			"sipCallID", m.config.SIPCallID,
			"conferenceID", m.config.ConferenceID,
			"floorID", m.config.ContentFloorID,
			"userID", VirtualClientUserID,
			"requestID", m.virtualRequestID,
		)
		return fmt.Errorf("floor request failed: %w", err)
	}

	m.logBFCPInfo("bfcp.webrtc.floor_request_status",
		"userID", VirtualClientUserID,
		"requestID", m.virtualRequestID,
		"status", status.String(),
	)

	// If pending, grant it (we are the server, auto-grant for virtual client)
	if status == bfcp.RequestStatusPending || status == bfcp.RequestStatusAccepted {
		if err := floor.Grant(); err != nil {
			m.log.Errorw("bfcp.webrtc.floor_grant_failed", err,
				"sipCallID", m.config.SIPCallID,
				"conferenceID", m.config.ConferenceID,
				"floorID", m.config.ContentFloorID,
				"userID", VirtualClientUserID,
			)
			return fmt.Errorf("floor grant failed: %w", err)
		}

		// Update floor state
		m.floorState.WebRTCHasFloor = true

		m.logBFCPInfo("bfcp.webrtc.floor_granted",
			"userID", VirtualClientUserID,
			"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
			"PolyHasFloor", m.floorState.PolyHasFloor,
		)

		// Invoke OnFloorGranted callback (triggers re-INVITE for content negotiation)
		if m.OnFloorGranted != nil {
			m.OnFloorGranted(m.config.ContentFloorID, VirtualClientUserID)
		}
	}

	m.virtualFloorHeld = true

	// Log floor state after grant
	m.logBFCP("bfcp.floor.state_after_grant",
		"state", floor.GetState().String(),
		"owner", floor.GetOwner(),
		"isAvailable", floor.IsAvailable(),
		"isGranted", floor.IsGranted(),
	)

	// Broadcast FloorStatus (not FloorRequestStatus) to all connected BFCP clients (Poly)
	// FloorStatus is a notification about floor state, not a response to a specific request.
	// Poly didn't create this request (userID=65534 is virtual), so sending FloorRequestStatus
	// would cause Poly to reject it with "Error-Info: Invalid".
	m.logBFCPInfo("bfcp.webrtc.broadcast_floor_state",
		"primitive", "FloorStatus",
		"status", "GRANTED",
		"beneficiaryUserID", VirtualClientUserID,
	)
	m.server.BroadcastFloorState(m.config.ContentFloorID, VirtualClientUserID, bfcp.RequestStatusGranted)

	return nil
}

// ReleaseFloorForVirtualClient releases the content floor held by the
// virtual BFCP client (when WebRTC screenshare stops).
func (m *Manager) ReleaseFloorForVirtualClient() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.virtualFloorHeld {
		m.logBFCP("bfcp.webrtc.floor_not_held")
		return nil
	}

	m.logBFCPInfo("bfcp.webrtc.floor_releasing",
		"userID", VirtualClientUserID,
		"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
		"PolyHasFloor", m.floorState.PolyHasFloor,
	)

	floor, exists := m.server.GetFloor(m.config.ContentFloorID)
	if !exists {
		m.logBFCP("bfcp.floor.not_found_for_release")
		m.virtualFloorHeld = false
		m.floorState.WebRTCHasFloor = false
		return nil
	}

	// Log current floor state
	m.logBFCP("bfcp.floor.state_before_release",
		"state", floor.GetState().String(),
		"owner", floor.GetOwner(),
	)

	// Release the floor
	if err := floor.Release(VirtualClientUserID); err != nil {
		m.log.Errorw("bfcp.webrtc.floor_release_failed", err,
			"sipCallID", m.config.SIPCallID,
			"conferenceID", m.config.ConferenceID,
			"floorID", m.config.ContentFloorID,
			"userID", VirtualClientUserID,
		)
		return fmt.Errorf("floor release failed: %w", err)
	}

	m.virtualFloorHeld = false
	m.floorState.WebRTCHasFloor = false

	m.logBFCPInfo("bfcp.webrtc.floor_released",
		"userID", VirtualClientUserID,
		"WebRTCHasFloor", m.floorState.WebRTCHasFloor,
		"PolyHasFloor", m.floorState.PolyHasFloor,
	)

	// Log floor state after release
	m.logBFCP("bfcp.floor.state_after_release",
		"state", floor.GetState().String(),
		"owner", floor.GetOwner(),
		"isAvailable", floor.IsAvailable(),
	)

	// Broadcast FloorStatus RELEASED to all connected BFCP clients (Poly)
	// Using FloorStatus (not FloorRequestStatus) for the same reason as grant.
	m.logBFCPInfo("bfcp.webrtc.broadcast_floor_state",
		"primitive", "FloorStatus",
		"status", "RELEASED",
		"beneficiaryUserID", VirtualClientUserID,
	)
	m.server.BroadcastFloorState(m.config.ContentFloorID, VirtualClientUserID, bfcp.RequestStatusReleased)

	return nil
}

// IsVirtualClientHoldingFloor returns true if the virtual client currently holds the floor.
func (m *Manager) IsVirtualClientHoldingFloor() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.virtualFloorHeld
}

// GetFloorState returns the current state of the content floor for debugging.
func (m *Manager) GetFloorState() (state string, owner uint16, isGranted bool) {
	floor, exists := m.server.GetFloor(m.config.ContentFloorID)
	if !exists {
		return "not_found", 0, false
	}
	return floor.GetState().String(), floor.GetOwner(), floor.IsGranted()
}
