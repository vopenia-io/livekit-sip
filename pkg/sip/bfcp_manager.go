package sip

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
	"github.com/vopenia/bfcp"
)

// BFCPManager manages BFCP floor control for multiple SIP devices and one WebRTC participant
type BFCPManager struct {
	// Core components
	server       *bfcp.Server
	confManager  *bfcp.ConferenceManager
	conferenceID uint32
	log          logger.Logger

	// Participant tracking
	webrtcClient *BFCPWebRTCState            // Single WebRTC participant
	sipDevices   map[uint16]*BFCPDeviceState // User ID -> SIP device state

	// Floor management
	activeFloors map[uint16]*ActiveFloor // Floor ID -> Active floor holder

	// User ID allocation
	nextUserID uint16

	// Notification handler
	notifyHandler BFCPNotificationHandler

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc

	mu sync.RWMutex
}

// NewBFCPManager creates and initializes a BFCP manager for a call
func NewBFCPManager(
	log logger.Logger,
	server *bfcp.Server,
	conferenceID uint32,
	notifyHandler BFCPNotificationHandler,
) (*BFCPManager, error) {
	log.Infow("🔵 [BFCP-Manager] Initializing BFCP manager",
		"conferenceID", conferenceID,
	)

	ctx, cancel := context.WithCancel(context.Background())

	manager := &BFCPManager{
		server:        server,
		confManager:   server.GetConferenceManager(),
		conferenceID:  conferenceID,
		log:           log,
		sipDevices:    make(map[uint16]*BFCPDeviceState),
		activeFloors:  make(map[uint16]*ActiveFloor),
		nextUserID:    UserIDStart,
		notifyHandler: notifyHandler,
		ctx:           ctx,
		cancel:        cancel,
	}

	// Start maintenance loop
	go manager.runMaintenanceLoop()

	log.Infow("🔵 [BFCP-Manager] ✅ BFCP manager initialized",
		"conferenceID", conferenceID,
		"userIDRange", fmt.Sprintf("%d-%d", UserIDStart, UserIDEnd),
	)

	return manager, nil
}

// RegisterSIPDevice registers a physical SIP device with BFCP
func (m *BFCPManager) RegisterSIPDevice(deviceID string, callID string, userID uint16) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Infow("🔵 [BFCP-Manager] Registering SIP device",
		"deviceID", deviceID,
		"callID", callID,
		"userID", userID,
	)

	// Check if device already registered
	if existing, exists := m.sipDevices[userID]; exists {
		m.log.Infow("🔵 [BFCP-Manager] Device already registered",
			"deviceID", existing.DeviceID,
			"userID", userID,
		)
		return fmt.Errorf("device with userID %d already registered", userID)
	}

	// Create device state
	device := &BFCPDeviceState{
		DeviceID:     deviceID,
		CallID:       callID,
		UserID:       userID,
		ConferenceID: m.conferenceID,
		ActiveFloors: []uint16{},
		RegisteredAt: time.Now(),
		LastActivity: time.Now(),
	}

	m.sipDevices[userID] = device

	m.log.Infow("🔵 [BFCP-Manager] ✅ SIP device registered",
		"deviceID", deviceID,
		"userID", userID,
		"conferenceID", m.conferenceID,
	)

	return nil
}

// UnregisterSIPDevice removes a SIP device and releases its floors
func (m *BFCPManager) UnregisterSIPDevice(userID uint16) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Infow("🔵 [BFCP-Manager] Unregistering SIP device",
		"userID", userID,
	)

	device, exists := m.sipDevices[userID]
	if !exists {
		m.log.Infow("🔵 [BFCP-Manager] Device not found",
			"userID", userID,
		)
		return fmt.Errorf("device with userID %d not found", userID)
	}

	// Release any held floors
	if len(device.ActiveFloors) > 0 {
		m.log.Infow("🔵 [BFCP-Manager] Releasing active floors",
			"deviceID", device.DeviceID,
			"floors", device.ActiveFloors,
		)
		for _, floorID := range device.ActiveFloors {
			m.releaseFloorInternal(floorID, userID)
		}
	}

	// Remove from tracking
	delete(m.sipDevices, userID)

	m.log.Infow("🔵 [BFCP-Manager] ✅ SIP device unregistered",
		"deviceID", device.DeviceID,
		"userID", userID,
	)

	return nil
}

