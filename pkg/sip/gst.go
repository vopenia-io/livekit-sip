package sip

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"

	"errors"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/media-sdk/rtp"
)

func NewGstWriteStream(ctx context.Context, pipeline *gst.Pipeline, rw *GstReadWriteRTP, w rtp.WriteStreamCloser) *GstWriteStream {
	if ctx == nil || pipeline == nil || rw == nil || w == nil {
		panic("invalid arguments to NewGstWriteStream")
	}

	g := &GstWriteStream{
		pipeline: pipeline,
		w:        w,
		rw:       rw,
	}
	g.ctx, g.cancel = context.WithCancel(ctx)
	return g
}

type GstWriteStream struct {
	ctx      context.Context
	cancel   context.CancelFunc
	running  bool
	wg       sync.WaitGroup
	pipeline *gst.Pipeline
	w        rtp.WriteStreamCloser
	rw       *GstReadWriteRTP
}

func (g *GstWriteStream) String() string {
	pname := "nil"
	if g.pipeline != nil {
		pname = g.pipeline.GetName()
	}
	wname := "nil"
	if g.w != nil {
		wname = g.w.String()
	}

	return fmt.Sprintf("GstWriteStream(%s: %t) -> %s", pname, g.running, wname)
}

func (g *GstWriteStream) Start() error {
	if g.running {
		return nil
	}

	slog.InfoContext(g.ctx, "starting GstWriteStream", "stream", g.String())

	if err := g.pipeline.SetState(gst.StatePlaying); err != nil {
		return err
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		<-g.ctx.Done()
		g.pipeline.SetState(gst.StateNull)
	}()

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()

		var h rtp.Header
		payload := make([]byte, rtp.MTUSize)

		for {
			select {
			case <-g.ctx.Done():
				return
			default:
				n, err := g.rw.ReadRTP(&h, payload)
				if err != nil {
					if err == io.EOF {
						slog.InfoContext(g.ctx, "GstWriteStream read EOF, exiting")
						return
					}
					slog.ErrorContext(g.ctx, "GstWriteStream read RTP failed", err)
					continue
				}

				// payload = payload[:n] // trim to actual size, don't know if it's necessary

				_, err = g.w.WriteRTP(&h, payload[:n])
				if err != nil {
					slog.ErrorContext(g.ctx, "GstWriteStream write RTP failed", err)
					continue
				}
			}
		}
	}()

	g.running = true

	return nil
}

func (g *GstWriteStream) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
	if !g.running {
		if err := g.Start(); err != nil {
			return 0, err
		}
	}
	return g.rw.WriteRTP(h, payload)
}

func (g *GstWriteStream) Close() error {
	g.cancel()
	g.wg.Wait()
	return g.w.Close()
}

// func NewGstPipelineRunner(pipeline *gst.Pipeline) *GstPipelineRunner {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	return &GstPipelineRunner{
// 		pipeline: pipeline,
// 		ctx:      ctx,
// 		cancel:   cancel,
// 	}
// }

// type GstPipelineRunner struct {
// 	pipeline *gst.Pipeline
// 	ctx      context.Context
// 	cancel   context.CancelFunc
// 	wg       sync.WaitGroup

// 	sipIn      *GstReadWriteRTP
// 	sipOut     *GstReadWriteRTP
// 	sipRtcpIn  *GstReadRTP
// 	sipRtcpOut *GstWriteRTP

// 	webrtc
// }

func NewGstPipelineRunner(ctx context.Context, pipeline *gst.Pipeline) *GstPipelineRunner {
	c, cancel := context.WithCancel(ctx)
	return &GstPipelineRunner{
		pipeline: pipeline,
		ctx:      c,
		cancel:   cancel,
	}
}

type GstPipelineRunner struct {
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	pipeline *gst.Pipeline
	running  atomic.Bool
	runners  []*GstSinkRunner
}

func (g *GstPipelineRunner) Run(ctx context.Context) error {
	if g.running.Load() {
		return errors.New("GstPipelineRunner already running")
	}
	slog.InfoContext(g.ctx, "starting GstPipelineRunner", "pipeline", g.pipeline.GetName())

	if err := g.pipeline.SetState(gst.StatePlaying); err != nil {
		return err
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		<-g.ctx.Done()
		g.pipeline.SetState(gst.StateNull)
	}()
	g.running.Store(true)

	for _, r := range g.runners {
		slog.InfoContext(g.ctx, "starting GstSinkRunner", "runner", r.String())
		if err := r.Run(g.ctx); err != nil {
			return fmt.Errorf("failed to start GstSinkRunner: %w", err)
		}
	}
	return nil
}

func (g *GstPipelineRunner) NewSinkWriter(gr *GstReadRTP, w rtp.WriteStreamCloser) error {
	if g.running.Load() {
		return errors.New("cannot create GstSinkRunner while GstPipelineRunner is running")
	}
	sr := &GstSinkRunner{
		ctx: g.ctx,
		wg:  &g.wg,
		gr:  gr,
		w:   w,
	}
	g.runners = append(g.runners, sr)
	return nil
}

func (g *GstPipelineRunner) Close() error {
	g.cancel()
	var errs error
	for _, r := range g.runners {
		if err := r.Close(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to close GstSinkRunner: %w", err))
		}
	}
	g.wg.Wait()
	return errs
}

