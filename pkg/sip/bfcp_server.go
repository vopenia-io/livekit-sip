package sip

import (
	"fmt"
	"sync/atomic"

	"github.com/livekit/protocol/logger"
	"github.com/vopenia/bfcp"
)

// BFCPServerManager manages the BFCP server lifecycle and configuration
type BFCPServerManager struct {
	server            *bfcp.Server
	nextConferenceID  *atomic.Uint32
	log               logger.Logger
}

// NewBFCPServerManager creates a new BFCP server manager
func NewBFCPServerManager(log logger.Logger) *BFCPServerManager {
	return &BFCPServerManager{
		nextConferenceID: &atomic.Uint32{},
		log:              log,
	}
}

// StartBFCPServer initializes and starts the BFCP floor control server
func (m *BFCPServerManager) StartBFCPServer(listenIP string, bfcpPort int) error {
	if listenIP == "" {
		listenIP = "0.0.0.0"
	}

	bfcpAddr := fmt.Sprintf("%s:%d", listenIP, bfcpPort)

	m.log.Infow("🔵 [BFCP-Server] Initializing BFCP floor control server",
		"address", bfcpAddr,
	)

	// Create BFCP server configuration with auto-grant enabled
	bfcpConfig := bfcp.DefaultServerConfig(bfcpAddr, 1)
	bfcpConfig.AutoGrant = AutoGrantFirstPresenter
	bfcpConfig.MaxFloors = BFCPMaxFloors
	bfcpConfig.EnableLogging = EnableBFCPDebugLogging

	// Create BFCP server
	m.server = bfcp.NewServer(bfcpConfig)

	// Set up event callbacks
	m.setupCallbacks()

	// Start BFCP server in background
	go func() {
		m.log.Infow("🔵 [BFCP-Server] Starting BFCP floor control server",
			"address", bfcpAddr,
			"conferenceID", bfcpConfig.ConferenceID,
			"maxFloors", bfcpConfig.MaxFloors,
			"autoGrant", bfcpConfig.AutoGrant,
		)

		if err := m.server.ListenAndServe(); err != nil {
			m.log.Errorw("🔴 [BFCP-Server] BFCP server error", err,
				"address", bfcpAddr,
			)
		}
	}()

	m.log.Infow("🔵 [BFCP-Server] ✅ BFCP server initialization complete",
		"listening", bfcpAddr,
		"autoGrant", bfcpConfig.AutoGrant,
	)

	return nil
}

// setupCallbacks configures BFCP server event callbacks
func (m *BFCPServerManager) setupCallbacks() {
	m.server.OnClientConnect = func(remoteAddr string, userID uint16) {
		if LogFloorStateChanges {
			m.log.Infow("🟢 [BFCP-Server] Client connected",
				"remoteAddr", remoteAddr,
				"userID", userID,
			)
		}
	}

	m.server.OnClientDisconnect = func(remoteAddr string, userID uint16) {
		if LogFloorStateChanges {
			m.log.Infow("🟡 [BFCP-Server] Client disconnected",
				"remoteAddr", remoteAddr,
				"userID", userID,
			)
		}
	}

	m.server.OnFloorRequest = func(floorID, userID, requestID uint16) bool {
		if LogBFCPMessages {
			m.log.Infow("🔵 [BFCP-Server] Floor request received",
				"floorID", floorID,
				"userID", userID,
				"requestID", requestID,
			)
		}

		// Auto-grant floor requests (simple architecture)
		// For multi-device arbitration, this is where priority logic would go
		return AutoGrantFirstPresenter
	}

	m.server.OnFloorGranted = func(floorID, userID, requestID uint16) {
		if LogFloorStateChanges {
			m.log.Infow("🟢 [BFCP-Server] ✅ Floor granted",
				"floorID", floorID,
				"userID", userID,
				"requestID", requestID,
			)
		}
	}

	m.server.OnFloorReleased = func(floorID, userID uint16) {
		if LogFloorStateChanges {
			m.log.Infow("🔵 [BFCP-Server] Floor released",
				"floorID", floorID,
				"userID", userID,
			)
		}
	}

	m.server.OnFloorDenied = func(floorID, userID, requestID uint16) {
		if LogFloorStateChanges {
			m.log.Warnw("🔴 [BFCP-Server] ❌ Floor denied", nil,
				"floorID", floorID,
				"userID", userID,
				"requestID", requestID,
			)
		}
	}

	m.server.OnError = func(err error) {
		m.log.Errorw("🔴 [BFCP-Server] Server error", err)
	}

	if EnableBFCPDebugLogging {
		m.log.Infow("🔵 [BFCP-Server] Event callbacks configured",
			"logMessages", LogBFCPMessages,
			"logStateChanges", LogFloorStateChanges,
		)
	}
}

// AllocateConferenceID generates a unique BFCP conference ID for each call
func (m *BFCPServerManager) AllocateConferenceID() uint32 {
	conferenceID := m.nextConferenceID.Add(1)

	if EnableBFCPDebugLogging {
		m.log.Debugw("🔵 [BFCP-Server] Conference ID allocated",
			"conferenceID", conferenceID,
		)
	}

	return conferenceID
}

// GetServer returns the underlying BFCP server instance
func (m *BFCPServerManager) GetServer() *bfcp.Server {
	return m.server
}

// Shutdown stops the BFCP server (currently no-op as bfcp.Server doesn't have Shutdown)
func (m *BFCPServerManager) Shutdown() error {
	if m.server == nil {
		return nil
	}

	m.log.Infow("🔵 [BFCP-Server] BFCP server shutdown requested")
	// Note: bfcp.Server doesn't provide a Shutdown method
	// The server will be cleaned up when the process exits
	return nil
}
