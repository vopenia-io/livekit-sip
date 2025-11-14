package media

import (
	"log"

	"github.com/pion/sdp/v3"
)

type AudioProcessor struct {
	// Delegates to existing implementation
}

func NewAudioProcessor() *AudioProcessor {
	return &AudioProcessor{}
}

func (a *AudioProcessor) Process(media *sdp.MediaDescription) {
	log.Printf("[POC-Audio] Processing audio media: %d codecs detected", len(media.MediaName.Formats))
	// Delegate to existing audio handling
}