type GstSinkRunner struct {
	ctx context.Context
	wg  *sync.WaitGroup
	gr  *GstReadRTP
	w   rtp.WriteStreamCloser
}

func (g *GstSinkRunner) String() string {
	return fmt.Sprintf("GstSinkRunner(%s) -> %s", g.gr.String(), g.w.String())
}

func (g *GstSinkRunner) Run(ctx context.Context) error {
	if err := g.gr.SetState(gst.StatePlaying); err != nil {
		return err
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()

		var h rtp.Header
		payload := make([]byte, rtp.MTUSize)

		for {
			select {
			case <-ctx.Done():
				return
			default:
				n, err := g.gr.ReadRTP(&h, payload)
				if err != nil {
					if err == io.EOF {
						slog.InfoContext(ctx, "GstSinkRunner read EOF, exiting")
						return
					}
					slog.ErrorContext(ctx, "GstSinkRunner read RTP failed", err)
					continue
				}

				_, err = g.w.WriteRTP(&h, payload[:n])
				if err != nil {
					slog.ErrorContext(ctx, "GstSinkRunner write RTP failed", err)
					continue
				}
			}
		}
	}()
	return nil
}

func (g *GstSinkRunner) Close() error {
	var errs error
	if err := g.gr.Close(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to close GstReadRTP: %w", err))
	}
	if err := g.w.Close(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to close WriteStreamCloser: %w", err))
	}
	return errs
}

func NewGstReadRTP(sink *app.Sink) (*GstReadRTP, error) {
	g := &GstReadRTP{
		sink: sink,
	}

	if err := g.sink.SetState(gst.StatePlaying); err != nil {
		return nil, err
	}

	return g, nil
}

type GstReadRTP struct {
	sink *app.Sink
}

func (g *GstReadRTP) SetState(state gst.State) error {
	return g.sink.SetState(state)
}

func (g *GstReadRTP) String() string {
	return fmt.Sprintf("GstReadRtp(%s:%s)", g.sink.GetFactory().GetName(), g.sink.GetName())
}

func (g *GstReadRTP) Close() error {
	if err := g.sink.SetState(gst.StateNull); err != nil {
		return err
	}
	return nil
}

func (g *GstReadRTP) ReadRTP(h *rtp.Header, payload []byte) (int, error) {
	var pkt rtp.Packet

	sample := g.sink.PullSample()
	if sample == nil {
		if g.sink.IsEOS() {
			return 0, io.EOF
		}
		return 0, errors.New("failed to pull sample from appsink")
	}
	buf := sample.GetBuffer()
	if buf == nil {
		return 0, errors.New("failed to get buffer from sample")
	}

	if err := pkt.Unmarshal(buf.Bytes()); err != nil {
		return 0, err
	}

	*h = pkt.Header
	n := copy(payload, pkt.Payload)
	return n, nil
}

func NewGstWriteRTP(src *app.Source) (*GstWriteRTP, error) {
	g := &GstWriteRTP{
		src: src,
	}

	if err := g.src.SetState(gst.StatePlaying); err != nil {
		return nil, err
	}

	return g, nil
}

type GstWriteRTP struct {
	src *app.Source
}

func (g *GstWriteRTP) String() string {
	return fmt.Sprintf("GstWriteRTP(%s:%s)", g.src.GetFactory().GetName(), g.src.GetName())
}

func (g *GstWriteRTP) Close() error {
	if err := g.src.SetState(gst.StateNull); err != nil {
		return err
	}
	return nil
}

func (g *GstWriteRTP) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
	p := rtp.Packet{
		Header:  *h,
		Payload: payload,
	}
	data, err := p.Marshal()
	if err != nil {
		return 0, err
	}
	n := len(payload)

	buf := gst.NewBufferFromBytes(data)
	if buf == nil {
		return 0, errors.New("failed to create GST buffer from RTP packet")
	}
	g.src.PushBuffer(buf)

	return n, nil
}

func NewReadWriteRTP(src *app.Source, sink *app.Sink) (*GstReadWriteRTP, error) {
	g := &GstReadWriteRTP{}

	r, err := NewGstReadRTP(sink)
	if err != nil {
		return nil, fmt.Errorf("failed to create GstReadRTP: %w", err)
	}
	g.GstReadRTP = r

	w, err := NewGstWriteRTP(src)
	if err != nil {
		g.GstReadRTP.Close()
		return nil, fmt.Errorf("failed to create GstWriteRTP: %w", err)
	}
	g.GstWriteRTP = w

	return g, nil
}

type GstReadWriteRTP struct {
	*GstReadRTP
	*GstWriteRTP
}

func (g *GstReadWriteRTP) String() string {
	return fmt.Sprintf("GstReadWriteRTP(w: %s -> r: %s)", g.GstWriteRTP.String(), g.GstReadRTP.String())
}

func (g *GstReadWriteRTP) Close() error {
	var errs error
	if err := g.GstWriteRTP.Close(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to close GstWriteRTP: %w", err))
	}
	if err := g.GstReadRTP.Close(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to close GstReadRTP: %w", err))
	}

	return errs
}
