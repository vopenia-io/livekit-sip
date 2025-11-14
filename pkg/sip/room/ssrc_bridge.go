package room

import (
	"log"
	"sync"
)

type SSRCBridge struct {
	mu          sync.RWMutex
	sipToLK     map[uint32]uint32  // SIP SSRC -> LiveKit SSRC
	lkToSIP     map[uint32]uint32  // LiveKit SSRC -> SIP SSRC
	trackSSRCs  map[string]uint32  // TrackID -> SSRC
	callToTrack map[string]string  // CallID -> TrackID
}

func NewSSRCBridge() *SSRCBridge {
	return &SSRCBridge{
		sipToLK:     make(map[uint32]uint32),
		lkToSIP:     make(map[uint32]uint32),
		trackSSRCs:  make(map[string]uint32),
		callToTrack: make(map[string]string),
	}
}

func (s *SSRCBridge) MapSSRC(sipSSRC, livekitSSRC uint32, trackID, callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sipToLK[sipSSRC] = livekitSSRC
	s.lkToSIP[livekitSSRC] = sipSSRC
	s.trackSSRCs[trackID] = livekitSSRC
	s.callToTrack[callID] = trackID

	log.Printf("[POC-SSRCBridge] SSRC Mapped:")
	log.Printf("  SIP SSRC: %d -> LiveKit SSRC: %d", sipSSRC, livekitSSRC)
	log.Printf("  Track ID: %s", trackID)
	log.Printf("  Call ID: %s", callID)
	log.Printf("  Total mappings: %d", len(s.sipToLK))
}

func (s *SSRCBridge) GetLiveKitSSRC(sipSSRC uint32) (uint32, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lkSSRC, exists := s.sipToLK[sipSSRC]
	if exists {
		log.Printf("[POC-SSRCBridge] Lookup SIP->LK: %d -> %d", sipSSRC, lkSSRC)
	}
	return lkSSRC, exists
}

func (s *SSRCBridge) GetSIPSSRC(livekitSSRC uint32) (uint32, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sipSSRC, exists := s.lkToSIP[livekitSSRC]
	if exists {
		log.Printf("[POC-SSRCBridge] Lookup LK->SIP: %d -> %d", livekitSSRC, sipSSRC)
	}
	return sipSSRC, exists
}

func (s *SSRCBridge) RemoveTrackMapping(trackID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ssrc, exists := s.trackSSRCs[trackID]; exists {
		if sipSSRC, exists := s.lkToSIP[ssrc]; exists {
			delete(s.sipToLK, sipSSRC)
			delete(s.lkToSIP, ssrc)
			log.Printf("[POC-SSRCBridge] Removed mapping for track %s (LK SSRC: %d, SIP SSRC: %d)",
				trackID, ssrc, sipSSRC)
		}
		delete(s.trackSSRCs, trackID)
	}

	// Remove call to track mapping
	for callID, tid := range s.callToTrack {
		if tid == trackID {
			delete(s.callToTrack, callID)
			break
		}
	}
}

func (s *SSRCBridge) GetStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]interface{}{
		"total_mappings": len(s.sipToLK),
		"tracked_calls":  len(s.callToTrack),
		"tracked_ssrcs":  len(s.trackSSRCs),
	}
}

func (s *SSRCBridge) DumpMappings() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	log.Println("[POC-SSRCBridge] Current SSRC Mappings:")
	log.Println("================================")
	for sipSSRC, lkSSRC := range s.sipToLK {
		trackID := ""
		for tid, ssrc := range s.trackSSRCs {
			if ssrc == lkSSRC {
				trackID = tid
				break
			}
		}
		log.Printf("  SIP: %d <-> LK: %d (Track: %s)", sipSSRC, lkSSRC, trackID)
	}
	log.Println("================================")
}
