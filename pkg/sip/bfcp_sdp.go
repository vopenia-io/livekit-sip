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
	// CRITICAL: We echo back Poly's ConferenceID and UserID, but NOT FloorID
	// FloorID comes from the offer parameter (which we set to Floor 2 in re-INVITE)
	// This forces Poly to use our floor numbering (Floor 2) instead of its Floor 1
	// CRITICAL: We are ALWAYS the BFCP server, so we ALWAYS respond with s-only
	// This forces the SIP device to be the client and connect to us
	answerFloorCtrl := "s-only" // We're ALWAYS server-only

	answer := &sdpv2.BFCPMedia{
		Port:         uint16(b.bfcpServerPort), // Tell device our BFCP server port (5070)
		ConnectionIP: serverIP,                 // Tell device our BFCP server IP
		FloorCtrl:    answerFloorCtrl,          // Dynamic: s-only if they're c-only, else c-only
		ConferenceID: offer.ConferenceID,       // Echo back Poly's conference ID from initial offer
		UserID:       offer.UserID,             // Echo back the device's user ID
		FloorID:      offer.FloorID,            // Use OUR floor ID (Floor 2) - passed in via offer parameter
		MediaStream:  offer.MediaStream,        // Echo back the media stream (should be 3 for slides)
		Setup:        "passive",                // We're the server (passive), device connects (active)
		Connection:   "new",                    // New connection
	}

	if EnableBFCPDebugLogging {
		b.log.Infow("🔵 [BFCP-SDP] Built BFCP answer (forcing Floor 2)",
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
			"note", "We are BFCP server - rejecting Poly's Floor 1, using Floor 2",
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
