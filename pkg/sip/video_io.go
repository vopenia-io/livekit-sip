package sip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

func NewDebugWriteCloser(w io.WriteCloser, prefix string, tick time.Duration) *DebugWriteCloser {
	dw := &DebugWriteCloser{
		ctx:         context.Background(),
		WriteCloser: w,
		Prefix:      prefix,
	}

	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()

		fmt.Printf("%s debug writer started\n", dw.Prefix)
		defer fmt.Printf("%s debug writer stopped\n", dw.Prefix)
		for {
			select {
			case <-ticker.C:
				// Log the number of bytes written so far
				n := dw.n.Swap(0)
				c := dw.c.Swap(0)
				fmt.Printf("%s wrote %d times (%d bytes)\n", dw.Prefix, c, n)
			case <-dw.ctx.Done():
				return
			}
		}
	}()

	return dw
}

type DebugWriteCloser struct {
	io.WriteCloser
	ctx    context.Context
	n      atomic.Int64
	c      atomic.Int64
	Prefix string
}

func (d *DebugWriteCloser) Write(p []byte) (int, error) {
	d.c.Add(1)
	n, err := d.WriteCloser.Write(p)
	if err == nil {
		d.n.Add(int64(n))
	}
	return n, err
}

func (d *DebugWriteCloser) Close() error {
	d.ctx.Done()
	return d.WriteCloser.Close()
}

func NewDebugReadCloser(r io.ReadCloser, prefix string, tick time.Duration) *DebugReadCloser {
	dr := &DebugReadCloser{
		ctx:        context.Background(),
		ReadCloser: r,
		Prefix:     prefix,
	}

	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()

		fmt.Printf("%s debug reader started\n", dr.Prefix)
		defer fmt.Printf("%s debug reader stopped\n", dr.Prefix)
		for {
			select {
			case <-ticker.C:
				// Log the number of bytes read so far
				n := dr.n.Swap(0)
				c := dr.c.Swap(0)
				fmt.Printf("%s read %d times (%d bytes)\n", dr.Prefix, c, n)
			case <-dr.ctx.Done():
				return
			}
		}
	}()

	return dr
}

type DebugReadCloser struct {
	io.ReadCloser
	ctx    context.Context
	n      atomic.Int64
	c      atomic.Int64
	Prefix string
}

func (d *DebugReadCloser) Read(p []byte) (int, error) {
	d.c.Add(1)
	n, err := d.ReadCloser.Read(p)
	if err == nil {
		d.n.Add(int64(n))
	}
	return n, err
}

func (d *DebugReadCloser) Close() error {
	d.ctx.Done()
	return d.ReadCloser.Close()
}

type NopWriteCloser struct {
	io.Writer
}

func (n *NopWriteCloser) Close() error {
	return nil
}

type CallbackWriteCloser struct {
	io.Writer
	Callback func() error
}

func (c *CallbackWriteCloser) Close() error {
	if c.Callback != nil {
		err := c.Callback()
		c.Callback = nil
		return err
	}
	return nil
}

func NewSwitchWriter() *SwitchWriter {
	return &SwitchWriter{}
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

func NewSwitchReader() *SwitchReader {
	sr := &SwitchReader{}
	sr.b = sync.NewCond(&sr.mu)
	sr.closed.Store(false)
	return sr
}

type SwitchReader struct {
	r      atomic.Pointer[io.ReadCloser]
	b      *sync.Cond
	mu     sync.Mutex
	closed atomic.Bool
}

func (s *SwitchReader) Read(p []byte) (n int, err error) {
	r := s.r.Load()
	if r == nil {
		// If closed, return EOF immediately
		if s.closed.Load() {
			return 0, io.EOF
		}
		// Wait for a reader to be swapped in
		s.mu.Lock()
		s.b.Wait()
		s.mu.Unlock()
		// Check again if closed after waking up
		if s.closed.Load() {
			return 0, io.EOF
		}
		return s.Read(p)
	}
	return (*r).Read(p)
}

func (s *SwitchReader) Close() error {
	// Mark as closed first
	s.closed.Store(true)
	// Wake up any blocked readers
	s.mu.Lock()
	s.b.Broadcast()
	s.mu.Unlock()
	// Close the underlying reader if exists
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
		s.mu.Lock()
		s.b.Broadcast()
		s.mu.Unlock()
	}
	if old != nil {
		return *old
	}
	return nil
}

