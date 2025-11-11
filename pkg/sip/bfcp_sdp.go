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
	answer := &sdpv2.BFCPMedia{
		Port:         uint16(b.bfcpServerPort), // Tell device our BFCP server port (5070)
		ConnectionIP: serverIP,                 // Tell device our BFCP server IP
		FloorCtrl:    "c-only",                 // Tell device: YOU must be client-only
		ConferenceID: b.conferenceID,           // Use our allocated conference ID
		UserID:       offer.UserID,             // Echo back the device's user ID
		FloorID:      offer.FloorID,            // Echo back the floor ID
		MediaStream:  offer.MediaStream,        // Echo back the media stream
		Setup:        "passive",                // We're the server (passive), device connects (active)
		Connection:   "new",                    // New connection
	}

	if EnableBFCPDebugLogging {
		b.log.Infow("🔵 [BFCP-SDP] Built BFCP answer",
			"offerPort", offer.Port,
			"offerFloorCtrl", offer.FloorCtrl,
			"offerSetup", offer.Setup,
			"offerConferenceID", offer.ConferenceID,
			"offerUserID", offer.UserID,
			"answerPort", answer.Port,
			"answerFloorCtrl", answer.FloorCtrl,
			"answerSetup", answer.Setup,
			"answerConferenceID", answer.ConferenceID,
			"answerUserID", answer.UserID,
			"serverIP", b.bfcpServerIP,
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
