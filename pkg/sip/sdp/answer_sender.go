package sdp

import (
	"log"
	"sync"
	"time"

	"github.com/pion/sdp/v3"
)

// AnswerState represents the state of an SDP answer
type AnswerState int

const (
	ANSWER_PENDING AnswerState = iota
	ANSWER_GENERATING
	ANSWER_READY
	ANSWER_SENT
	ANSWER_CONFIRMED
)

func (s AnswerState) String() string {
	states := []string{
		"PENDING",
		"GENERATING",
		"READY",
		"SENT",
		"CONFIRMED",
	}
	if int(s) < len(states) {
		return states[s]
	}
	return "UNKNOWN"
}

type AnswerSender struct {
	mu             sync.RWMutex
	answerBuilder  *AnswerBuilder
	pendingAnswers map[string]*PendingAnswer
}

type PendingAnswer struct {
	CallID        string
	TransactionID string
	AnswerSDP     *sdp.SessionDescription
	State         AnswerState
	CreatedAt     time.Time
	SentAt        time.Time
	ConfirmedAt   time.Time
}

func NewAnswerSender(builder *AnswerBuilder) *AnswerSender {
	return &AnswerSender{
		answerBuilder:  builder,
		pendingAnswers: make(map[string]*PendingAnswer),
	}
}

func (s *AnswerSender) PrepareInitialAnswer(callID string, offer *sdp.SessionDescription) (*sdp.SessionDescription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log.Printf("[POC-AnswerSender] Preparing initial answer for Call: %s", callID)

	// Track as pending
	pending := &PendingAnswer{
		CallID:    callID,
		State:     ANSWER_GENERATING,
		CreatedAt: time.Now(),
	}
	s.pendingAnswers[callID] = pending

	// Build answer
	answer, err := s.answerBuilder.BuildAnswer(offer, callID)
	if err != nil {
		log.Printf("[POC-AnswerSender] Failed to build answer: %v", err)
		pending.State = ANSWER_PENDING
		return nil, err
	}

	pending.AnswerSDP = answer
	pending.State = ANSWER_READY

	log.Printf("[POC-AnswerSender] Answer ready for Call: %s", callID)
	s.logAnswerSummary(answer)

	return answer, nil
}

func (s *AnswerSender) MarkAnswerSent(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if pending, exists := s.pendingAnswers[callID]; exists {
		pending.State = ANSWER_SENT
		pending.SentAt = time.Now()

		log.Printf("[POC-AnswerSender] Answer marked as SENT for Call: %s", callID)
		log.Printf("  Generation->Send delay: %v", pending.SentAt.Sub(pending.CreatedAt))
	}
}

func (s *AnswerSender) OnACKReceived(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if pending, exists := s.pendingAnswers[callID]; exists {
		pending.State = ANSWER_CONFIRMED
		pending.ConfirmedAt = time.Now()

		log.Printf("[POC-AnswerSender] ACK received for Call: %s", callID)
		if !pending.SentAt.IsZero() {
			log.Printf("  Answer->ACK delay: %v", pending.ConfirmedAt.Sub(pending.SentAt))
		}

		// Clean up confirmed answer after a delay
		go func() {
			time.Sleep(30 * time.Second)
			s.mu.Lock()
			delete(s.pendingAnswers, callID)
			s.mu.Unlock()
			log.Printf("[POC-AnswerSender] Cleaned up answer state for Call: %s", callID)
		}()
	}
}

func (s *AnswerSender) PrepareReInviteAnswer(callID string, offer *sdp.SessionDescription) (*sdp.SessionDescription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log.Printf("[POC-AnswerSender] Preparing re-INVITE answer for Call: %s", callID)

	// Get existing answer if available
	var oldAnswer *sdp.SessionDescription
	if existing, exists := s.pendingAnswers[callID]; exists {
		oldAnswer = existing.AnswerSDP
	}

	// Update pending state
	pending := &PendingAnswer{
		CallID:    callID,
		State:     ANSWER_GENERATING,
		CreatedAt: time.Now(),
	}
	s.pendingAnswers[callID] = pending

	// Build updated answer
	answer, err := s.answerBuilder.BuildAnswer(offer, callID)
	if err != nil {
		log.Printf("[POC-AnswerSender] Failed to build re-INVITE answer: %v", err)
		return nil, err
	}

	pending.AnswerSDP = answer
	pending.State = ANSWER_READY

	// Log changes
	if oldAnswer != nil {
		s.logMediaChanges(oldAnswer, answer)
	}

	log.Printf("[POC-AnswerSender] Re-INVITE answer ready for Call: %s", callID)

	return answer, nil
}

func (s *AnswerSender) logAnswerSummary(answer *sdp.SessionDescription) {
	log.Printf("[POC-AnswerSender] Answer Summary:")

	for _, media := range answer.MediaDescriptions {
		if media.MediaName.Port.Value > 0 {
			// Extract codec
			codec := "unknown"
			for _, attr := range media.Attributes {
				if attr.Key == "rtpmap" {
					codec = attr.Value
					break
				}
			}

			log.Printf("  ✅ %s: Port %d, Codec: %s (ACTIVE)",
				media.MediaName.Media, media.MediaName.Port.Value, codec)
		} else {
			log.Printf("  ❌ %s: REJECTED (port 0)", media.MediaName.Media)
		}
	}
}

func (s *AnswerSender) logMediaChanges(oldAnswer, newAnswer *sdp.SessionDescription) {
	log.Printf("[POC-AnswerSender] Re-INVITE media changes:")

	for i, newMedia := range newAnswer.MediaDescriptions {
		if i < len(oldAnswer.MediaDescriptions) {
			oldMedia := oldAnswer.MediaDescriptions[i]

			oldActive := oldMedia.MediaName.Port.Value > 0
			newActive := newMedia.MediaName.Port.Value > 0

			if oldActive != newActive {
				if newActive {
					log.Printf("  🎥 %s STARTED (now port %d)",
						newMedia.MediaName.Media, newMedia.MediaName.Port.Value)
				} else {
					log.Printf("  📵 %s STOPPED (was port %d)",
						newMedia.MediaName.Media, oldMedia.MediaName.Port.Value)
				}
			} else if newActive {
				log.Printf("  ↔️  %s UNCHANGED (port %d)",
					newMedia.MediaName.Media, newMedia.MediaName.Port.Value)
			}
		}
	}
}

func (s *AnswerSender) GetAnswerState(callID string) (AnswerState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if pending, exists := s.pendingAnswers[callID]; exists {
		return pending.State, true
	}
	return ANSWER_PENDING, false
}

func (s *AnswerSender) GetPendingAnswer(callID string) (*sdp.SessionDescription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if pending, exists := s.pendingAnswers[callID]; exists {
		return pending.AnswerSDP, true
	}
	return nil, false
}

func (s *AnswerSender) GetStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]interface{}{
		"pending_answers": len(s.pendingAnswers),
	}
}
