package sdp

import (
	"fmt"
	"log"
	"net/netip"

	sdksdp "github.com/livekit/media-sdk/sdp/v2"
	pionsdp "github.com/pion/sdp/v3"
)

// AnswerBuilder uses media-sdk's SDP v2 API for answer generation
type AnswerBuilder struct {
	localIP netip.Addr
}

func NewAnswerBuilder(localIP string) *AnswerBuilder {
	addr, err := netip.ParseAddr(localIP)
	if err != nil {
		log.Printf("[POC-SDPAnswer] Invalid IP address: %s, using 0.0.0.0", localIP)
		addr = netip.IPv4Unspecified()
	}
	return &AnswerBuilder{
		localIP: addr,
	}
}

// BuildAnswer generates a complete SDP answer from an offer using media-sdk
func (a *AnswerBuilder) BuildAnswer(offer *pionsdp.SessionDescription, callID string) (*pionsdp.SessionDescription, error) {
	log.Printf("[POC-SDPAnswer] Building answer for call %s using media-sdk", callID)

	// Parse offer into media-sdk SDP
	offerSDP := &sdksdp.SDP{}
	if err := offerSDP.FromPion(*offer); err != nil {
		log.Printf("[POC-SDPAnswer] Failed to parse offer: %v", err)
		return nil, err
	}

	// Build answer using media-sdk's builder pattern
	builder := (&sdksdp.SDP{}).Builder()
	builder.SetAddress(a.localIP)

	// Handle audio if present in offer
	if offerSDP.Audio != nil && !offerSDP.Audio.Disabled {
		log.Printf("[POC-SDPAnswer] Building audio answer")
		builder.SetAudio(func(b *sdksdp.SDPMediaBuilder) (*sdksdp.SDPMedia, error) {
			// Select first available audio codec (PCMU/PCMA priority handled by media-sdk)
			if err := offerSDP.Audio.SelectCodec(); err != nil {
				return nil, fmt.Errorf("no common audio codec: %w", err)
			}

			return b.
				SetRTPPort(10000).
				SetDirection(sdksdp.DirectionSendRecv).
				AddCodec(func(cb *sdksdp.CodecBuilder) (*sdksdp.Codec, error) {
					return cb.
						SetPayloadType(0). // PCMU
						SetCodec(offerSDP.Audio.Codec.Codec).
						Build()
				}, true).
				Build()
		})
	}

	// Handle video if present in offer
	if offerSDP.Video != nil && !offerSDP.Video.Disabled {
		log.Printf("[POC-SDPAnswer] Building video answer")

		// Detect and log codec
		if err := offerSDP.Video.SelectCodec(); err == nil {
			codecName := offerSDP.Video.Codec.Codec.Info().SDPName
			log.Printf("[POC-SDPAnswer] Selected video codec: %s", codecName)

			// Log transcoding scenario
			livekitCodec := "VP8" // LiveKit default
			if codecName != livekitCodec {
				log.Printf("[POC-SDPAnswer] Transcoding required: %s <-> %s", codecName, livekitCodec)
			} else {
				log.Printf("[POC-SDPAnswer] PASSTHROUGH - No transcoding needed")
			}
		}

		builder.SetVideo(func(b *sdksdp.SDPMediaBuilder) (*sdksdp.SDPMedia, error) {
			if err := offerSDP.Video.SelectCodec(); err != nil {
				// Reject video if no common codec
				log.Printf("[POC-SDPAnswer] No common video codec, rejecting")
				return b.SetDisabled(true).Build()
			}

			return b.
				SetRTPPort(10002).
				SetDirection(sdksdp.DirectionSendRecv).
				AddCodec(func(cb *sdksdp.CodecBuilder) (*sdksdp.Codec, error) {
					return cb.
						SetPayloadType(96).
						SetCodec(offerSDP.Video.Codec.Codec).
						Build()
				}, true).
				Build()
		})
	}

	// Handle BFCP (screenshare) if present in offer
	if offerSDP.BFCP != nil {
		log.Printf("[POC-SDPAnswer] BFCP detected, accepting application media")
		// media-sdk handles BFCP automatically when building the answer
		builder.SetBFCP(offerSDP.BFCP)
	}

	// Build the answer
	answerSDP, err := builder.Build()
	if err != nil {
		log.Printf("[POC-SDPAnswer] Failed to build answer: %v", err)
		return nil, err
	}

	// Convert back to pion SDP
	answerPion, err := answerSDP.ToPion()
	if err != nil {
		log.Printf("[POC-SDPAnswer] Failed to convert answer to pion: %v", err)
		return nil, err
	}

	log.Printf("[POC-SDPAnswer] Successfully generated answer with %d media sections", len(answerPion.MediaDescriptions))
	return &answerPion, nil
}
