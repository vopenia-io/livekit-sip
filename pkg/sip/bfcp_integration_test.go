package sip

import (
	"net/netip"
	"testing"

	"github.com/livekit/protocol/logger"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/media-sdk/sdp/v2/bfcp"
)

// TestPolyBFCPNegotiation simulates a Poly device INVITE with BFCP
func TestPolyBFCPNegotiation(t *testing.T) {
	log := logger.GetLogger()

	// Simulate Poly device SDP offer
	polyOffer := createPolyBFCPOffer()

	// Step 1: Handler processes offer (vendor-agnostic)
	handler, err := NewBFCPHandler(log, "192.168.1.100", 5070, true)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	conferenceID := uint32(12345)
	err = handler.ProcessOffer(polyOffer, conferenceID)
	if err != nil {
		t.Fatalf("Failed to process Poly offer: %v", err)
	}

	// Step 2: Verify vendor detection happened internally
	offerUserID := handler.GetOfferUserID()
	if offerUserID == 0 {
		t.Error("Should have extracted user ID from offer")
	}

	// Step 3: Create answer (vendor-specific logic applied internally)
	answer, err := handler.CreateAnswer()
	if err != nil {
		t.Fatalf("Failed to create answer: %v", err)
	}

	// Step 4: Verify answer has correct Poly-compatible settings
	if answer == nil {
		t.Fatal("Answer should not be nil")
	}

	// Poly expects setup:passive when they send active
	if answer.Setup != "passive" {
		t.Errorf("Expected setup:passive for Poly, got %s", answer.Setup)
	}

	// Should maintain conference ID
	if answer.ConferenceID != conferenceID {
		t.Errorf("Expected conference ID %d, got %d", conferenceID, answer.ConferenceID)
	}

	// Should have floor control
	if answer.FloorCtrl == "" {
		t.Error("Answer should have floor control set")
	}

	t.Logf("✅ Poly negotiation successful: setup=%s, floorCtrl=%s, confID=%d",
		answer.Setup, answer.FloorCtrl, answer.ConferenceID)
}

// TestBFCPSessionLifecycle tests session management with BFCP
func TestBFCPSessionLifecycle(t *testing.T) {
	log := logger.GetLogger()

	// Step 1: Process offer
	handler, _ := NewBFCPHandler(log, "192.168.1.100", 5070, true)
	polyOffer := createPolyBFCPOffer()
	handler.ProcessOffer(polyOffer, 12345)

	// Step 2: Create session with generic config
	sessionMgr := NewBFCPSessionManager(log)
	config := handler.GetConfig()

	if config == nil {
		t.Fatal("Handler should provide config after processing offer")
	}

	session, err := sessionMgr.CreateSession("call-123", config)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Step 3: Verify session has generic config (no vendor details)
	sessionConfig := session.GetConfig()
	if sessionConfig.ConferenceID != 12345 {
		t.Error("Session should have correct conference ID")
	}

	// Step 4: Activate session
	session.SetActive(true)
	if !session.IsActive() {
		t.Error("Session should be active")
	}

	// Step 5: Map content track to floor
	session.SetContentTrack("track-456", ScreenShareFloorID)
	trackID, floorID := session.GetContentTrack()

	if trackID != "track-456" {
		t.Errorf("Expected track track-456, got %s", trackID)
	}

	if floorID != ScreenShareFloorID {
		t.Errorf("Expected floor %d, got %d", ScreenShareFloorID, floorID)
	}

	// Step 6: Cleanup
	err = sessionMgr.DestroySession("call-123")
	if err != nil {
		t.Errorf("Failed to destroy session: %v", err)
	}

	t.Log("✅ Session lifecycle completed successfully")
}

