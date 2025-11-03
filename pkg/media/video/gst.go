package video

import (
	"fmt"
	"io"

	"errors"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

// func NewGstPipelineRunner(ctx context.Context, pipeline *gst.Pipeline) *GstPipelineRunner {
// 	c, cancel := context.WithCancel(ctx)
// 	return &GstPipelineRunner{
// 		pipeline: pipeline,
// 		ctx:      c,
// 		cancel:   cancel,
// 	}
// }

// type GstPipelineRunner struct {
// 	ctx      context.Context
// 	cancel   context.CancelFunc
// 	wg       sync.WaitGroup
// 	pipeline *gst.Pipeline
// 	running  atomic.Bool
// 	runners  []*GstSinkRunner
// }

// func (g *GstPipelineRunner) Run(ctx context.Context) error {
// 	if g.running.Load() {
// 		return errors.New("GstPipelineRunner already running")
// 	}
// 	slog.InfoContext(g.ctx, "starting GstPipelineRunner", "pipeline", g.pipeline.GetName())

// 	if err := g.pipeline.SetState(gst.StatePlaying); err != nil {
// 		return err
// 	}
// 	g.wg.Add(1)
// 	go func() {
// 		defer g.wg.Done()
// 		<-g.ctx.Done()
// 		g.pipeline.SetState(gst.StateNull)
// 	}()
// 	g.running.Store(true)

// 	for _, r := range g.runners {
// 		if err := r.Run(g.ctx); err != nil {
// 			return fmt.Errorf("failed to start GstSinkRunner: %w", err)
// 		}
// 	}
// 	return nil
// }

// func (g *GstPipelineRunner) NewSinkWriter(gr *GstReader, w io.WriteCloser) error {
// 	if g.running.Load() {
// 		return errors.New("cannot create GstSinkRunner while GstPipelineRunner is running")
// 	}
// 	sr := &GstSinkRunner{
// 		ctx: g.ctx,
// 		wg:  &g.wg,
// 		gr:  gr,
// 		w:   w,
// 	}
// 	g.runners = append(g.runners, sr)
// 	return nil
// }

// func (g *GstPipelineRunner) Close() error {
// 	g.cancel()
// 	var errs error
// 	for _, r := range g.runners {
// 		if err := r.Close(); err != nil {
// 			errs = errors.Join(errs, fmt.Errorf("failed to close GstSinkRunner: %w", err))
// 		}
// 	}
// 	g.wg.Wait()
// 	return errs
// }

// type GstSinkRunner struct {
// 	ctx context.Context
// 	wg  *sync.WaitGroup
// 	gr  *GstReader
// 	w   io.WriteCloser
// }

// func (g *GstSinkRunner) Run(ctx context.Context) error {
// 	if err := g.gr.SetState(gst.StatePlaying); err != nil {
// 		return err
// 	}
// 	g.wg.Add(1)
// 	go func() {
// 		defer g.wg.Done()

// 		buf := make([]byte, rtp.MTUSize)

// 		for {
// 			select {
// 			case <-ctx.Done():
// 				return
// 			default:
// 				n, err := g.gr.Read(buf)
// 				if err != nil {
// 					if err == io.EOF {
// 						slog.InfoContext(ctx, "GstSinkRunner read EOF, exiting")
// 						return
// 					}
// 					slog.ErrorContext(ctx, "GstSinkRunner read RTP failed", err)
// 					continue
// 				}

// 				_, err = g.w.Write(buf[:n])
// 				if err != nil {
// 					slog.ErrorContext(ctx, "GstSinkRunner write RTP failed", err)
// 					continue
// 				}
// 			}
// 		}
// 	}()
// 	return nil
// }

// func (g *GstSinkRunner) Close() error {
// 	var errs error
// 	if err := g.gr.Close(); err != nil {
// 		errs = errors.Join(errs, fmt.Errorf("failed to close GstReadRTP: %w", err))
// 	}
// 	if err := g.w.Close(); err != nil {
// 		errs = errors.Join(errs, fmt.Errorf("failed to close WriteStreamCloser: %w", err))
// 	}
// 	return errs
// }

func NewGstReader(sink *app.Sink) (*GstReader, error) {
	g := &GstReader{
		sink: sink,
	}

	if err := g.sink.SetState(gst.StatePlaying); err != nil {
		return nil, err
	}

	return g, nil
}

type GstReader struct {
	sink *app.Sink
}

func (g *GstReader) SetState(state gst.State) error {
	return g.sink.SetState(state)
}

func (g *GstReader) Close() error {
	if err := g.sink.SetState(gst.StateNull); err != nil {
		return err
	}
	return nil
}

func (g *GstReader) Read(buf []byte) (int, error) {
	sample := g.sink.PullSample()
	if sample == nil {
		if g.sink.IsEOS() {
			return 0, io.EOF
		}
		return 0, errors.New("failed to pull sample from appsink")
	}
	buffer := sample.GetBuffer()
	if buf == nil {
		return 0, errors.New("failed to get buffer from sample")
	}

	n := copy(buf, buffer.Bytes())
	return n, nil
}

func NewGstWriter(src *app.Source) (*GstWriter, error) {
	g := &GstWriter{
		src: src,
	}

	if err := g.src.SetState(gst.StatePlaying); err != nil {
		return nil, err
	}

	return g, nil
}

type GstWriter struct {
	src *app.Source
}

func (g *GstWriter) String() string {
	return fmt.Sprintf("GstWriteRTP(%s:%s)", g.src.GetFactory().GetName(), g.src.GetName())
}

func (g *GstWriter) Close() error {
	if err := g.src.SetState(gst.StateNull); err != nil {
		return err
	}
	return nil
}

func (g *GstWriter) Write(buf []byte) (int, error) {
	buffer := gst.NewBufferFromBytes(buf)
	if buffer == nil {
		return 0, errors.New("failed to create GST buffer from RTP packet")
	}
	g.src.PushBuffer(buffer)
	return len(buf), nil
}

func NewReadWriteRTP(src *app.Source, sink *app.Sink) (*GstReadWriter, error) {
	g := &GstReadWriter{}

	r, err := NewGstReader(sink)
	if err != nil {
		return nil, fmt.Errorf("failed to create GstReadRTP: %w", err)
	}
	g.GstReader = r

	w, err := NewGstWriter(src)
	if err != nil {
		g.GstReader.Close()
		return nil, fmt.Errorf("failed to create GstWriteRTP: %w", err)
	}
	g.GstWriter = w

	return g, nil
}

type GstReadWriter struct {
	*GstReader
	*GstWriter
}

func (g *GstReadWriter) Close() error {
	var errs error
	if err := g.GstWriter.Close(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to close GstWriteRTP: %w", err))
	}
	if err := g.GstReader.Close(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to close GstReadRTP: %w", err))
	}

	return errs
}
