package sip

import "fmt"

func (*MediaOrchestrator) OnOffer(offer []byte) (answer chan SdpResult, err error) {
	return nil, nil
}

func (*MediaOrchestrator) OnAnswer(answer []byte) error {
	return nil
}

// GenerateFullOffer creates a complete SDP offer for re-invite
// TODO: Implement this method to generate SDP with full session capabilities
func (*MediaOrchestrator) GenerateFullOffer() ([]byte, error) {
	return nil, fmt.Errorf("GenerateFullOffer not implemented")
}
