package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

func (p *Pipeline) ConnectRoom(wsUrl, token string, attributes map[string]string) error {
	attr := gst.NewStructure("participant-attributes")

	for k, v := range attributes {
		if err := attr.SetValue(k, v); err != nil {
			p.Log.Warnw("failed to set participant attribute", err, "key", k, "value", v)
		}
	}

	if err := p.WebrtcIo.LivekitBin.SetProperty("participant-attributes", attr); err != nil {
		return fmt.Errorf("failed to set participant attributes: %w", err)
	}

	p.Log.Infow("Setting room options", "wsUrl", wsUrl)
	if err := p.WebrtcIo.LivekitBin.SetProperty("ws-url", wsUrl); err != nil {
		return fmt.Errorf("failed to set ws-url property: %w", err)
	}
	if err := p.WebrtcIo.LivekitBin.SetProperty("token", token); err != nil {
		return fmt.Errorf("failed to set token property: %w", err)
	}

	success := make(chan bool, 1)
	go func() {
		select {
		case <-p.WebrtcIo.Connected():
			success <- true
		case <-p.WebrtcIo.Closed():
			success <- false
		}
	}()

	if _, err := p.WebrtcIo.LivekitBin.Emit("connect"); err != nil {
		return fmt.Errorf("failed to emit connect signal: %v", err)
	}

	ok := <-success
	if !ok {
		return fmt.Errorf("failed to join room")
	}

	if err := p.WebrtcIo.LivekitBin.SetProperty("participant-attributes", attr); err != nil {
		return fmt.Errorf("failed to set participant attributes: %w", err)
	}

	p.Log.Infow("Joined room successfully", "wsUrl", wsUrl)

	return nil
}
