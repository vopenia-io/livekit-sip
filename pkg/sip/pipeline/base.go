package pipeline

import (
	"fmt"
	"sync"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
)

func newBasePipeline() (*basePipeline, error) {
	pipeline, err := gst.NewPipeline("")
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	return &basePipeline{
		Pipeline: pipeline,
	}, nil
}

type basePipeline struct {
	mu       sync.Mutex
	closed   core.Fuse
	Pipeline *gst.Pipeline
}

func (b *basePipeline) SetState(state gst.State) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.Closed() {
		return fmt.Errorf("cannot set state on closed pipeline")
	}

	if state == gst.StateNull {
		return b.close()
	}
	return b.Pipeline.SetState(state)
}

func (b *basePipeline) close() error {
	if b.Closed() {
		return nil
	}
	b.closed.Break()

	return b.Pipeline.SetState(gst.StateNull)
}

func (b *basePipeline) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.close()
}

func (b *basePipeline) Closed() bool {
	return b.closed.IsBroken()
}
