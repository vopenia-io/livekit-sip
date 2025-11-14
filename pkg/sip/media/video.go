package media

import (
	"fmt"
	"log"
	"strings"

	"github.com/pion/sdp/v3"
)

type VideoProcessor struct {
	// SFU-mode video handling with transcoding detection
	transcoder   *VideoTranscoder
	sipCodec     string
	livekitCodec string
}

func NewVideoProcessor() *VideoProcessor {
	return &VideoProcessor{
		transcoder:   NewVideoTranscoder(),
		livekitCodec: "VP8", // LiveKit default preference
	}
}

func (v *VideoProcessor) Process(media *sdp.MediaDescription) {
	log.Printf("[POC-Video] Processing video media")

	// Parse video codecs
	detectedCodecs := []string{}
	var ssrc uint32

	for _, format := range media.MediaName.Formats {
		for _, attr := range media.Attributes {
			if attr.Key == "rtpmap" && strings.HasPrefix(attr.Value, format+" ") {
				codecInfo := strings.Split(attr.Value, "/")
				codecName := strings.Split(codecInfo[0], " ")[1]
				detectedCodecs = append(detectedCodecs, codecName)
				log.Printf("[POC-Video] Codec detected: %s", codecName)
			}
		}
	}

	// Parse SSRC
	for _, attr := range media.Attributes {
		if attr.Key == "ssrc" {
			parts := strings.SplitN(attr.Value, " ", 2)
			if len(parts) > 0 {
				// Simple parsing for POC
				fmt.Sscanf(parts[0], "%d", &ssrc)
				log.Printf("[POC-Video] SSRC detected: %d", ssrc)
			}
		}
	}

	// Determine SIP codec
	if len(detectedCodecs) > 0 {
		v.sipCodec = detectedCodecs[0]
		if v.sipCodec == "h264" {
			v.sipCodec = "H264"
		} else if v.sipCodec == "vp8" {
			v.sipCodec = "VP8"
		}
	}

	// Check transcoding needs (placeholder)
	mode := v.transcoder.CheckTranscodingNeeded(v.sipCodec, v.livekitCodec)

	// Log what would happen
	switch mode {
	case PASSTHROUGH:
		log.Printf("[POC-Video] Would setup direct SFU forwarding (no transcoding)")
	case TRANSCODE_H264_TO_VP8:
		log.Printf("[POC-Video] Would setup H264->VP8 transcoding pipeline")
		if ssrc > 0 {
			v.transcoder.LogSSRCRewriting(ssrc)
		}
	case TRANSCODE_VP8_TO_H264:
		log.Printf("[POC-Video] Would setup VP8->H264 transcoding pipeline")
		if ssrc > 0 {
			v.transcoder.LogSSRCRewriting(ssrc)
		}
	}
}
