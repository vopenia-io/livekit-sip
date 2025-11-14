package room

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
)

type MediaType int

const (
	AUDIO MediaType = iota
	VIDEO
	SCREEN
)

func (m MediaType) String() string {
	switch m {
	case AUDIO:
		return "AUDIO"
	case VIDEO:
		return "VIDEO"
	case SCREEN:
		return "SCREEN"
	default:
		return "UNKNOWN"
	}
}

type Direction int

const (
	SEND Direction = iota
	RECV
	SENDRECV
)

type TrackManager struct {
	mu             sync.RWMutex
	sipTracks      map[string]*SIPTrack      // CallID -> Track
	livekitTracks  map[string]*LiveKitTrack  // TrackID -> Track
	participantMap map[string]*ParticipantInfo
	ssrcBridge     *SSRCBridge
	eventLogger    *MediaEventLogger
}

type SIPTrack struct {
	CallID    string
	MediaType MediaType
	SSRC      uint32
	Codec     string
	Direction Direction
	Active    bool
	CreatedAt time.Time
}

type LiveKitTrack struct {
	TrackID             string
	TrackSID            string
	ParticipantSID      string
	ParticipantIdentity string
	MediaType           MediaType
	SSRC                uint32
	Codec               string
	Active              bool
	StartTime           time.Time
}

type ParticipantInfo struct {
	SID      string
	Identity string
	CallID   string
	Tracks   []string // Track IDs
}

func NewTrackManager() *TrackManager {
	return &TrackManager{
		sipTracks:      make(map[string]*SIPTrack),
		livekitTracks:  make(map[string]*LiveKitTrack),
		participantMap: make(map[string]*ParticipantInfo),
	}
}

func (t *TrackManager) SetSSRCBridge(bridge *SSRCBridge) {
	t.ssrcBridge = bridge
}

func (t *TrackManager) SetEventLogger(logger *MediaEventLogger) {
	t.eventLogger = logger
}

func (t *TrackManager) RegisterSIPTrack(callID string, mediaType MediaType, ssrc uint32, codec string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	track := &SIPTrack{
		CallID:    callID,
		MediaType: mediaType,
		SSRC:      ssrc,
		Codec:     codec,
		Direction: SENDRECV,
		Active:    true,
		CreatedAt: time.Now(),
	}

	t.sipTracks[callID] = track

	log.Printf("[POC-TrackManager] SIP track registered - CallID: %s, Type: %v, SSRC: %d, Codec: %s",
		callID, mediaType, ssrc, codec)

	// Log event
	if t.eventLogger != nil {
		if mediaType == VIDEO {
			t.eventLogger.LogVideoStateChange(callID, true)
		} else if mediaType == SCREEN {
			t.eventLogger.LogScreenshareStateChange(callID, true)
		}
	}
}

func (t *TrackManager) UnregisterSIPTrack(callID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if track, exists := t.sipTracks[callID]; exists {
		track.Active = false

		log.Printf("[POC-TrackManager] SIP track unregistered - CallID: %s, Type: %v, Duration: %v",
			callID, track.MediaType, time.Since(track.CreatedAt))

		// Log event
		if t.eventLogger != nil {
			if track.MediaType == VIDEO {
				t.eventLogger.LogVideoStateChange(callID, false)
			} else if track.MediaType == SCREEN {
				t.eventLogger.LogScreenshareStateChange(callID, false)
			}
		}

		delete(t.sipTracks, callID)
	}
}

