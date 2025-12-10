package sip

import (
	"errors"
	"io"
	"sync/atomic"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

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

func NewTrackAdapter(track *webrtc.TrackRemote) *TrackAdapter {
	ta := &TrackAdapter{
		TrackRemote: track,
	}
	return ta
}

type TrackAdapter struct {
	*webrtc.TrackRemote
}

func (t *TrackAdapter) Read(p []byte) (n int, err error) {
	n, _, err = t.TrackRemote.Read(p)
	return n, err
}

func (t *TrackAdapter) Close() error {
	if err := t.TrackRemote.SetReadDeadline(time.Now()); err != nil {
		return err
	}
	return nil
}

type RtcpWriter struct {
	pc *webrtc.PeerConnection
}

func (r *RtcpWriter) Write(p []byte) (n int, err error) {
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
	deadline   atomic.Bool
}

func (r *RtcpReader) Read(p []byte) (n int, err error) {
	n, err = r.pipeReader.Read(p)
	if r.deadline.Load() {
		r.deadline.Store(false)
	}
	return n, err
}

func (r *RtcpReader) SetReadDeadline(t time.Time) error {
	r.deadline.Store(true)
	go func() {
		time.Sleep(time.Until(t))
		if r.deadline.Load() {
			r.pipeWriter.Write([]byte{})
		}
	}()
	return nil
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

type WebrtcTrackInput struct {
	TrackInput
	SSRC uint32
}

type TrackInput struct {
	RtpIn  io.ReadCloser
	RtcpIn io.ReadCloser
}

type TrackOutput struct {
	RtpOut  io.WriteCloser
	RtcpOut io.WriteCloser
}