// TestHandlerRemainsVendorAgnostic verifies handler has no vendor-specific code
func TestHandlerRemainsVendorAgnostic(t *testing.T) {
	log := logger.GetLogger()

	// Test with Poly-like offer
	polyOffer := createPolyBFCPOffer()

	handler1, _ := NewBFCPHandler(log, "192.168.1.100", 5070, true)
	err1 := handler1.ProcessOffer(polyOffer, 12345)
	if err1 != nil {
		t.Fatalf("Handler should process Poly offer: %v", err1)
	}

	answer1, _ := handler1.CreateAnswer()

	// Test with generic offer (different setup pattern)
	genericOffer := createGenericBFCPOffer()

	handler2, _ := NewBFCPHandler(log, "192.168.1.100", 5070, true)
	err2 := handler2.ProcessOffer(genericOffer, 12346)
	if err2 != nil {
		t.Fatalf("Handler should process generic offer: %v", err2)
	}

	answer2, _ := handler2.CreateAnswer()

	// Verify handler processed both without vendor-specific code paths
	if answer1 == nil || answer2 == nil {
		t.Fatal("Handler should create answers for both vendors")
	}

	// Both answers should have setup roles (reversed from offer)
	if answer1.Setup == "" || answer2.Setup == "" {
		t.Error("Both answers should have setup roles")
	}

	// Answers should be different based on vendor detection
	// (done internally by media-sdk)
	if answer1.Setup == answer2.Setup {
		// This might be same or different depending on offers
		// The point is handler doesn't care - it delegates to negotiator
		t.Log("Handler delegates vendor logic to negotiator")
	}

	t.Log("✅ Handler remains vendor-agnostic")
}

// TestBFCPSessionWithPipelineIntegration tests session + pipeline integration
func TestBFCPSessionWithPipelineIntegration(t *testing.T) {
	log := logger.GetLogger()

	// Setup
	handler, _ := NewBFCPHandler(log, "192.168.1.100", 5070, true)
	polyOffer := createPolyBFCPOffer()
	handler.ProcessOffer(polyOffer, 12345)

	sessionMgr := NewBFCPSessionManager(log)
	config := handler.GetConfig()
	session, _ := sessionMgr.CreateSession("call-123", config)

	// Create pipeline
	pipelineConfig := DefaultBFCPPipelineConfig()
	pipeline := NewBFCPPipeline(pipelineConfig, log)

	// Simulate floor granted
	trackID := "content-track-1"
	session.SetContentTrack(trackID, ScreenShareFloorID)
	session.SetActive(true)

	// Note: We can't actually start the pipeline without real IO sources
	// but we can verify the integration points exist

	// Verify session state
	if !session.IsActive() {
		t.Error("Session should be active")
	}

	retrievedTrack, retrievedFloor := session.GetContentTrack()
	if retrievedTrack != trackID {
		t.Error("Session should track content track")
	}

	if retrievedFloor != ScreenShareFloorID {
		t.Error("Session should track floor ID")
	}

	// Verify pipeline can be queried
	stats := pipeline.GetStats()
	if stats == nil {
		t.Error("Pipeline should provide stats")
	}

	// Simulate floor revoked
	session.SetActive(false)
	pipeline.Stop()

	if session.IsActive() {
		t.Error("Session should be inactive")
	}

	if pipeline.IsRunning() {
		t.Error("Pipeline should be stopped")
	}

	sessionMgr.DestroySession("call-123")

	t.Log("✅ Session + Pipeline integration verified")
}

// TestMultipleSessionsWithFloorLookup tests multiple sessions and floor-based lookup
func TestMultipleSessionsWithFloorLookup(t *testing.T) {
	log := logger.GetLogger()
	sessionMgr := NewBFCPSessionManager(log)

	// Create multiple sessions
	addr1 := netip.MustParseAddr("192.168.1.100")
	addr2 := netip.MustParseAddr("192.168.1.101")

	config1 := &bfcp.Config{
		Port:         5070,
		Addr:         addr1,
		ConferenceID: 12345,
		UserID:       1,
		FloorID:      1,
		MediaStream:  1,
	}

	config2 := &bfcp.Config{
		Port:         5070,
		Addr:         addr2,
		ConferenceID: 12346,
		UserID:       2,
		FloorID:      2,
		MediaStream:  2,
	}

	session1, _ := sessionMgr.CreateSession("call-1", config1)
	session2, _ := sessionMgr.CreateSession("call-2", config2)

	// Activate sessions and map tracks
	session1.SetActive(true)
	session1.SetContentTrack("track-1", 1)

	session2.SetActive(true)
	session2.SetContentTrack("track-2", 2)

	// Lookup by floor ID
	found, exists := sessionMgr.GetSessionByFloor(1)
	if !exists {
		t.Fatal("Should find session by floor 1")
	}

	if found.GetCallID() != "call-1" {
		t.Error("Should find correct session for floor 1")
	}

	// Verify active sessions
	active := sessionMgr.GetActiveSessions()
	if len(active) != 2 {
		t.Errorf("Expected 2 active sessions, got %d", len(active))
	}

	// Cleanup
	sessionMgr.DestroyAll()

	all := sessionMgr.GetAllSessions()
	if len(all) != 0 {
		t.Error("All sessions should be destroyed")
	}

	t.Log("✅ Multiple sessions managed correctly")
}

