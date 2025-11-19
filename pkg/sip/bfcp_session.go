package sip

import (
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/media-sdk/sdp/v2/bfcp"
	bfcpclient "github.com/vopenia/bfcp"
)

type BFCPSession struct {
	callID       string
	config       *bfcp.Config
	client       *bfcpclient.Client
	active       bool
	contentTrack string
	floorID      uint16
	createdAt    time.Time
	updatedAt    time.Time

	mu  sync.RWMutex
	log logger.Logger
}

func NewBFCPSession(callID string, config *bfcp.Config, log logger.Logger) *BFCPSession {
	return &BFCPSession{
		callID:    callID,
		config:    config,
		log:       log,
		createdAt: time.Now(),
		updatedAt: time.Now(),
	}
}

func (s *BFCPSession) SetClient(client *bfcpclient.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.client = client
	s.updatedAt = time.Now()

	s.log.Infow("🔵 [BFCP-Session] Client set",
		"callID", s.callID,
		"conferenceID", s.config.ConferenceID,
		"userID", s.config.UserID,
	)
}

func (s *BFCPSession) GetClient() *bfcpclient.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client
}

func (s *BFCPSession) SetActive(active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.active = active
	s.updatedAt = time.Now()

	s.log.Infow("🔵 [BFCP-Session] Session state changed",
		"callID", s.callID,
		"active", active,
	)
}

func (s *BFCPSession) IsActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

func (s *BFCPSession) SetContentTrack(trackID string, floorID uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.contentTrack = trackID
	s.floorID = floorID
	s.updatedAt = time.Now()

	s.log.Infow("🔵 [BFCP-Session] Content track mapped",
		"callID", s.callID,
		"trackID", trackID,
		"floorID", floorID,
	)
}

func (s *BFCPSession) GetContentTrack() (trackID string, floorID uint16) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contentTrack, s.floorID
}

func (s *BFCPSession) GetConfig() *bfcp.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

func (s *BFCPSession) GetCallID() string {
	return s.callID
}

func (s *BFCPSession) GetStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]interface{}{
		"callID":        s.callID,
		"active":        s.active,
		"hasClient":     s.client != nil,
		"hasTrack":      s.contentTrack != "",
		"floorID":       s.floorID,
		"conferenceID":  s.config.ConferenceID,
		"userID":        s.config.UserID,
		"createdAt":     s.createdAt,
		"updatedAt":     s.updatedAt,
		"age":           time.Since(s.createdAt),
	}
}

func (s *BFCPSession) Destroy() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log.Infow("🔵 [BFCP-Session] Destroying session",
		"callID", s.callID,
		"active", s.active,
		"hasClient", s.client != nil,
	)

	if s.client != nil {
		if err := s.client.Disconnect(); err != nil {
			s.log.Warnw("🔵 [BFCP-Session] Failed to disconnect client", err,
				"callID", s.callID,
			)
		}
		s.client = nil
	}

	s.active = false
	s.contentTrack = ""
	s.updatedAt = time.Now()

	s.log.Infow("🔵 [BFCP-Session] ✅ Session destroyed",
		"callID", s.callID,
	)

	return nil
}

type BFCPSessionManager struct {
	sessions map[string]*BFCPSession
	mu       sync.RWMutex
	log      logger.Logger
}

func NewBFCPSessionManager(log logger.Logger) *BFCPSessionManager {
	return &BFCPSessionManager{
		sessions: make(map[string]*BFCPSession),
		log:      log,
	}
}

func (m *BFCPSessionManager) CreateSession(callID string, config *bfcp.Config) (*BFCPSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[callID]; exists {
		return nil, fmt.Errorf("session already exists for call %s", callID)
	}

	session := NewBFCPSession(callID, config, m.log)
	m.sessions[callID] = session

	m.log.Infow("🔵 [BFCP-SessionMgr] Session created",
		"callID", callID,
		"conferenceID", config.ConferenceID,
		"userID", config.UserID,
		"floorID", config.FloorID,
	)

	return session, nil
}

func (m *BFCPSessionManager) GetSession(callID string) (*BFCPSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, exists := m.sessions[callID]
	return session, exists
}

func (m *BFCPSessionManager) UpdateSession(callID string, config *bfcp.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[callID]
	if !exists {
		return fmt.Errorf("session not found for call %s", callID)
	}

	session.config = config
	session.updatedAt = time.Now()

	m.log.Infow("🔵 [BFCP-SessionMgr] Session updated",
		"callID", callID,
		"conferenceID", config.ConferenceID,
	)

	return nil
}

func (m *BFCPSessionManager) DestroySession(callID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[callID]
	if !exists {
		return fmt.Errorf("session not found for call %s", callID)
	}

	if err := session.Destroy(); err != nil {
		return fmt.Errorf("failed to destroy session: %w", err)
	}

	delete(m.sessions, callID)

	m.log.Infow("🔵 [BFCP-SessionMgr] ✅ Session removed",
		"callID", callID,
		"remainingSessions", len(m.sessions),
	)

	return nil
}

func (m *BFCPSessionManager) GetActiveSessions() []*BFCPSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	active := make([]*BFCPSession, 0)
	for _, session := range m.sessions {
		if session.IsActive() {
			active = append(active, session)
		}
	}

	return active
}

func (m *BFCPSessionManager) GetAllSessions() []*BFCPSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*BFCPSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}

	return sessions
}

func (m *BFCPSessionManager) GetSessionByFloor(floorID uint16) (*BFCPSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, session := range m.sessions {
		if session.IsActive() {
			_, sessionFloor := session.GetContentTrack()
			if sessionFloor == floorID {
				return session, true
			}
		}
	}

	return nil, false
}

func (m *BFCPSessionManager) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	activeSessions := 0
	withClients := 0
	withTracks := 0

	for _, session := range m.sessions {
		if session.IsActive() {
			activeSessions++
		}
		if session.GetClient() != nil {
			withClients++
		}
		track, _ := session.GetContentTrack()
		if track != "" {
			withTracks++
		}
	}

	return map[string]interface{}{
		"totalSessions":  len(m.sessions),
		"activeSessions": activeSessions,
		"withClients":    withClients,
		"withTracks":     withTracks,
	}
}

func (m *BFCPSessionManager) DestroyAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Infow("🔵 [BFCP-SessionMgr] Destroying all sessions",
		"count", len(m.sessions),
	)

	for callID, session := range m.sessions {
		if err := session.Destroy(); err != nil {
			m.log.Warnw("🔵 [BFCP-SessionMgr] Failed to destroy session", err,
				"callID", callID,
			)
		}
	}

	m.sessions = make(map[string]*BFCPSession)

	m.log.Infow("🔵 [BFCP-SessionMgr] ✅ All sessions destroyed")

	return nil
}
