package room

import (
	"log"
	"sync"
	"time"
)

type EventType int

const (
	VIDEO_STARTED EventType = iota
	VIDEO_STOPPED
	SCREENSHARE_STARTED
	SCREENSHARE_STOPPED
	TRACK_PUBLISHED
	TRACK_UNPUBLISHED
	TRACK_SUBSCRIBED
	TRACK_UNSUBSCRIBED
	SSRC_MAPPED
	CODEC_NEGOTIATED
)

func (e EventType) String() string {
	names := []string{
		"VIDEO_STARTED",
		"VIDEO_STOPPED",
		"SCREENSHARE_STARTED",
		"SCREENSHARE_STOPPED",
		"TRACK_PUBLISHED",
		"TRACK_UNPUBLISHED",
		"TRACK_SUBSCRIBED",
		"TRACK_UNSUBSCRIBED",
		"SSRC_MAPPED",
		"CODEC_NEGOTIATED",
	}
	if int(e) < len(names) {
		return names[e]
	}
	return "UNKNOWN"
}

type MediaEvent struct {
	Timestamp time.Time
	EventType EventType
	CallID    string
	TrackID   string
	MediaType MediaType
	SSRC      uint32
	Details   map[string]interface{}
}

type MediaEventLogger struct {
	mu         sync.Mutex
	events     []MediaEvent
	eventQueue chan MediaEvent
	running    bool
	stopCh     chan struct{}
}

func NewMediaEventLogger() *MediaEventLogger {
	logger := &MediaEventLogger{
		events:     make([]MediaEvent, 0, 1000),
		eventQueue: make(chan MediaEvent, 100),
		stopCh:     make(chan struct{}),
		running:    true,
	}

	// Start event processor
	go logger.processEvents()

	log.Println("[POC-EventLogger] Media event logger started")
	return logger
}

func (m *MediaEventLogger) processEvents() {
	for {
		select {
		case event := <-m.eventQueue:
			m.mu.Lock()
			m.events = append(m.events, event)
			m.logEvent(event)
			m.mu.Unlock()

		case <-m.stopCh:
			log.Println("[POC-EventLogger] Shutting down event logger")
			return
		}
	}
}

func (m *MediaEventLogger) logEvent(event MediaEvent) {
	// Format event for logging
	log.Printf("[POC-MediaEvent] %s | %s | CallID: %s | TrackID: %s | MediaType: %v | SSRC: %d",
		event.Timestamp.Format("15:04:05.000"),
		event.EventType,
		event.CallID,
		event.TrackID,
		event.MediaType,
		event.SSRC)

	// Log additional details if present
	if len(event.Details) > 0 {
		for key, value := range event.Details {
			log.Printf("  %s: %v", key, value)
		}
	}

	// Special formatting for state changes
	switch event.EventType {
	case VIDEO_STARTED:
		log.Printf("🎥 VIDEO STARTED for Call %s", event.CallID)
	case VIDEO_STOPPED:
		log.Printf("📵 VIDEO STOPPED for Call %s", event.CallID)
	case SCREENSHARE_STARTED:
		log.Printf("🖥️  SCREENSHARE STARTED for Call %s", event.CallID)
	case SCREENSHARE_STOPPED:
		log.Printf("⬜ SCREENSHARE STOPPED for Call %s", event.CallID)
	}
}

func (m *MediaEventLogger) LogVideoStateChange(callID string, started bool) {
	eventType := VIDEO_STOPPED
	if started {
		eventType = VIDEO_STARTED
	}

	m.eventQueue <- MediaEvent{
		Timestamp: time.Now(),
		EventType: eventType,
		CallID:    callID,
		MediaType: VIDEO,
	}
}

func (m *MediaEventLogger) LogScreenshareStateChange(callID string, started bool) {
	eventType := SCREENSHARE_STOPPED
	if started {
		eventType = SCREENSHARE_STARTED
	}

	m.eventQueue <- MediaEvent{
		Timestamp: time.Now(),
		EventType: eventType,
		CallID:    callID,
		MediaType: SCREEN,
	}
}

func (m *MediaEventLogger) LogTrackPublished(trackID string, mediaType MediaType, ssrc uint32) {
	m.eventQueue <- MediaEvent{
		Timestamp: time.Now(),
		EventType: TRACK_PUBLISHED,
		TrackID:   trackID,
		MediaType: mediaType,
		SSRC:      ssrc,
	}
}

func (m *MediaEventLogger) LogTrackUnpublished(trackID string, mediaType MediaType) {
	m.eventQueue <- MediaEvent{
		Timestamp: time.Now(),
		EventType: TRACK_UNPUBLISHED,
		TrackID:   trackID,
		MediaType: mediaType,
	}
}

func (m *MediaEventLogger) GetEventHistory() []MediaEvent {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return copy of events
	history := make([]MediaEvent, len(m.events))
	copy(history, m.events)
	return history
}

func (m *MediaEventLogger) PrintSummary() {
	m.mu.Lock()
	defer m.mu.Unlock()

	videoStarts := 0
	videoStops := 0
	screenshareStarts := 0
	screenshareStops := 0
	tracksPublished := 0
	tracksUnpublished := 0

	for _, event := range m.events {
		switch event.EventType {
		case VIDEO_STARTED:
			videoStarts++
		case VIDEO_STOPPED:
			videoStops++
		case SCREENSHARE_STARTED:
			screenshareStarts++
		case SCREENSHARE_STOPPED:
			screenshareStops++
		case TRACK_PUBLISHED:
			tracksPublished++
		case TRACK_UNPUBLISHED:
			tracksUnpublished++
		}
	}

	log.Println("[POC-EventLogger] Event Summary:")
	log.Println("=================================")
	log.Printf("  Video Started:      %d", videoStarts)
	log.Printf("  Video Stopped:      %d", videoStops)
	log.Printf("  Screen Started:     %d", screenshareStarts)
	log.Printf("  Screen Stopped:     %d", screenshareStops)
	log.Printf("  Tracks Published:   %d", tracksPublished)
	log.Printf("  Tracks Unpublished: %d", tracksUnpublished)
	log.Printf("  Total Events:       %d", len(m.events))
	log.Println("=================================")
}

func (m *MediaEventLogger) Stop() {
	m.running = false
	close(m.stopCh)
}