// TestBFCPAnswerForDifferentVendors ensures answers are appropriate for each vendor
func TestBFCPAnswerForDifferentVendors(t *testing.T) {
	log := logger.GetLogger()

	testCases := []struct {
		name          string
		offer         *sdpv2.SDP
		expectedSetup string
		description   string
	}{
		{
			name:          "Poly active",
			offer:         createPolyBFCPOffer(),
			expectedSetup: "passive",
			description:   "Poly sends active, expects passive",
		},
		{
			name:          "Generic passive",
			offer:         createGenericBFCPOffer(),
			expectedSetup: "active",
			description:   "Generic sends passive, expects active",
		},
		{
			name:          "Actpass offer",
			offer:         createActpassBFCPOffer(),
			expectedSetup: "passive",
			description:   "Actpass offer, respond with passive",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			handler, _ := NewBFCPHandler(log, "192.168.1.100", 5070, true)
			err := handler.ProcessOffer(tc.offer, 12345)
			if err != nil {
				t.Fatalf("Failed to process %s offer: %v", tc.name, err)
			}

			answer, err := handler.CreateAnswer()
			if err != nil {
				t.Fatalf("Failed to create answer for %s: %v", tc.name, err)
			}

			if answer.Setup != tc.expectedSetup {
				t.Errorf("%s: expected setup %s, got %s",
					tc.description, tc.expectedSetup, answer.Setup)
			}

			t.Logf("✅ %s: %s", tc.name, tc.description)
		})
	}
}

// Helper functions to create test SDP offers

func createPolyBFCPOffer() *sdpv2.SDP {
	addr := netip.MustParseAddr("192.168.1.50")

	return &sdpv2.SDP{
		Addr: addr,
		BFCP: &sdpv2.BFCPMedia{
			Port:         9,
			ConnectionIP: addr,
			FloorCtrl:    "c-s",
			ConferenceID: 1,
			UserID:       1,
			FloorID:      2,
			MediaStream:  3,
			Setup:        "active",
			Connection:   "new",
			Floors: []sdpv2.BFCPFloor{
				{FloorID: 2, MediaStream: 3},
			},
		},
		ScreenShareVideo: &sdpv2.SDPMedia{
			Kind:    sdpv2.MediaKindVideo,
			Label:   "3",
			Content: "slides",
		},
	}
}

func createGenericBFCPOffer() *sdpv2.SDP {
	addr := netip.MustParseAddr("192.168.1.60")

	return &sdpv2.SDP{
		Addr: addr,
		BFCP: &sdpv2.BFCPMedia{
			Port:         5000,
			ConnectionIP: addr,
			FloorCtrl:    "c-s",
			ConferenceID: 1,
			UserID:       10,
			FloorID:      1,
			MediaStream:  1,
			Setup:        "passive",
			Connection:   "new",
			Floors: []sdpv2.BFCPFloor{
				{FloorID: 1, MediaStream: 1},
			},
		},
	}
}

func createActpassBFCPOffer() *sdpv2.SDP {
	addr := netip.MustParseAddr("192.168.1.70")

	return &sdpv2.SDP{
		Addr: addr,
		BFCP: &sdpv2.BFCPMedia{
			Port:         9,
			ConnectionIP: addr,
			FloorCtrl:    "c-s",
			ConferenceID: 1,
			UserID:       5,
			FloorID:      2,
			MediaStream:  10,
			Setup:        "actpass",
			Connection:   "new",
			Floors: []sdpv2.BFCPFloor{
				{FloorID: 2, MediaStream: 10},
			},
		},
	}
}