func NewTrackAdapter(track *webrtc.TrackRemote) *TrackAdapter {
	ta := &TrackAdapter{}
	ta.Store(track)
	return ta
}

type TrackAdapter struct {
	atomic.Pointer[webrtc.TrackRemote]
}

func (t *TrackAdapter) Read(p []byte) (n int, err error) {
	tr := t.Load()
	if tr == nil {
		return 0, io.EOF
	}
	n, _, err = (*tr).Read(p)
	return n, err
}

func (t *TrackAdapter) Close() error {
	t.Store(nil)
	return nil
}

type RtcpWriter struct {
	pc *webrtc.PeerConnection
}

func (r *RtcpWriter) Write(p []byte) (n int, err error) {
	// fmt.Printf("RTCP Writer got %d bytes\n", len(p))
	pkts, err := rtcp.Unmarshal(p)
	if err != nil {
		return 0, err
	}
	if err := r.pc.WriteRTCP(pkts); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *RtcpWriter) Close() error {
	return nil
}

// ParticipantRtcpWriter writes RTCP to a RemoteParticipant (for sending PLI to track senders)
type ParticipantRtcpWriter struct {
	rp    *lksdk.RemoteParticipant
	track *webrtc.TrackRemote
}

func NewParticipantRtcpWriter(rp *lksdk.RemoteParticipant, track *webrtc.TrackRemote) *ParticipantRtcpWriter {
	return &ParticipantRtcpWriter{
		rp:    rp,
		track: track,
	}
}

func (r *ParticipantRtcpWriter) Write(p []byte) (n int, err error) {
	pkts, err := rtcp.Unmarshal(p)
	if err != nil {
		return 0, err
	}

	// Handle PLI/FIR packets by requesting keyframe via RemoteParticipant
	for _, pkt := range pkts {
		switch pkt.(type) {
		case *rtcp.PictureLossIndication:
			// Use WritePLI to request keyframe from sender
			if r.track != nil {
				r.rp.WritePLI(r.track.SSRC())
			}
		case *rtcp.FullIntraRequest:
			// FIR also requests keyframe
			if r.track != nil {
				r.rp.WritePLI(r.track.SSRC())
			}
		}
		// Other RTCP packets are ignored for now (could be extended if needed)
	}

	return len(p), nil
}

func (r *ParticipantRtcpWriter) Close() error {
	return nil
}

func NewRtcpReader(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) *RtcpReader {
	pipeReader, pipeWriter := io.Pipe()
	rr := &RtcpReader{
		pipeReader: pipeReader,
		pipeWriter: pipeWriter,
		pub:        pub,
		rp:         rp,
	}

	pub.OnRTCP(func(pkt rtcp.Packet) {
		var buf []byte
		b, err := pkt.Marshal()
		if err != nil {
			return
		}
		buf = append(buf, b...)
		_, err = rr.pipeWriter.Write(buf)
		if err != nil {
		}
	})

	return rr
}

type RtcpReader struct {
	pipeReader *io.PipeReader
	pipeWriter *io.PipeWriter
	pub        *lksdk.RemoteTrackPublication
	rp         *lksdk.RemoteParticipant
}

func (r *RtcpReader) Read(p []byte) (n int, err error) {
	return r.pipeReader.Read(p)
}

func (r *RtcpReader) Close() error {
	return errors.Join(r.pipeWriter.Close(), r.pipeReader.Close())
}

func NewTrackInput(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) *TrackInput {
	ti := &TrackInput{
		RtpIn:  NewTrackAdapter(track),
		RtcpIn: NewRtcpReader(pub, rp),
	}
	return ti
}

type TrackInput struct {
	RtpIn  io.ReadCloser
	RtcpIn io.ReadCloser
}

type TrackOutput struct {
	RtpOut  io.WriteCloser
	RtcpOut io.WriteCloser
}