// RegisterWebRTCParticipant registers the WebRTC participant with BFCP
func (m *BFCPManager) RegisterWebRTCParticipant(participantID string, userID uint16, client *bfcp.Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Infow("🔵 [BFCP-Manager] Registering WebRTC participant",
		"participantID", participantID,
		"userID", userID,
	)

	// Check if already registered
	if m.webrtcClient != nil {
		m.log.Infow("🔵 [BFCP-Manager] WebRTC participant already registered",
			"existingID", m.webrtcClient.ParticipantID,
		)
		return fmt.Errorf("WebRTC participant already registered: %s", m.webrtcClient.ParticipantID)
	}

	// Create WebRTC state
	m.webrtcClient = &BFCPWebRTCState{
		ParticipantID: participantID,
		UserID:        userID,
		ConferenceID:  m.conferenceID,
		ActiveFloors:  []uint16{},
		JoinedAt:      time.Now(),
		LastActivity:  time.Now(),
	}

	m.log.Infow("🔵 [BFCP-Manager] ✅ WebRTC participant registered",
		"participantID", participantID,
		"userID", userID,
		"conferenceID", m.conferenceID,
	)

	return nil
}

// UnregisterWebRTCParticipant removes the WebRTC participant
func (m *BFCPManager) UnregisterWebRTCParticipant() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.webrtcClient == nil {
		return nil
	}

	m.log.Infow("🔵 [BFCP-Manager] Unregistering WebRTC participant",
		"participantID", m.webrtcClient.ParticipantID,
	)

	// Release any held floors
	if len(m.webrtcClient.ActiveFloors) > 0 {
		m.log.Infow("🔵 [BFCP-Manager] Releasing active floors",
			"floors", m.webrtcClient.ActiveFloors,
		)
		for _, floorID := range m.webrtcClient.ActiveFloors {
			m.releaseFloorInternal(floorID, m.webrtcClient.UserID)
		}
	}

	participantID := m.webrtcClient.ParticipantID
	m.webrtcClient = nil

	m.log.Infow("🔵 [BFCP-Manager] ✅ WebRTC participant unregistered",
		"participantID", participantID,
	)

	// Notify handler
	if m.notifyHandler != nil {
		m.notifyHandler.OnParticipantDisconnected(participantID)
	}

	return nil
}

// GrantFloor grants a floor to a user (called from BFCP callbacks)
func (m *BFCPManager) GrantFloor(floorID uint16, userID uint16, requestID uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Infow("🔵 [BFCP-Manager] Floor granted",
		"floorID", floorID,
		"userID", userID,
		"requestID", requestID,
	)

	// Determine holder type and participant ID
	var holderType string
	var participantID string

	if m.webrtcClient != nil && m.webrtcClient.UserID == userID {
		holderType = "WebRTC"
		participantID = m.webrtcClient.ParticipantID
		m.webrtcClient.ActiveFloors = append(m.webrtcClient.ActiveFloors, floorID)
		m.webrtcClient.LastActivity = time.Now()
	} else if device, exists := m.sipDevices[userID]; exists {
		holderType = "SIP"
		participantID = device.CallID
		device.ActiveFloors = append(device.ActiveFloors, floorID)
		device.LastActivity = time.Now()
	} else {
		m.log.Warnw("🔵 [BFCP-Manager] ⚠️ Unknown user granted floor", nil,
			"userID", userID,
			"floorID", floorID,
		)
		return
	}

	// Update active floor tracking
	m.activeFloors[floorID] = &ActiveFloor{
		FloorID:       floorID,
		HolderUserID:  userID,
		HolderType:    holderType,
		ParticipantID: participantID,
		GrantedAt:     time.Now(),
		RequestID:     requestID,
	}

	m.log.Infow("🔵 [BFCP-Manager] ✅ Floor grant recorded",
		"floorID", floorID,
		"holderType", holderType,
		"participantID", participantID,
	)

	// Notify handler (for VideoManager control)
	if m.notifyHandler != nil && floorID == ScreenShareFloorID {
		m.log.Infow("🔵 [BFCP-Manager] Notifying handler of screenshare floor grant")
		m.notifyHandler.OnFloorGranted(participantID, floorID)
	}
}

// RevokeFloor revokes a floor from a user
func (m *BFCPManager) RevokeFloor(floorID uint16, userID uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Infow("🔵 [BFCP-Manager] Floor revoked",
		"floorID", floorID,
		"userID", userID,
	)

	m.releaseFloorInternal(floorID, userID)

	// Notify handler
	if m.notifyHandler != nil && floorID == ScreenShareFloorID {
		participantID := ""
		if m.webrtcClient != nil && m.webrtcClient.UserID == userID {
			participantID = m.webrtcClient.ParticipantID
		} else if device, exists := m.sipDevices[userID]; exists {
			participantID = device.CallID
		}
		if participantID != "" {
			m.log.Infow("🔵 [BFCP-Manager] Notifying handler of screenshare floor revoke")
			m.notifyHandler.OnFloorRevoked(participantID, floorID, "Floor revoked by server")
		}
	}
}

// ReleaseFloor releases a floor (called when user releases voluntarily)
func (m *BFCPManager) ReleaseFloor(floorID uint16, userID uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Infow("🔵 [BFCP-Manager] Floor release requested",
		"floorID", floorID,
		"userID", userID,
	)

	m.releaseFloorInternal(floorID, userID)
}

