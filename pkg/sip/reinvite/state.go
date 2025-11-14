package reinvite

import (
	"log"
	"sync"
	"time"
)

type MediaStateInfo struct {
	AudioActive  bool
	VideoActive  bool
	ScreenActive bool
	LastUpdated  time.Time
}

type MediaState struct {
	mu      sync.RWMutex
	current *MediaStateInfo
	history []MediaStateInfo
}

func NewMediaState() *MediaState {
	return &MediaState{
		current: &MediaStateInfo{
			AudioActive:  false,
			VideoActive:  false,
			ScreenActive: false,
			LastUpdated:  time.Now(),
		},
		history: make([]MediaStateInfo, 0),
	}
}

func (m *MediaState) GetCurrent() MediaStateInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return *m.current
}

func (m *MediaState) Update(new *MediaStateInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Save to history
	m.history = append(m.history, *m.current)

	// Update current
	new.LastUpdated = time.Now()
	m.current = new

	log.Printf("[POC-State] Updated - Audio: %v, Video: %v, Screen: %v",
		new.AudioActive, new.VideoActive, new.ScreenActive)
}
