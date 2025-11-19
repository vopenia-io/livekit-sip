package sip

import "time"

// BFCP Floor IDs
const (
	// ScreenShareFloorID is the floor ID used for screen sharing
	// Floor ID 1 is typically used for main video/audio
	// Floor ID 2 is commonly used for content/presentation streams
	ScreenShareFloorID uint16 = 2
)

// BFCP Timeouts
const (
	// FloorRequestTimeout is how long to wait for floor request response
	FloorRequestTimeout = 5 * time.Second
)

// BFCP Debug
var (
	// EnableBFCPDebugLogging enables detailed BFCP logging
	EnableBFCPDebugLogging = false
)