// releaseFloorInternal handles floor release (must be called with lock held)
func (m *BFCPManager) releaseFloorInternal(floorID uint16, userID uint16) {
	// Check if floor is actually held by this user
	if floor, exists := m.activeFloors[floorID]; exists {
		if floor.HolderUserID != userID {
			m.log.Warnw("🔵 [BFCP-Manager] ⚠️ Floor not held by this user", nil,
				"floorID", floorID,
				"requestedBy", userID,
				"actualHolder", floor.HolderUserID,
			)
			return
		}
	} else {
		m.log.Debugw("🔵 [BFCP-Manager] Floor not currently held",
			"floorID", floorID,
		)
		return
	}

	// Remove from active floors
	delete(m.activeFloors, floorID)

	// Update participant state
	if m.webrtcClient != nil && m.webrtcClient.UserID == userID {
		m.webrtcClient.ActiveFloors = removeFloorFromList(m.webrtcClient.ActiveFloors, floorID)
		m.webrtcClient.LastActivity = time.Now()
	} else if device, exists := m.sipDevices[userID]; exists {
		device.ActiveFloors = removeFloorFromList(device.ActiveFloors, floorID)
		device.LastActivity = time.Now()
	}

	m.log.Infow("🔵 [BFCP-Manager] ✅ Floor released",
		"floorID", floorID,
		"userID", userID,
	)
}

// GetActiveFloorHolder returns who currently holds a floor
func (m *BFCPManager) GetActiveFloorHolder(floorID uint16) (participantID string, holderType string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if floor, exists := m.activeFloors[floorID]; exists {
		return floor.ParticipantID, floor.HolderType
	}

	return "", ""
}

// GetStats returns BFCP statistics
func (m *BFCPManager) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	webrtcCount := 0
	if m.webrtcClient != nil {
		webrtcCount = 1
	}

	return map[string]interface{}{
		"sip_devices":     len(m.sipDevices),
		"webrtc_clients":  webrtcCount,
		"active_floors":   len(m.activeFloors),
		"conference_id":   m.conferenceID,
	}
}

// Shutdown gracefully shuts down the BFCP manager
func (m *BFCPManager) Shutdown() {
	m.log.Infow("🔵 [BFCP-Manager] Shutting down")

	m.cancel()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Release all floors
	for floorID, floor := range m.activeFloors {
		m.log.Infow("🔵 [BFCP-Manager] Releasing floor on shutdown",
			"floorID", floorID,
			"holder", floor.ParticipantID,
		)
	}
	m.activeFloors = make(map[uint16]*ActiveFloor)

	// Clear device tracking
	m.sipDevices = make(map[uint16]*BFCPDeviceState)
	m.webrtcClient = nil

	m.log.Infow("🔵 [BFCP-Manager] ✅ Shutdown complete")
}

// runMaintenanceLoop handles periodic cleanup
func (m *BFCPManager) runMaintenanceLoop() {
	ticker := time.NewTicker(ParticipantCleanupInterval)
	defer ticker.Stop()

	m.log.Infow("🔵 [BFCP-Manager] Maintenance loop started",
		"interval", ParticipantCleanupInterval,
	)

	for {
		select {
		case <-m.ctx.Done():
			m.log.Infow("🔵 [BFCP-Manager] Maintenance loop stopping")
			return
		case <-ticker.C:
			m.performMaintenance()
		}
	}
}

func (m *BFCPManager) performMaintenance() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	staleTimeout := ParticipantCleanupInterval * 2

	// Check for stale SIP devices
	for userID, device := range m.sipDevices {
		inactiveTime := now.Sub(device.LastActivity)
		if inactiveTime > staleTimeout {
			m.log.Infow("🔵 [BFCP-Manager] Removing stale SIP device",
				"deviceID", device.DeviceID,
				"userID", userID,
				"inactiveFor", inactiveTime,
			)
			delete(m.sipDevices, userID)
		}
	}

	// Check for floor hold timeouts
	for floorID, floor := range m.activeFloors {
		holdDuration := now.Sub(floor.GrantedAt)
		if holdDuration > MaxFloorHoldDuration {
			m.log.Infow("🔵 [BFCP-Manager] Floor held too long, forcing release",
				"floorID", floorID,
				"holder", floor.ParticipantID,
				"holdDuration", holdDuration,
			)
			m.releaseFloorInternal(floorID, floor.HolderUserID)
		}
	}

	if EnableBFCPDebugLogging {
		m.log.Debugw("🔵 [BFCP-Manager] Maintenance check complete",
			"sipDevices", len(m.sipDevices),
			"webrtcClient", m.webrtcClient != nil,
			"activeFloors", len(m.activeFloors),
		)
	}
}

// Helper function to remove floor from list
func removeFloorFromList(floors []uint16, floorID uint16) []uint16 {
	result := make([]uint16, 0, len(floors))
	for _, f := range floors {
		if f != floorID {
			result = append(result, f)
		}
	}
	return result
}
