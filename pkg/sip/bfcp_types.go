package sip

import (
	"time"
)

// BFCPDeviceState represents a physical SIP device's BFCP state
type BFCPDeviceState struct {
	DeviceID     string
	CallID       string
	UserID       uint16
	ConferenceID uint32
	ActiveFloors []uint16
	RegisteredAt time.Time
	LastActivity time.Time
}

// BFCPWebRTCState represents the single WebRTC participant's BFCP state
type BFCPWebRTCState struct {
	ParticipantID string
	UserID        uint16
	ConferenceID  uint32
	ActiveFloors  []uint16
	JoinedAt      time.Time
	LastActivity  time.Time
}

// ActiveFloor represents a currently granted floor
type ActiveFloor struct {
	FloorID       uint16
	HolderUserID  uint16
	HolderType    string // "SIP" or "WebRTC"
	ParticipantID string // CallID for SIP, ParticipantID for WebRTC
	GrantedAt     time.Time
	RequestID     uint16
}

// BFCPNotificationHandler interface for external notifications
// Implement this interface to receive BFCP events (e.g., for VideoManager control)
type BFCPNotificationHandler interface {
	OnFloorGranted(participantID string, floorID uint16)
	OnFloorRevoked(participantID string, floorID uint16, reason string)
	OnFloorDenied(participantID string, floorID uint16, reason string)
	OnParticipantDisconnected(participantID string)
}
