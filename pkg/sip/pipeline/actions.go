package pipeline

import (
	"context"
	"fmt"
	"time"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	msdk "github.com/livekit/media-sdk"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/samplewriter"
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

func (p *Pipeline) PlayAudio(ctx context.Context, sampleDur time.Duration, rate int, frames []msdk.PCM16Sample) error {
	ctx, cancel := context.WithTimeout(ctx, sampleDur*time.Duration(len(frames))*2)

	writer, err := samplewriter.NewSampleWriter(ctx, sampleDur, rate, frames)
	if err != nil {
		return fmt.Errorf("failed to create sample writer: %w", err)
	}
	writerSrc := writer.GetStaticPad("src")

	done := make(chan struct{})
	weakWriter := glib.WeakRefInit(writer)
	weakP := weak.Make(p)
	writerSrc.AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		event := info.GetEvent()
		if event == nil {
			return gst.PadProbePass
		}
		if event.Type() != gst.EventTypeEOS {
			return gst.PadProbePass
		}

		pad.RemoveProbe(uint64(info.ID()))
		close(done)

		glib.IdleAdd(func() {
			cleanupSampleWriter(weakP, weakWriter)
			cancel()
		})

		return gst.PadProbeDrop
	})

	if err := p.Pipeline().Add(writer); err != nil {
		return fmt.Errorf("failed to add sample writer to pipeline: %w", err)
	}

	if ret := writerSrc.Link(p.IOManager.LivekitController.GetRequestPad("raw_sink_%u")); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link sample writer to livekitbin: %v", ret)
	}

	if !writer.SyncStateWithParent() {
		p.Log.Warnw("Failed to sync sample writer state with parent", nil)
	}

	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

func cleanupSampleWriter(weakP weak.Pointer[Pipeline], weakWriter *glib.WeakRef) {
	p := weakP.Value()
	if p == nil {
		fmt.Printf("Pipeline has been garbage collected, stopping EOS probe\n")
		return
	}
	writer := gst.ToElement(weakWriter.Get())
	if writer == nil || writer.Instance() == nil {
		fmt.Printf("SampleWriter has been garbage collected, stopping EOS probe\n")
		return
	}
	p.Log.Debugw("Received EOS from sample writer, removing from pipeline")
	pad := writer.GetStaticPad("src")

	peer := pad.GetPeer()

	if err := writer.SetState(gst.StateNull); err != nil {
		p.Log.Warnw("Failed to set sample writer to null state", err)
	}
	if err := p.Pipeline().Remove(writer); err != nil {
		p.Log.Warnw("Failed to remove sample writer from pipeline", err)
	}
	if peer != nil {
		p.IOManager.LivekitController.ReleaseRequestPad(peer)
	}
}
