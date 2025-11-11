package sip

import (
	"fmt"

	"github.com/livekit/protocol/logger"
	"github.com/vopenia/bfcp"
)

// BFCPClientCallbacks handles BFCP client callbacks for the WebRTC participant
// This is where screenshare VideoManager lifecycle is controlled
type BFCPClientCallbacks struct {
	manager              *BFCPManager
	participantID        string
	userID               uint16
	screenShareFloorID   uint16 // Expected floor ID for screenshare
	log                  logger.Logger

	// Callback for VideoManager control
	onFloorGranted func(floorID uint16)
	onFloorRevoked func(floorID uint16)
}

// NewBFCPClientCallbacks creates a new callback handler
func NewBFCPClientCallbacks(
	manager *BFCPManager,
	participantID string,
	userID uint16,
	screenShareFloorID uint16,
	log logger.Logger,
	onFloorGranted func(floorID uint16),
	onFloorRevoked func(floorID uint16),
) *BFCPClientCallbacks {
	return &BFCPClientCallbacks{
		manager:            manager,
		participantID:      participantID,
		userID:             userID,
		screenShareFloorID: screenShareFloorID,
		log:                log,
		onFloorGranted:     onFloorGranted,
		onFloorRevoked:     onFloorRevoked,
	}
}

// OnConnected is called when BFCP client connects to server
func (cb *BFCPClientCallbacks) OnConnected() {
	cb.log.Infow("🟢 [BFCP-Client-WebRTC] ✅ Connected to BFCP server",
		"participantID", cb.participantID,
		"userID", cb.userID,
	)
}

// OnDisconnected is called when BFCP client disconnects from server
func (cb *BFCPClientCallbacks) OnDisconnected() {
	cb.log.Infow("🟡 [BFCP-Client-WebRTC] ⚠️ Disconnected from BFCP server",
		"participantID", cb.participantID,
		"userID", cb.userID,
	)

	// Auto-release floors if configured
	if AutoReleaseOnDisconnect {
		cb.log.Infow("🟡 [BFCP-Client-WebRTC] Auto-releasing floors on disconnect")
		// The manager will handle cleanup through maintenance loop
	}
}

// OnFloorGranted is called when a floor is granted
// For screenshare floor (FloorID=2), this starts the VideoManager
func (cb *BFCPClientCallbacks) OnFloorGranted(floorID, requestID uint16) {
	cb.log.Infow("🟢 [BFCP-Client-WebRTC] ✅✅✅ Floor GRANTED ✅✅✅",
		"floorID", floorID,
		"requestID", requestID,
		"participantID", cb.participantID,
		"userID", cb.userID,
	)

	// Update manager state
	cb.manager.GrantFloor(floorID, cb.userID, requestID)

	// If this is the screenshare floor, trigger VideoManager start
	if floorID == cb.screenShareFloorID {
		cb.log.Infow("🟢 [BFCP-Client-WebRTC] ✅ Screenshare floor granted - starting VideoManager",
			"floorID", floorID,
		)

		if cb.onFloorGranted != nil {
			cb.onFloorGranted(floorID)
		}
	}
}

// OnFloorDenied is called when a floor request is denied
func (cb *BFCPClientCallbacks) OnFloorDenied(floorID, requestID uint16, errorCode bfcp.ErrorCode) {
	cb.log.Warnw("🔴 [BFCP-Client-WebRTC] ❌ Floor DENIED", nil,
		"floorID", floorID,
		"requestID", requestID,
		"errorCode", errorCode,
		"participantID", cb.participantID,
		"userID", cb.userID,
	)

	// Notify manager handler
	if cb.manager.notifyHandler != nil {
		reason := fmt.Sprintf("Floor denied (error code: %d)", errorCode)
		cb.manager.notifyHandler.OnFloorDenied(cb.participantID, floorID, reason)
	}
}

// OnFloorRevoked is called when a floor is revoked by the server
// For screenshare floor (FloorID=2), this stops the VideoManager
func (cb *BFCPClientCallbacks) OnFloorRevoked(floorID uint16) {
	cb.log.Infow("🟡 [BFCP-Client-WebRTC] ⚠️ Floor REVOKED",
		"floorID", floorID,
		"participantID", cb.participantID,
		"userID", cb.userID,
	)

	// Update manager state
	cb.manager.RevokeFloor(floorID, cb.userID)

	// If this is the screenshare floor, trigger VideoManager stop
	if floorID == cb.screenShareFloorID {
		cb.log.Infow("🟡 [BFCP-Client-WebRTC] ⚠️ Screenshare floor revoked - stopping VideoManager",
			"floorID", floorID,
		)

		if cb.onFloorRevoked != nil {
			cb.onFloorRevoked(floorID)
		}
	}
}

// OnFloorReleased is called when a floor is released
// For screenshare floor (FloorID=2), this stops the VideoManager
func (cb *BFCPClientCallbacks) OnFloorReleased(floorID uint16) {
	cb.log.Infow("🟢 [BFCP-Client-WebRTC] Floor RELEASED",
		"floorID", floorID,
		"participantID", cb.participantID,
		"userID", cb.userID,
	)

	// Update manager state
	cb.manager.ReleaseFloor(floorID, cb.userID)

	// If this is the screenshare floor, trigger VideoManager stop
	if floorID == cb.screenShareFloorID {
		cb.log.Infow("🟢 [BFCP-Client-WebRTC] Screenshare floor released - stopping VideoManager",
			"floorID", floorID,
		)

		if cb.onFloorRevoked != nil {
			cb.onFloorRevoked(floorID)
		}
	}
}

// OnError is called when a BFCP protocol error occurs
func (cb *BFCPClientCallbacks) OnError(err error) {
	cb.log.Errorw("🔴 [BFCP-Client-WebRTC] BFCP protocol error", err,
		"participantID", cb.participantID,
		"userID", cb.userID,
	)
}

// DefaultBFCPNotificationHandler provides a simple implementation of BFCPNotificationHandler
// This is used for logging and can be extended for more complex behavior
type DefaultBFCPNotificationHandler struct {
	log logger.Logger
}

// NewDefaultBFCPNotificationHandler creates a default notification handler
func NewDefaultBFCPNotificationHandler(log logger.Logger) *DefaultBFCPNotificationHandler {
	return &DefaultBFCPNotificationHandler{
		log: log,
	}
}

func (h *DefaultBFCPNotificationHandler) OnFloorGranted(participantID string, floorID uint16) {
	h.log.Infow("🟢 [BFCP-Notify] Floor granted notification",
		"participantID", participantID,
		"floorID", floorID,
	)
}

func (h *DefaultBFCPNotificationHandler) OnFloorRevoked(participantID string, floorID uint16, reason string) {
	h.log.Infow("🟡 [BFCP-Notify] Floor revoked notification",
		"participantID", participantID,
		"floorID", floorID,
		"reason", reason,
	)
}

func (h *DefaultBFCPNotificationHandler) OnFloorDenied(participantID string, floorID uint16, reason string) {
	h.log.Warnw("🔴 [BFCP-Notify] Floor denied notification", nil,
		"participantID", participantID,
		"floorID", floorID,
		"reason", reason,
	)
}

func (h *DefaultBFCPNotificationHandler) OnParticipantDisconnected(participantID string) {
	h.log.Infow("🟡 [BFCP-Notify] Participant disconnected notification",
		"participantID", participantID,
	)
}