func (t *TrackManager) HandleTrackPublished(track *livekit.TrackInfo, participantIdentity string, participantSID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Extract SSRC (placeholder - would use real extraction)
	ssrc := t.extractSSRCFromTrack(track)

	// Determine media type
	mediaType := t.detectMediaType(track)

	lkTrack := &LiveKitTrack{
		TrackID:             track.Sid,
		TrackSID:            track.Sid,
		ParticipantSID:      participantSID,
		ParticipantIdentity: participantIdentity,
		MediaType:           mediaType,
		SSRC:                ssrc,
		Codec:               track.MimeType,
		Active:              true,
		StartTime:           time.Now(),
	}

	t.livekitTracks[track.Sid] = lkTrack

	log.Printf("[POC-TrackManager] LiveKit track published - ID: %s, Type: %v, SSRC: %d, Participant: %s",
		track.Sid, mediaType, ssrc, participantIdentity)

	// Map SSRCs if we have a matching SIP track
	if t.ssrcBridge != nil {
		// Find matching SIP track by participant identity (which might be the call ID)
		for callID, sipTrack := range t.sipTracks {
			if sipTrack.MediaType == mediaType && sipTrack.Active {
				t.ssrcBridge.MapSSRC(sipTrack.SSRC, ssrc, track.Sid, callID)
				log.Printf("[POC-TrackManager] SSRC mapping created: SIP %d <-> LiveKit %d",
					sipTrack.SSRC, ssrc)
				break
			}
		}
	}

	// Log media event
	if t.eventLogger != nil {
		t.eventLogger.LogTrackPublished(track.Sid, mediaType, ssrc)
	}
}

func (t *TrackManager) HandleTrackUnpublished(trackSID string, participantIdentity string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if lkTrack, exists := t.livekitTracks[trackSID]; exists {
		lkTrack.Active = false

		log.Printf("[POC-TrackManager] LiveKit track unpublished - ID: %s, Type: %v, Duration: %v",
			trackSID, lkTrack.MediaType, time.Since(lkTrack.StartTime))

		// Remove SSRC mapping
		if t.ssrcBridge != nil {
			t.ssrcBridge.RemoveTrackMapping(trackSID)
		}

		// Log media event
		if t.eventLogger != nil {
			t.eventLogger.LogTrackUnpublished(trackSID, lkTrack.MediaType)

			// Log state change if it's video or screen
			if lkTrack.MediaType == VIDEO {
				// Find associated call ID
				callID := t.findCallIDForTrack(trackSID)
				if callID != "" {
					t.eventLogger.LogVideoStateChange(callID, false)
				}
			} else if lkTrack.MediaType == SCREEN {
				callID := t.findCallIDForTrack(trackSID)
				if callID != "" {
					t.eventLogger.LogScreenshareStateChange(callID, false)
				}
			}
		}

		delete(t.livekitTracks, trackSID)
	}
}

func (t *TrackManager) extractSSRCFromTrack(track *livekit.TrackInfo) uint32 {
	// Placeholder: Generate SSRC from track SID
	// In real implementation, would extract from track's RTP parameters
	hash := uint32(0)
	for _, b := range []byte(track.Sid) {
		hash = hash*31 + uint32(b)
	}
	ssrc := 0x10000000 | (hash & 0x0FFFFFFF) // Ensure it looks like a LiveKit SSRC
	log.Printf("[POC-TrackManager] Generated placeholder SSRC %d for track %s", ssrc, track.Sid)
	return ssrc
}

func (t *TrackManager) detectMediaType(track *livekit.TrackInfo) MediaType {
	// Simple detection based on track type and name
	if track.Type == livekit.TrackType_AUDIO {
		return AUDIO
	}

	// Check if it's screenshare (might have specific naming pattern or source)
	if track.Name != "" &&
		(strings.Contains(strings.ToLower(track.Name), "screen") ||
			strings.Contains(strings.ToLower(track.Name), "share")) {
		return SCREEN
	}

	// Check source type
	if track.Source == livekit.TrackSource_SCREEN_SHARE ||
		track.Source == livekit.TrackSource_SCREEN_SHARE_AUDIO {
		return SCREEN
	}

	return VIDEO
}

func (t *TrackManager) findCallIDForTrack(trackID string) string {
	// Find call ID associated with this track
	// This would be mapped through participant identity
	if track, exists := t.livekitTracks[trackID]; exists {
		// Participant identity might be the call ID or contain it
		return track.ParticipantIdentity
	}
	return ""
}

func (t *TrackManager) GetStats() map[string]interface{} {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return map[string]interface{}{
		"sip_tracks":      len(t.sipTracks),
		"livekit_tracks":  len(t.livekitTracks),
		"participants":    len(t.participantMap),
	}
}
