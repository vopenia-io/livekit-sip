package media

import (
	"log"

	"github.com/pion/sdp/v3"
)

type ScreenHandler struct {
	// BFCP integration
}

func NewScreenHandler() *ScreenHandler {
	return &ScreenHandler{}
}

func (s *ScreenHandler) Process(media *sdp.MediaDescription) {
	log.Println("[POC-Screen] BFCP/Screenshare detected")

	// Check for BFCP attributes
	for _, attr := range media.Attributes {
		if attr.Key == "floorctrl" {
			log.Printf("[POC-Screen] Floor control: %s", attr.Value)
		}
		if attr.Key == "confid" {
			log.Printf("[POC-Screen] Conference ID: %s", attr.Value)
		}
	}

	log.Println("[POC-Screen] Would initialize BFCP client here")
}
