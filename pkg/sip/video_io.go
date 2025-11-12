package sip

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/config"
	"github.com/pion/rtcp"
	pionrtp "github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type NopWriteCloser struct {
	io.Writer
}

func (n *NopWriteCloser) Close() error {
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
	return sr
}

type SwitchReader struct {
	r  atomic.Pointer[io.ReadCloser]
	b  *sync.Cond
	mu sync.Mutex
}

func (s *SwitchReader) Read(p []byte) (n int, err error) {
	r := s.r.Load()
	if r == nil {
		s.mu.Lock()
		s.b.Wait()
		s.mu.Unlock()
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
		s.mu.Lock()
		s.b.Broadcast()
		s.mu.Unlock()
	}
	if old != nil {
		return *old
	}
	return nil
}

type TrackAdapter struct{ *webrtc.TrackRemote }

func (t *TrackAdapter) Read(p []byte) (n int, err error) {
	n, _, err = t.TrackRemote.Read(p)
	return n, err
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

func NewTrackInput(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) *TrackInput {
	ti := &TrackInput{
		Sid:    pub.SID(),
		SSRC:   uint32(track.SSRC()),
		RtpIn:  io.NopCloser(&TrackAdapter{TrackRemote: track}),
		RtcpIn: io.NopCloser(NewRtcpReader(pub, rp)),
		pub:    pub,
		rp:     rp,
	}
	return ti
}

type TrackInput struct {
	Sid    string
	SSRC   uint32
	RtpIn  io.ReadCloser
	RtcpIn io.ReadCloser
	pub    *lksdk.RemoteTrackPublication
	rp     *lksdk.RemoteParticipant
}

// SendPLI sends a PLI (Picture Loss Indication) to request a keyframe from this track
func (ti *TrackInput) SendPLI() error {
	if ti.rp == nil {
		return errors.New("no participant available")
	}
	// Use the RemoteParticipant's WritePLI method which handles the RTCP correctly
	ti.rp.WritePLI(webrtc.SSRC(ti.SSRC))
	return nil
}

// GetIdentity returns the identity of the participant associated with this track
func (ti *TrackInput) GetIdentity() string {
	if ti.rp == nil {
		return ""
	}
	return ti.rp.Identity()
}

// RTPWriteStreamAdapter adapts a GstWriter (io.Writer) to the rtp.WriteStream interface
// required by VideoSwitcher. It marshals RTP packets and writes them to the underlying writer.
type RTPWriteStreamAdapter struct {
	writer *GstWriter
}

// NewRTPWriteStreamAdapter creates an adapter that bridges GstWriter to rtp.WriteStream
func NewRTPWriteStreamAdapter(writer *GstWriter) *RTPWriteStreamAdapter {
	return &RTPWriteStreamAdapter{writer: writer}
}

// WriteRTP implements rtp.WriteStream interface by marshaling the RTP packet and writing to GstWriter
func (a *RTPWriteStreamAdapter) WriteRTP(header *pionrtp.Header, payload []byte) (int, error) {
	// Create RTP packet from header and payload
	pkt := &pionrtp.Packet{
		Header:  *header,
		Payload: payload,
	}

	// Marshal packet to bytes
	data, err := pkt.Marshal()
	if err != nil {
		return 0, err
	}

	// Write to underlying GstWriter
	return a.writer.Write(data)
}

// Close closes the underlying writer
func (a *RTPWriteStreamAdapter) Close() error {
	if a.writer != nil {
		return a.writer.Close()
	}
	return nil
}

// String implements fmt.Stringer for logging
func (a *RTPWriteStreamAdapter) String() string {
	return "RTPWriteStreamAdapter"
}
