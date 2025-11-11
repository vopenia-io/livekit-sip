package sip

import (
	"net/netip"

	"github.com/livekit/protocol/logger"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
)

// BFCPSDPBuilder handles BFCP SDP answer generation
// This tells SIP devices how to connect to our BFCP server
type BFCPSDPBuilder struct {
	log              logger.Logger
	bfcpServerIP     string
	bfcpServerPort   int
	conferenceID     uint32
}

// NewBFCPSDPBuilder creates a new BFCP SDP builder
func NewBFCPSDPBuilder(log logger.Logger, serverIP string, serverPort int, conferenceID uint32) *BFCPSDPBuilder {
	return &BFCPSDPBuilder{
		log:            log,
		bfcpServerIP:   serverIP,
		bfcpServerPort: serverPort,
		conferenceID:   conferenceID,
	}
}

// BuildAnswer creates a BFCP SDP answer that tells the SIP device to connect as a client
// This is critical for Polycom and other SIP devices to establish BFCP connection
func (b *BFCPSDPBuilder) BuildAnswer(offer *sdpv2.BFCPMedia) *sdpv2.BFCPMedia {
	if offer == nil {
		b.log.Warnw("🔴 [BFCP-SDP] Cannot build answer - offer is nil", nil)
		return nil
	}

	// Parse server IP address
	serverIP, err := netip.ParseAddr(b.bfcpServerIP)
	if err != nil {
		b.log.Errorw("🔴 [BFCP-SDP] Failed to parse server IP", err,
			"serverIP", b.bfcpServerIP,
		)
		return nil
	}

	// Build answer that instructs SIP device to connect to our BFCP server
	// CRITICAL: We echo back Poly's ConferenceID, UserID, and FloorID
	// FloorID comes from the offer parameter (preserving Poly's original Floor 1)
	// CRITICAL FIX: Use "c-s" (client-server) instead of "s-only" for Poly compatibility
	// Poly devices expect "c-s" mode where both can control floors
	answerFloorCtrl := "c-s" // Client-server mode for Poly compatibility

	answer := &sdpv2.BFCPMedia{
		Port:         uint16(b.bfcpServerPort), // Tell device our BFCP server port (5070)
		ConnectionIP: serverIP,                 // Tell device our BFCP server IP
		FloorCtrl:    answerFloorCtrl,          // c-s for Poly compatibility (was s-only)
		ConferenceID: offer.ConferenceID,       // Echo back Poly's conference ID from initial offer
		UserID:       offer.UserID,             // Echo back the device's user ID
		FloorID:      offer.FloorID,            // Echo back Poly's floor ID (preserve Floor 1)
		MediaStream:  offer.MediaStream,        // Echo back the media stream (should be 3 for slides)
		Setup:        "active",                 // We initiate TCP connection (Poly offered actpass)
		Connection:   "new",                    // New connection
	}

	if EnableBFCPDebugLogging {
		b.log.Infow("🔵 [BFCP-SDP] Built BFCP answer (preserving Poly's Floor ID)",
			"offerPort", offer.Port,
			"offerFloorCtrl", offer.FloorCtrl,
			"offerSetup", offer.Setup,
			"offerConferenceID", offer.ConferenceID,
			"offerUserID", offer.UserID,
			"offerFloorID", offer.FloorID,
			"answerPort", answer.Port,
			"answerFloorCtrl", answer.FloorCtrl,
			"answerSetup", answer.Setup,
			"answerConferenceID", answer.ConferenceID,
			"answerUserID", answer.UserID,
			"answerFloorID", answer.FloorID,
			"serverIP", b.bfcpServerIP,
			"note", "c-s mode, active setup - Poly compatibility fix",
		)
	}

	return answer
}

// UpdateConferenceID updates the conference ID for subsequent answers
func (b *BFCPSDPBuilder) UpdateConferenceID(conferenceID uint32) {
	b.conferenceID = conferenceID

	if EnableBFCPDebugLogging {
		b.log.Debugw("🔵 [BFCP-SDP] Conference ID updated",
			"conferenceID", conferenceID,
		)
	}
}
