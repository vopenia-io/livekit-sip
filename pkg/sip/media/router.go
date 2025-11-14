package media

import (
	"log"
	"strings"

	"github.com/livekit/sip/pkg/sip/sdp"
	pionsdp "github.com/pion/sdp/v3"
)

type MediaMode int

const (
	MCU_MODE MediaMode = iota
	SFU_MODE
	HYBRID_MODE
)

type MediaRouter struct {
	audioProcessor  *AudioProcessor
	videoProcessor  *VideoProcessor
	videoTranscoder *VideoTranscoder
	screenHandler   *ScreenHandler
	currentMode     MediaMode
	answerBuilder   *sdp.AnswerBuilder
}

func NewMediaRouter() *MediaRouter {
	return &MediaRouter{
		audioProcessor:  NewAudioProcessor(),
		videoProcessor:  NewVideoProcessor(),
		videoTranscoder: NewVideoTranscoder(),
		screenHandler:   NewScreenHandler(),
		currentMode:     MCU_MODE, // Default audio-only
	}
}

func NewMediaRouterWithIP(localIP string) *MediaRouter {
	return &MediaRouter{
		audioProcessor:  NewAudioProcessor(),
		videoProcessor:  NewVideoProcessor(),
		videoTranscoder: NewVideoTranscoder(),
		screenHandler:   NewScreenHandler(),
		currentMode:     MCU_MODE,
		answerBuilder:   sdp.NewAnswerBuilder(localIP),
	}
}

func (m *MediaRouter) ProcessSDP(offer *pionsdp.SessionDescription) {
	log.Printf("[POC-MediaRouter] Processing SDP with %d media sections", len(offer.MediaDescriptions))

	hasAudio := false
	hasVideo := false

	for _, media := range offer.MediaDescriptions {
		log.Printf("[POC-MediaRouter] Media type: %s, Port: %d", media.MediaName.Media, media.MediaName.Port)

		switch media.MediaName.Media {
		case "audio":
			hasAudio = true
			log.Println("[POC-MediaRouter] Audio detected - delegating to AudioProcessor")
			m.audioProcessor.Process(media)

		case "video":
			hasVideo = true
			log.Println("[POC-MediaRouter] Video detected - delegating to VideoProcessor")
			m.videoProcessor.Process(media)

			// Log transcoding scenario (placeholder)
			sipCodec := m.extractVideoCodec(media)
			if sipCodec == "H264" {
				m.videoTranscoder.LogTranscodingScenario("poc-call", "sip_h264_livekit_vp8")
			} else if sipCodec == "VP8" {
				m.videoTranscoder.LogTranscodingScenario("poc-call", "sip_vp8_livekit_vp8")
			}

		case "application":
			// Check for BFCP
			for _, attr := range media.Attributes {
				if attr.Key == "floorctrl" {
					log.Println("[POC-MediaRouter] BFCP detected - delegating to ScreenHandler")
					m.screenHandler.Process(media)
				}
			}
		}
	}

	// Update mode based on media presence
	if hasAudio && hasVideo {
		m.currentMode = HYBRID_MODE
		log.Println("[POC-MediaRouter] Mode: HYBRID (Audio+Video)")
	} else if hasVideo {
		m.currentMode = SFU_MODE
		log.Println("[POC-MediaRouter] Mode: SFU (Video only)")
	} else {
		m.currentMode = MCU_MODE
		log.Println("[POC-MediaRouter] Mode: MCU (Audio only)")
	}
}

func (m *MediaRouter) extractVideoCodec(media *pionsdp.MediaDescription) string {
	for _, format := range media.MediaName.Formats {
		for _, attr := range media.Attributes {
			if attr.Key == "rtpmap" && strings.HasPrefix(attr.Value, format+" ") {
				codecInfo := strings.Split(attr.Value, "/")
				codecName := strings.Split(codecInfo[0], " ")[1]
				if strings.ToUpper(codecName) == "H264" {
					return "H264"
				} else if strings.ToUpper(codecName) == "VP8" {
					return "VP8"
				}
			}
		}
	}
	return "unknown"
}

func (m *MediaRouter) GetCurrentMode() MediaMode {
	return m.currentMode
}

// POC: Notification methods for video events
func (m *MediaRouter) NotifyVideoStart(callID string, ssrc uint32, codec string) {
	log.Printf("[POC-MediaRouter] Notifying video start: Call %s, SSRC %d, Codec %s",
		callID, ssrc, codec)

	// This would be connected to TrackManager in real implementation
	// For POC, just log the intent
	log.Printf("[POC-MediaRouter] Would register SIP track in TrackManager")
}

func (m *MediaRouter) NotifyVideoStop(callID string) {
	log.Printf("[POC-MediaRouter] Notifying video stop: Call %s", callID)

	// This would be connected to TrackManager
	log.Printf("[POC-MediaRouter] Would unregister SIP track in TrackManager")
}

func (m *MediaRouter) NotifyScreenshareStart(callID string, ssrc uint32) {
	log.Printf("[POC-MediaRouter] Notifying screenshare start: Call %s, SSRC %d",
		callID, ssrc)

	log.Printf("[POC-MediaRouter] Would register screenshare track in TrackManager")
}

func (m *MediaRouter) NotifyScreenshareStop(callID string) {
	log.Printf("[POC-MediaRouter] Notifying screenshare stop: Call %s", callID)

	log.Printf("[POC-MediaRouter] Would unregister screenshare track in TrackManager")
}

// POC: Generate SDP answer from offer
func (m *MediaRouter) GenerateAnswer(offer *pionsdp.SessionDescription, callID string) (*pionsdp.SessionDescription, error) {
	if m.answerBuilder == nil {
		log.Printf("[POC-MediaRouter] AnswerBuilder not initialized, cannot generate answer")
		return nil, nil
	}

	log.Printf("[POC-MediaRouter] Generating SDP answer for call %s", callID)

	// Use AnswerBuilder to create answer
	answer, err := m.answerBuilder.BuildAnswer(offer, callID)
	if err != nil {
		log.Printf("[POC-MediaRouter] Failed to build answer: %v", err)
		return nil, err
	}

	log.Printf("[POC-MediaRouter] Successfully generated SDP answer with %d media sections", len(answer.MediaDescriptions))
	return answer, nil
}
