package sip

import (
	"time"
)

// BFCP Server Configuration
const (
	BFCPServerAddr      = "0.0.0.0:5070"
	BFCPConferenceID    = uint32(1)
	BFCPMaxFloors       = 10
	BFCPMaxParticipants = 50
)

// User ID Allocation Range
// All participants (both SIP and WebRTC) use the same range
const (
	UserIDStart = uint16(1)
	UserIDEnd   = uint16(999)
)

// Floor IDs (Industry Standard Convention)
// While not RFC-mandated, these assignments match vendor implementations:
// - Polycom X series
// - Cisco endpoints
// - Most SIP MCUs
const (
	// FloorID 1: Main video/camera (participant video)
	MainVideoFloorID = uint16(1)

	// FloorID 2: Content/presentation sharing (screen share)
	// This is the floor controlled by BFCP for VideoManager lifecycle
	ScreenShareFloorID = uint16(2)

	// FloorID 3: Audio (optional, for future use)
	AudioFloorID = uint16(3)
)

// Floor Control Behavior
const (
	AutoGrantFirstPresenter = true
	QueueFloorRequests      = false  // Simple architecture - no queuing
	MaxQueuedRequests       = 10
	AllowMultiplePresenters = false
	AutoReleaseOnDisconnect = true
)

// BFCP Protocol Timeouts
const (
	FloorRequestTimeout         = 10 * time.Second
	BFCPResponseTimeout         = 5 * time.Second
	BFCPKeepAliveInterval       = 30 * time.Second
	ParticipantCleanupInterval  = 60 * time.Second
	MaxFloorHoldDuration        = 30 * time.Minute
)

// Logging Configuration
const (
	EnableBFCPDebugLogging   = true
	EnableBFCPTracing        = true
	LogFloorStateChanges     = true
	EnableQueueNotifications = false  // Queuing disabled for simple architecture
	LogBFCPMessages          = true
)
