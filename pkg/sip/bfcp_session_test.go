package sip

import (
	"net/netip"
	"testing"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/media-sdk/sdp/v2/bfcp"
)

func TestBFCPSession_Lifecycle(t *testing.T) {
	log := logger.GetLogger()

	config := &bfcp.Config{
		Port:         9,
		Addr:         netip.MustParseAddr("192.168.1.100"),
		ConferenceID: 12345,
		UserID:       1,
		FloorID:      2,
		SetupRole:    "passive",
		FloorControl: "c-s",
	}

	session := NewBFCPSession("call-123", config, log)

	if session.GetCallID() != "call-123" {
		t.Errorf("Expected callID call-123, got %s", session.GetCallID())
	}

	if session.IsActive() {
		t.Error("Session should not be active initially")
	}

	session.SetActive(true)
	if !session.IsActive() {
		t.Error("Session should be active after SetActive(true)")
	}

	session.SetContentTrack("track-456", 2)
	trackID, floorID := session.GetContentTrack()

	if trackID != "track-456" {
		t.Errorf("Expected trackID track-456, got %s", trackID)
	}

	if floorID != 2 {
		t.Errorf("Expected floorID 2, got %d", floorID)
	}

	stats := session.GetStats()
	if stats["callID"] != "call-123" {
		t.Error("Stats should contain correct callID")
	}
	if !stats["active"].(bool) {
		t.Error("Stats should show active session")
	}

	err := session.Destroy()
	if err != nil {
		t.Errorf("Failed to destroy session: %v", err)
	}

	if session.IsActive() {
		t.Error("Session should not be active after destroy")
	}

	trackID, _ = session.GetContentTrack()
	if trackID != "" {
		t.Error("Content track should be cleared after destroy")
	}
}

func TestBFCPSessionManager_CreateAndGet(t *testing.T) {
	log := logger.GetLogger()
	manager := NewBFCPSessionManager(log)

	config := &bfcp.Config{
		ConferenceID: 12345,
		UserID:       1,
		FloorID:      2,
	}

	session, err := manager.CreateSession("call-123", config)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	if session.GetCallID() != "call-123" {
		t.Error("Session has wrong callID")
	}

	retrieved, exists := manager.GetSession("call-123")
	if !exists {
		t.Fatal("Session should exist")
	}

	if retrieved.GetCallID() != "call-123" {
		t.Error("Retrieved session has wrong callID")
	}

	_, err = manager.CreateSession("call-123", config)
	if err == nil {
		t.Error("Should not be able to create duplicate session")
	}
}

func TestBFCPSessionManager_Update(t *testing.T) {
	log := logger.GetLogger()
	manager := NewBFCPSessionManager(log)

	config := &bfcp.Config{
		ConferenceID: 12345,
		UserID:       1,
	}

	_, err := manager.CreateSession("call-123", config)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	newConfig := &bfcp.Config{
		ConferenceID: 67890,
		UserID:       2,
	}

	err = manager.UpdateSession("call-123", newConfig)
	if err != nil {
		t.Errorf("Failed to update session: %v", err)
	}

	session, _ := manager.GetSession("call-123")
	if session.GetConfig().ConferenceID != 67890 {
		t.Error("Session config should be updated")
	}
}

func TestBFCPSessionManager_Destroy(t *testing.T) {
	log := logger.GetLogger()
	manager := NewBFCPSessionManager(log)

	config := &bfcp.Config{
		ConferenceID: 12345,
		UserID:       1,
	}

	session, _ := manager.CreateSession("call-123", config)
	session.SetActive(true)

	err := manager.DestroySession("call-123")
	if err != nil {
		t.Errorf("Failed to destroy session: %v", err)
	}

	_, exists := manager.GetSession("call-123")
	if exists {
		t.Error("Session should not exist after destroy")
	}
}

func TestBFCPSessionManager_GetActiveSessions(t *testing.T) {
	log := logger.GetLogger()
	manager := NewBFCPSessionManager(log)

	config := &bfcp.Config{
		ConferenceID: 12345,
		UserID:       1,
	}

	session1, _ := manager.CreateSession("call-1", config)
	session2, _ := manager.CreateSession("call-2", config)
	session3, _ := manager.CreateSession("call-3", config)

	session1.SetActive(true)
	session2.SetActive(false)
	session3.SetActive(true)

	active := manager.GetActiveSessions()
	if len(active) != 2 {
		t.Errorf("Expected 2 active sessions, got %d", len(active))
	}

	all := manager.GetAllSessions()
	if len(all) != 3 {
		t.Errorf("Expected 3 total sessions, got %d", len(all))
	}
}

func TestBFCPSessionManager_GetByFloor(t *testing.T) {
	log := logger.GetLogger()
	manager := NewBFCPSessionManager(log)

	config := &bfcp.Config{
		ConferenceID: 12345,
		UserID:       1,
	}

	session, _ := manager.CreateSession("call-123", config)
	session.SetActive(true)
	session.SetContentTrack("track-456", 2)

	found, exists := manager.GetSessionByFloor(2)
	if !exists {
		t.Fatal("Session should be found by floor")
	}

	if found.GetCallID() != "call-123" {
		t.Error("Found wrong session")
	}

	_, exists = manager.GetSessionByFloor(99)
	if exists {
		t.Error("Should not find session with non-existent floor")
	}
}

func TestBFCPSessionManager_Stats(t *testing.T) {
	log := logger.GetLogger()
	manager := NewBFCPSessionManager(log)

	config := &bfcp.Config{
		ConferenceID: 12345,
		UserID:       1,
	}

	session1, _ := manager.CreateSession("call-1", config)
	session2, _ := manager.CreateSession("call-2", config)

	session1.SetActive(true)
	session1.SetContentTrack("track-1", 1)
	session2.SetActive(false)

	stats := manager.GetStats()

	if stats["totalSessions"].(int) != 2 {
		t.Error("Stats should show 2 total sessions")
	}

	if stats["activeSessions"].(int) != 1 {
		t.Error("Stats should show 1 active session")
	}

	if stats["withTracks"].(int) != 1 {
		t.Error("Stats should show 1 session with track")
	}
}

func TestBFCPSessionManager_DestroyAll(t *testing.T) {
	log := logger.GetLogger()
	manager := NewBFCPSessionManager(log)

	config := &bfcp.Config{
		ConferenceID: 12345,
		UserID:       1,
	}

	manager.CreateSession("call-1", config)
	manager.CreateSession("call-2", config)
	manager.CreateSession("call-3", config)

	err := manager.DestroyAll()
	if err != nil {
		t.Errorf("Failed to destroy all sessions: %v", err)
	}

	all := manager.GetAllSessions()
	if len(all) != 0 {
		t.Errorf("Expected 0 sessions after DestroyAll, got %d", len(all))
	}
}
