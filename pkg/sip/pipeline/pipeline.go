package pipeline

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline/event"
)

type Pipeline struct {
	Log      logger.Logger
	pipeline *gst.Pipeline
	loop     *event.EventLoop
	ctx      context.Context
	closed   core.Fuse
	cleanup  func() error

	*SipIo
	*WebrtcIo
	*SipToWebrtc
	*WebrtcToSip
}

type GstChain interface {
	Create() error
	Add() error
	Link() error
	Close() error
}

func (p *Pipeline) Loop() *event.EventLoop {
	return p.loop
}

func (p *Pipeline) Pipeline() *gst.Pipeline {
	return p.pipeline
}

func (p *Pipeline) SetState(state gst.State) error {
	if p.Closed() {
		return fmt.Errorf("cannot set state on closed pipeline")
	}

	if state == gst.StateNull {
		return p.Close()
	}

	if err := p.Pipeline().SetState(state); err != nil {
		return fmt.Errorf("failed to set pipeline state: %w", err)
	}

	return nil
}

func (p *Pipeline) SetStateWait(state gst.State) error {
	if p.Closed() {
		return fmt.Errorf("cannot set state on closed pipeline")
	}

	if state == gst.StateNull {
		return p.Close()
	}

	if err := p.Pipeline().SetState(state); err != nil {
		return fmt.Errorf("failed to set pipeline state: %w", err)
	}

	cr, s := p.Pipeline().GetState(state, gst.ClockTime(time.Second*30))
	if cr != gst.StateChangeSuccess {
		return fmt.Errorf("failed to change pipeline state, wanted %s got %s: %s", state.String(), s.String(), cr.String())
	}
	if s != state {
		return fmt.Errorf("pipeline did not reach desired state, wanted %s got %s", state.String(), s.String())
	}

	return nil
}

var pid = os.Getpid()

func (p *Pipeline) Close() error {
	if p.Closed() {
		p.Log.Debugw("Pipeline already closed")
		return nil
	}
	p.closed.Break()
	p.Log.Debugw("Closing pipeline")
	defer p.loop.Stop()

	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		p.Log.Debugw("Setting pipeline to null state", "pid", pid)
		err = p.Pipeline().SetState(gst.StateNull)
		p.Log.Debugw("Pipeline set to null state complete", "pid", pid, "err", err)
	}()

	closed := false
	select {
	case <-done:
		closed = true
	case <-time.After(10 * time.Second):
	}
	if !closed {
		p.Log.Warnw("Timeout waiting for pipeline to set to null state, sending flush event", nil)
		go func() {
			p.Pipeline().SendEvent(gst.NewFlushStartEvent())
		}()
		select {
		case <-done:
			closed = true
		case <-time.After(5 * time.Second):
		}
	}
	if !closed {
		p.Log.Warnw("Timeout waiting for pipeline to set to null state after flush start, sending flush stop event", nil)
		go func() {
			p.Pipeline().SendEvent(gst.NewFlushStopEvent(true))
		}()
		select {
		case <-done:
			closed = true
		case <-time.After(5 * time.Second):
		}
	}
	if !closed {
		p.Log.Warnw("Timeout waiting for pipeline to set to null state after flush stop, trying to break clock", nil)
		go func() {
			p.Pipeline().SetBaseTime(0)
			p.Pipeline().SetStartTime(gst.ClockTimeNone)
		}()
		select {
		case <-done:
			closed = true
		case <-time.After(5 * time.Second):
		}
	}

	if !closed && p.cleanup != nil {
		p.Log.Warnw("Failed to set pipeline to null state after breaking clock, trying early cleanup", nil)
		if err := p.cleanup(); err != nil {
			p.Log.Errorw("Failed timeout cleanup before setting pipeline to null state", err)
		}
		p.cleanup = nil // prevent double cleanup
		select {
		case <-done:
			closed = true
		case <-time.After(5 * time.Second):
		}
	}

	if !closed {
		p.Log.Errorw("Failed to set pipeline to null state after breaking clock", nil)
		return fmt.Errorf("failed to set pipeline to null state")
	}

	p.Log.Debugw("Pipeline set to null state")

	if p.cleanup != nil {
		p.Log.Debugw("Running pipeline cleanup")
		if err := p.cleanup(); err != nil {
			p.Log.Errorw("Failed timeout cleanup before setting pipeline to null state", err)
		}
		p.Log.Debugw("Pipeline cleanup complete")
	}

	time.Sleep(100 * time.Millisecond) // give some time to settle
	p.Log.Debugw("Pipeline closed")

	return nil
}

func (p *Pipeline) Closed() bool {
	return p.closed.IsBroken()
}

