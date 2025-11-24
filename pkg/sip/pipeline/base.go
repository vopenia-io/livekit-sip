package pipeline

import (
	"fmt"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
)

func newBasePipeline() (*basePipeline, error) {
	pipeline, err := gst.NewPipeline("")
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	// go func() {
	// 	for {
	// 		state := pipeline.GetCurrentState()
	// 		fmt.Printf("Pipeline %s state: %s\n", pipeline.GetName(), state.String())
	// 		time.Sleep(5 * time.Second)
	// 	}
	// }()

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

	if err := b.Pipeline.SetState(state); err != nil {
		return fmt.Errorf("failed to set pipeline state: %w", err)
	}

	return nil
}

func (b *basePipeline) SetStateWait(state gst.State) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.Closed() {
		return fmt.Errorf("cannot set state on closed pipeline")
	}

	if state == gst.StateNull {
		return b.close()
	}

	if err := b.Pipeline.SetState(state); err != nil {
		return fmt.Errorf("failed to set pipeline state: %w", err)
	}

	cr, s := b.Pipeline.GetState(state, gst.ClockTime(time.Second*30))
	if cr != gst.StateChangeSuccess {
		return fmt.Errorf("failed to change pipeline state, wanted %s got %s: %s", state.String(), s.String(), cr.String())
	}
	if s != state {
		return fmt.Errorf("pipeline did not reach desired state, wanted %s got %s", state.String(), s.String())
	}

	return nil
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
