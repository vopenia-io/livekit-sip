package camera_pipeline

import (
	"context"
	"fmt"

	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/event"

	"github.com/livekit/protocol/logger"
)

type CameraPipeline struct {
	*pipeline.BasePipeline
	*SipToWebrtc

	ctx    context.Context
	cancel context.CancelFunc
	loop   *event.EventLoop
}

func New(ctx context.Context, log logger.Logger) (*CameraPipeline, error) {
	log.Debugw("Creating camera pipeline")
	ctx, cancel := context.WithCancel(ctx)
	cp := &CameraPipeline{
		ctx:    ctx,
		cancel: cancel,
		loop:   event.NewEventLoop(ctx),
	}

	p, err := pipeline.New(log.WithComponent("camera_pipeline"))
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}
	p.Log().Debugw("Setting bus to flushing")
	p.Pipeline().GetBus().SetFlushing(true)

	cp.BasePipeline = p

	p.Log().Debugw("Starting event loop")
	go cp.loop.Run()

	p.Log().Debugw("Adding SIP to WebRTC chain")
	cp.SipToWebrtc, err = pipeline.AddChain(cp, NewSipToWebrtcChain(log, cp))
	if err != nil {
		p.Log().Errorw("Failed to add SIP to WebRTC chain", err)
		return nil, err
	}

	p.Log().Debugw("Camera pipeline created")

	return cp, nil
}

func (cp *CameraPipeline) Close() error {
	closed := cp.Closed()

	cp.loop.Stop()

	if err := cp.BasePipeline.Close(); err != nil {
		return fmt.Errorf("failed to close camera pipeline: %w", err)
	}

	if !closed {
		fmt.Println("Closing camera pipeline")
		if err := cp.SipToWebrtc.Close(); err != nil {
			return fmt.Errorf("failed to close SIP to WebRTC chain: %w", err)
		}
	}
	fmt.Println("Camera pipeline closed")

	return nil
}