func New(ctx context.Context, log logger.Logger) (*Pipeline, error) {
	log.Debugw("Creating pipeline")
	pipeline, err := gst.NewPipeline("")
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	p := &Pipeline{
		Log:      log.WithComponent("pipeline"),
		pipeline: pipeline,
		loop:     event.NewEventLoop(ctx, log),
	}
	p.cleanup = p.cleanupChains

	go p.Loop().Run()

	p.Log.Debugw("Setting bus to flushing")
	p.Pipeline().GetBus().SetFlushing(true)

	p.Log.Debugw("Adding SIP IO chain")
	p.SipIo, err = AddChain(p, NewSipInput(log, p))
	if err != nil {
		p.Log.Errorw("Failed to add SIP IO chain", err)
		return nil, err
	}

	p.Log.Debugw("Adding Webrtc IO chain")
	p.WebrtcIo, err = AddChain(p, NewWebrtcIo(log, p))
	if err != nil {
		p.Log.Errorw("Failed to add WebRTC IO chain", err)
		return nil, err
	}

	p.Log.Debugw("Adding SIP to WebRTC chain")
	p.SipToWebrtc, err = AddChain(p, NewSipToWebrtcChain(log, p))
	if err != nil {
		p.Log.Errorw("Failed to add SIP to WebRTC chain", err)
		return nil, err
	}

	p.Log.Debugw("Adding WebRTC to SIP chain")
	p.WebrtcToSip, err = AddChain(p, NewWebrtcToSipChain(log, p))
	if err != nil {
		p.Log.Errorw("Failed to add WebRTC to SIP chain", err)
		return nil, err
	}

	p.Log.Debugw("Linking chains")
	if err := LinkChains(p,
		p.SipIo,
		p.WebrtcIo,
		p.SipToWebrtc,
		p.WebrtcToSip,
	); err != nil {
		p.Log.Errorw("Failed to link chains", err)
		return nil, err
	}

	p.Log.Debugw("Pipeline created")

	return p, nil
}

func (p *Pipeline) cleanupChains() error {
	p.Log.Debugw("Closing pipeline chains")

	p.Log.Debugw("Closing SIP IO")
	if p.SipIo != nil {
		if err := p.SipIo.Close(); err != nil {
			return fmt.Errorf("failed to close SIP IO: %w", err)
		}
		p.SipIo = nil
	}

	p.Log.Debugw("Closing WebRTC IO")
	if p.WebrtcIo != nil {
		if err := p.WebrtcIo.Close(); err != nil {
			return fmt.Errorf("failed to close WebRTC IO: %w", err)
		}
		p.WebrtcIo = nil
	}

	p.Log.Debugw("Closing SIP to WebRTC chain")
	if p.SipToWebrtc != nil {
		if err := p.SipToWebrtc.Close(); err != nil {
			return fmt.Errorf("failed to close SIP to WebRTC chain: %w", err)
		}
		p.SipToWebrtc = nil
	}

	p.Log.Debugw("Closing WebRTC to SIP chain")
	if p.WebrtcToSip != nil {
		if err := p.WebrtcToSip.Close(); err != nil {
			return fmt.Errorf("failed to close WebRTC to SIP chain: %w", err)
		}
		p.WebrtcToSip = nil
	}

	p.Log.Debugw("Pipeline chains closed")
	return nil
}

func AddChain[C GstChain](p *Pipeline, chain C) (C, error) {
	var zero C

	p.Log.Debugw("Adding chain to pipeline")
	if err := chain.Create(); err != nil {
		return zero, fmt.Errorf("failed to create chain: %w", err)
	}

	p.Log.Debugw("Adding chain elements to pipeline")
	if err := chain.Add(); err != nil {
		return zero, fmt.Errorf("failed to add chain to pipeline: %w", err)
	}

	p.Log.Debugw("Chain added to pipeline")
	return chain, nil
}

func LinkChains(p *Pipeline, chains ...GstChain) error {
	for i, chain := range chains {
		p.Log.Debugw("Linking chain in pipeline", "chain_index", i)
		if err := chain.Link(); err != nil {
			typ := reflect.TypeOf(chain)
			p.Log.Errorw("Failed to link chain in pipeline", err, "index", i, "chain_type", typ.String())
			return fmt.Errorf("failed to link chain %s in pipeline: %w", typ.String(), err)
		}
	}
	return nil
}

func SyncElements(elements ...*gst.Element) error {
	for _, elem := range elements {
		if !elem.SyncStateWithParent() {
			return fmt.Errorf("failed to sync state for %s", elem.GetName())
		}
	}
	return nil
}
