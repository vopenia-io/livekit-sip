package sip

func (*MediaOrchestrator) OnOffer(offer []byte) (answer chan SdpResult, err error) {
	return nil, nil
}

func (*MediaOrchestrator) OnAnswer(answer []byte) error {
	return nil
}
