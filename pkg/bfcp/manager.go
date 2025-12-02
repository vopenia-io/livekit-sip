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

	// Callbacks
	OnFloorGranted     func(floorID, userID uint16)
	OnFloorReleased    func(floorID, userID uint16)
	OnFloorDenied      func(floorID, userID uint16)
	OnClientConnect    func(remoteAddr string, userID uint16)
	OnClientDisconnect func(remoteAddr string, userID uint16)
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
		m.log.Debugw("BFCP floor created",
			"floorID", cfg.ContentFloorID,
			"conferenceID", cfg.ConferenceID,
		)
	}

	// Set up server callbacks
	m.server.OnFloorRequest = m.handleFloorRequest
	m.server.OnFloorGranted = m.handleFloorGranted
	m.server.OnFloorReleased = m.handleFloorReleased
	m.server.OnFloorDenied = m.handleFloorDenied
	m.server.OnClientConnect = m.handleClientConnect
	m.server.OnClientDisconnect = m.handleClientDisconnect
	m.server.OnError = m.handleError

	m.log.Infow("BFCP manager created",
		"listenAddr", cfg.ListenAddr,
		"conferenceID", cfg.ConferenceID,
		"contentFloorID", cfg.ContentFloorID,
		"autoGrant", cfg.AutoGrant,
	)

	return m, nil
}

// Start starts the BFCP server in a goroutine.
func (m *Manager) Start() error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("BFCP manager already running")
	}
	m.running = true
	m.mu.Unlock()

	m.log.Infow("BFCP server starting",
		"listenAddr", m.config.ListenAddr,
		"conferenceID", m.config.ConferenceID,
	)

	go func() {
		if err := m.server.ListenAndServe(); err != nil {
			m.log.Errorw("BFCP server error", err,
				"listenAddr", m.config.ListenAddr,
			)
		}
	}()

	return nil
}

// Stop stops the BFCP server.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return nil
	}

	m.log.Infow("BFCP server stopping",
		"listenAddr", m.config.ListenAddr,
	)

	m.running = false
	return m.server.Close()
}

// GrantFloor grants the specified floor to a user.
func (m *Manager) GrantFloor(floorID, userID uint16) error {
	m.log.Debugw("BFCP granting floor",
		"floorID", floorID,
		"userID", userID,
	)
	return m.server.GrantFloor(floorID, userID)
}

// RevokeFloor revokes the specified floor.
func (m *Manager) RevokeFloor(floorID uint16) {
	m.log.Debugw("BFCP revoking floor",
		"floorID", floorID,
	)
	m.server.ReleaseFloor(floorID)
}

