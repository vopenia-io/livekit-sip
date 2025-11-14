package reinvite

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/livekit/sip/pkg/sip/sdp"
	"github.com/livekit/sipgo/sip"
	pionsdp "github.com/pion/sdp/v3"
)

type Request struct {
	CallID    string
	SDP       string
	Timestamp time.Time
}

type Handler struct {
	requestQueue chan *Request
	mediaState   *MediaState
	answerSender *sdp.AnswerSender
}

func NewHandler() *Handler {
	return &Handler{
		requestQueue: make(chan *Request, 10),
		mediaState:   NewMediaState(),
	}
}

func NewHandlerWithAnswerSender(answerSender *sdp.AnswerSender) *Handler {
	return &Handler{
		requestQueue: make(chan *Request, 10),
		mediaState:   NewMediaState(),
		answerSender: answerSender,
	}
}

func (h *Handler) SetAnswerSender(answerSender *sdp.AnswerSender) {
	h.answerSender = answerSender
}

func (h *Handler) HandleReInvite(invite *sip.Request) (*pionsdp.SessionDescription, error) {
	log.Printf("[POC-ReInvite] Received re-INVITE for call: %s", invite.CallID())

	// Parse SDP
	sdpContent := extractSDP(invite)
	log.Printf("[POC-ReInvite] SDP Content:\n%s", sdpContent)

	// Parse SDP offer
	var offer *pionsdp.SessionDescription
	var err error
	if sdpContent != "" {
		offer = &pionsdp.SessionDescription{}
		err = offer.Unmarshal([]byte(sdpContent))
		if err != nil {
			log.Printf("[POC-ReInvite] Failed to parse SDP: %v", err)
			return nil, err
		}
	}

	// Detect media changes
	oldState := h.mediaState.GetCurrent()
	newState := h.detectMediaState(sdpContent)

	if oldState.VideoActive != newState.VideoActive {
		if newState.VideoActive {
			log.Println("[POC-ReInvite] VIDEO STARTED - Camera turned ON")
			log.Println("[POC-ReInvite] Notifying room components")
			// Extract SSRC and codec from SDP
			ssrc := h.extractSSRCFromSDP(sdpContent)
			codec := h.extractVideoCodec(sdpContent)
			// Notify room components (would be wired in real implementation)
			log.Printf("[POC-ReInvite] Would notify TrackManager: SSRC %d, Codec %s", ssrc, codec)
		} else {
			log.Println("[POC-ReInvite] VIDEO STOPPED - Camera turned OFF")
			log.Println("[POC-ReInvite] Notifying room components")
			// Notify room components
			log.Printf("[POC-ReInvite] Would notify TrackManager to stop video track")
		}
	}

	if oldState.ScreenActive != newState.ScreenActive {
		if newState.ScreenActive {
			log.Println("[POC-ReInvite] SCREENSHARE STARTED")
			log.Println("[POC-ReInvite] Notifying room components")
			ssrc := h.extractSSRCFromSDP(sdpContent)
			log.Printf("[POC-ReInvite] Would notify TrackManager: Screenshare SSRC %d", ssrc)
		} else {
			log.Println("[POC-ReInvite] SCREENSHARE STOPPED")
			log.Println("[POC-ReInvite] Notifying room components")
			log.Printf("[POC-ReInvite] Would notify TrackManager to stop screenshare track")
		}
	}

	// Update state
	h.mediaState.Update(newState)

	// Generate SDP answer if AnswerSender is available
	var answer *pionsdp.SessionDescription
	if h.answerSender != nil && offer != nil {
		log.Printf("[POC-ReInvite] Generating re-INVITE answer via AnswerSender")
		answer, err = h.answerSender.PrepareReInviteAnswer(invite.CallID().String(), offer)
		if err != nil {
			log.Printf("[POC-ReInvite] Failed to generate answer: %v", err)
			return nil, err
		}
		log.Printf("[POC-ReInvite] Successfully generated re-INVITE answer")
	} else {
		log.Printf("[POC-ReInvite] No AnswerSender available or no offer, skipping answer generation")
	}

	return answer, nil
}

func (h *Handler) detectMediaState(sdp string) *MediaStateInfo {
	state := &MediaStateInfo{
		AudioActive:  false,
		VideoActive:  false,
		ScreenActive: false,
	}

	// Simple detection logic for POC
	if strings.Contains(sdp, "m=audio") && !strings.Contains(sdp, "m=audio 0") {
		state.AudioActive = true
	}
	if strings.Contains(sdp, "m=video") && !strings.Contains(sdp, "m=video 0") {
		state.VideoActive = true

		// Check for screenshare indicators
		if strings.Contains(sdp, "a=content:slides") ||
			strings.Contains(sdp, "a=floorctrl") {
			state.ScreenActive = true
		}
	}

	return state
}

func extractSDP(req *sip.Request) string {
	// Extract SDP from request body
	if req.Body() != nil {
		return string(req.Body())
	}
	return ""
}

func (h *Handler) GetMediaState() *MediaState {
	return h.mediaState
}

// POC: Helper methods for extracting media info from SDP
func (h *Handler) extractSSRCFromSDP(sdp string) uint32 {
	// Simple SSRC extraction from SDP (placeholder)
	// In real implementation, would parse SDP properly
	lines := strings.Split(sdp, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "a=ssrc:") {
			var ssrc uint32
			parts := strings.SplitN(strings.TrimPrefix(line, "a=ssrc:"), " ", 2)
			if len(parts) > 0 {
				_, err := fmt.Sscanf(parts[0], "%d", &ssrc)
				if err == nil && ssrc > 0 {
					log.Printf("[POC-ReInvite] Extracted SSRC: %d", ssrc)
					return ssrc
				}
			}
		}
	}

	// Generate placeholder if not found
	placeholderSSRC := uint32(2000000)
	log.Printf("[POC-ReInvite] No SSRC found in SDP, using placeholder: %d", placeholderSSRC)
	return placeholderSSRC
}

func (h *Handler) extractVideoCodec(sdp string) string {
	// Simple codec extraction from SDP
	if strings.Contains(sdp, "H264") || strings.Contains(sdp, "h264") {
		log.Printf("[POC-ReInvite] Detected video codec: H264")
		return "H264"
	}
	if strings.Contains(sdp, "VP8") || strings.Contains(sdp, "vp8") {
		log.Printf("[POC-ReInvite] Detected video codec: VP8")
		return "VP8"
	}
	if strings.Contains(sdp, "H265") || strings.Contains(sdp, "h265") {
		log.Printf("[POC-ReInvite] Detected video codec: H265")
		return "H265"
	}

	log.Printf("[POC-ReInvite] Could not detect video codec")
	return "unknown"
}
