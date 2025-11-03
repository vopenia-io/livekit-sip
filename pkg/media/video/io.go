package video

import (
	"io"
	"sync"
	"sync/atomic"
)

type NopWriteCloser struct {
	io.Writer
}

func (n *NopWriteCloser) Close() error {
	return nil
}

type SwitchWriter struct {
	w atomic.Pointer[io.WriteCloser]
}

func (s *SwitchWriter) Write(p []byte) (n int, err error) {
	w := s.w.Load()
	if w == nil {
		return 0, nil
	}
	return (*w).Write(p)
}

func (s *SwitchWriter) Close() error {
	w := s.w.Load()
	if w != nil {
		return (*w).Close()
	}
	return nil
}

func (s *SwitchWriter) Swap(w io.WriteCloser) io.WriteCloser {
	var old *io.WriteCloser
	if w == nil {
		old = s.w.Swap(nil)
	} else {
		old = s.w.Swap(&w)
	}
	if old != nil {
		return *old
	}
	return nil
}

type SwitchReader struct {
	r atomic.Pointer[io.ReadCloser]
	b sync.Cond
}

func (s *SwitchReader) Read(p []byte) (n int, err error) {
	r := s.r.Load()
	if r == nil {
		s.b.Wait()
		return s.Read(p)
	}
	return (*r).Read(p)
}

func (s *SwitchReader) Close() error {
	r := s.r.Load()
	if r != nil {
		return (*r).Close()
	}
	return nil
}

func (s *SwitchReader) Swap(r io.ReadCloser) io.ReadCloser {
	var old *io.ReadCloser
	if r == nil {
		old = s.r.Swap(nil)
	} else {
		old = s.r.Swap(&r)
		s.b.Broadcast()
	}
	if old != nil {
		return *old
	}
	return nil
}