// CreateFloor creates a new floor with the given ID.
func (m *Manager) CreateFloor(floorID uint16) {
	m.server.CreateFloor(floorID)
	m.log.Debugw("BFCP floor created",
		"floorID", floorID,
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

// handleFloorRequest handles incoming floor requests.
// Returns true to grant, false to deny.
func (m *Manager) handleFloorRequest(floorID, userID, requestID uint16) bool {
	m.log.Infow("BFCP floor request received",
		"floorID", floorID,
		"userID", userID,
		"requestID", requestID,
		"autoGrant", m.config.AutoGrant,
	)

	// Auto-grant if configured (for 1:1 calls)
	if m.config.AutoGrant {
		return true
	}

	// Otherwise deny by default (requires explicit grant)
	return false
}

// handleFloorGranted handles floor granted events.
func (m *Manager) handleFloorGranted(floorID, userID, requestID uint16) {
	m.log.Infow("BFCP floor granted",
		"floorID", floorID,
		"userID", userID,
		"requestID", requestID,
	)

	if m.OnFloorGranted != nil {
		m.OnFloorGranted(floorID, userID)
	}
}

// handleFloorReleased handles floor released events.
func (m *Manager) handleFloorReleased(floorID, userID uint16) {
	m.log.Infow("BFCP floor released",
		"floorID", floorID,
		"userID", userID,
	)

	if m.OnFloorReleased != nil {
		m.OnFloorReleased(floorID, userID)
	}
}

// handleFloorDenied handles floor denied events.
func (m *Manager) handleFloorDenied(floorID, userID, requestID uint16) {
	m.log.Infow("BFCP floor denied",
		"floorID", floorID,
		"userID", userID,
		"requestID", requestID,
	)

	if m.OnFloorDenied != nil {
		m.OnFloorDenied(floorID, userID)
	}
}

// handleClientConnect handles client connection events.
func (m *Manager) handleClientConnect(remoteAddr string, userID uint16) {
	m.log.Infow("BFCP client connected",
		"remoteAddr", remoteAddr,
		"userID", userID,
	)

	if m.OnClientConnect != nil {
		m.OnClientConnect(remoteAddr, userID)
	}
}

// handleClientDisconnect handles client disconnection events.
func (m *Manager) handleClientDisconnect(remoteAddr string, userID uint16) {
	m.log.Infow("BFCP client disconnected",
		"remoteAddr", remoteAddr,
		"userID", userID,
	)

	if m.OnClientDisconnect != nil {
		m.OnClientDisconnect(remoteAddr, userID)
	}
}

// handleError handles server errors.
func (m *Manager) handleError(err error) {
	m.log.Errorw("BFCP server error", err)
}

// RequestFloorForVirtualClient requests the content floor on behalf of the
// virtual BFCP client (representing WebRTC participant starting screenshare).
// This simulates a FloorRequest from the virtual client and grants it.
func (m *Manager) RequestFloorForVirtualClient() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.virtualFloorHeld {
		m.log.Debugw("BFCP virtual client already holds floor",
			"floorID", m.config.ContentFloorID,
			"userID", VirtualClientUserID,
		)
		return nil
	}

	m.log.Infow("BFCP virtual client requesting floor (WebRTC screenshare start)",
		"floorID", m.config.ContentFloorID,
		"userID", VirtualClientUserID,
		"conferenceID", m.config.ConferenceID,
	)

	// Get the floor state machine
	floor, exists := m.server.GetFloor(m.config.ContentFloorID)
	if !exists {
		m.log.Debugw("BFCP floor not found, creating it",
			"floorID", m.config.ContentFloorID,
		)
		floor = m.server.CreateFloor(m.config.ContentFloorID)
	}

	// Log current floor state
	m.log.Debugw("BFCP floor state before virtual request",
		"floorID", m.config.ContentFloorID,
		"state", floor.GetState().String(),
		"owner", floor.GetOwner(),
		"isAvailable", floor.IsAvailable(),
		"isGranted", floor.IsGranted(),
	)

	// Request the floor for virtual client
	m.virtualRequestID++
	status, err := floor.Request(VirtualClientUserID, m.virtualRequestID, bfcp.PriorityNormal)
	if err != nil {
		m.log.Errorw("BFCP virtual client floor request failed", err,
			"floorID", m.config.ContentFloorID,
			"userID", VirtualClientUserID,
			"requestID", m.virtualRequestID,
		)
		return fmt.Errorf("floor request failed: %w", err)
	}

	m.log.Infow("BFCP virtual client floor request status",
		"floorID", m.config.ContentFloorID,
		"userID", VirtualClientUserID,
		"requestID", m.virtualRequestID,
		"status", status.String(),
	)

	// If pending, grant it (we are the server, auto-grant for virtual client)
	if status == bfcp.RequestStatusPending || status == bfcp.RequestStatusAccepted {
		if err := floor.Grant(); err != nil {
			m.log.Errorw("BFCP virtual client floor grant failed", err,
				"floorID", m.config.ContentFloorID,
				"userID", VirtualClientUserID,
			)
			return fmt.Errorf("floor grant failed: %w", err)
		}
		m.log.Infow("BFCP virtual client floor granted",
			"floorID", m.config.ContentFloorID,
			"userID", VirtualClientUserID,
		)

		// Invoke OnFloorGranted callback (triggers re-INVITE for content negotiation)
		if m.OnFloorGranted != nil {
			m.OnFloorGranted(m.config.ContentFloorID, VirtualClientUserID)
		}
	}

	m.virtualFloorHeld = true

	// Log floor state after grant
	m.log.Debugw("BFCP floor state after virtual grant",
		"floorID", m.config.ContentFloorID,
		"state", floor.GetState().String(),
		"owner", floor.GetOwner(),
		"isAvailable", floor.IsAvailable(),
		"isGranted", floor.IsGranted(),
	)

	// Broadcast FloorStatus to all connected BFCP clients (Polycom)
	// This tells them the floor is now held by the virtual client
	m.log.Infow("BFCP broadcasting FloorStatus GRANTED to connected clients",
		"floorID", m.config.ContentFloorID,
		"userID", VirtualClientUserID,
		"requestID", m.virtualRequestID,
	)
	m.server.BroadcastFloorStatus(VirtualClientUserID, m.config.ContentFloorID, m.virtualRequestID, bfcp.RequestStatusGranted, 0)

	return nil
}

// ReleaseFloorForVirtualClient releases the content floor held by the
// virtual BFCP client (when WebRTC screenshare stops).
func (m *Manager) ReleaseFloorForVirtualClient() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.virtualFloorHeld {
		m.log.Debugw("BFCP virtual client doesn't hold floor, nothing to release",
			"floorID", m.config.ContentFloorID,
		)
		return nil
	}

	m.log.Infow("BFCP virtual client releasing floor (WebRTC screenshare stop)",
		"floorID", m.config.ContentFloorID,
		"userID", VirtualClientUserID,
		"conferenceID", m.config.ConferenceID,
	)

	floor, exists := m.server.GetFloor(m.config.ContentFloorID)
	if !exists {
		m.log.Debugw("BFCP floor not found for release",
			"floorID", m.config.ContentFloorID,
		)
		m.virtualFloorHeld = false
		return nil
	}

	// Log current floor state
	m.log.Debugw("BFCP floor state before virtual release",
		"floorID", m.config.ContentFloorID,
		"state", floor.GetState().String(),
		"owner", floor.GetOwner(),
	)

	// Release the floor
	if err := floor.Release(VirtualClientUserID); err != nil {
		m.log.Errorw("BFCP virtual client floor release failed", err,
			"floorID", m.config.ContentFloorID,
			"userID", VirtualClientUserID,
		)
		return fmt.Errorf("floor release failed: %w", err)
	}

	// Save request ID before clearing state
	requestID := m.virtualRequestID
	m.virtualFloorHeld = false

	m.log.Infow("BFCP virtual client floor released",
		"floorID", m.config.ContentFloorID,
		"userID", VirtualClientUserID,
	)

	// Log floor state after release
	m.log.Debugw("BFCP floor state after virtual release",
		"floorID", m.config.ContentFloorID,
		"state", floor.GetState().String(),
		"owner", floor.GetOwner(),
		"isAvailable", floor.IsAvailable(),
	)

	// Broadcast FloorStatus RELEASED to all connected BFCP clients (Polycom)
	// This tells them the floor is now available
	m.log.Infow("BFCP broadcasting FloorStatus RELEASED to connected clients",
		"floorID", m.config.ContentFloorID,
		"userID", VirtualClientUserID,
		"requestID", requestID,
	)
	m.server.BroadcastFloorStatus(VirtualClientUserID, m.config.ContentFloorID, requestID, bfcp.RequestStatusReleased, 0)

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
