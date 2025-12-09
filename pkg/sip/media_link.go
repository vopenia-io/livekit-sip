package sip

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"
)

type MediaLink struct {
	ctx     context.Context
	cancel  context.CancelFunc
	Dst     io.Writer
	Src     io.Reader
	running atomic.Bool
	ch      chan error
}

func NewMediaLink(ctx context.Context, dst io.Writer, src io.Reader) *MediaLink {
	cctx, cancel := context.WithCancel(ctx)
	return &MediaLink{
		ctx:    cctx,
		cancel: cancel,
		Dst:    dst,
		Src:    src,
		ch:     make(chan error, 1),
	}
}

func (m *MediaLink) Stop(timeout time.Duration) error {
	if !m.running.Swap(false) {
		return nil
	}
	m.cancel()

	t := time.After(timeout)
	select {
	case err, ok := <-m.ch:
		if !ok {
			return nil
		}
		return err
	case <-t:
		return errors.New("timeout waiting for media link to stop")
	}
}

func (m *MediaLink) Start() error {
	if m.running.Swap(true) {
		return errors.New("media link already running")
	}
	go func() {
		buf := make([]byte, 1500)
		var err error
		var n int
		defer func() {
			if err != nil {
				m.ch <- err
			}
			close(m.ch)
		}()
		for {
			select {
			case <-m.ctx.Done():
				return
			default:
			}

			n, err = m.Src.Read(buf)
			if err != nil {
				return
			}

			_, err = m.Dst.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()

	return nil
}
