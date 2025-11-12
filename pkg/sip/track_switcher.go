package sip

import (
	"io"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/config"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const MaxRtpPacketSize = 1500

func NewRtpRewriter(r io.ReadCloser, ssrc uint32, csrc uint32, seq *atomic.Int32) *RtpRewriter {
	return &RtpRewriter{
		r:    r,
		ssrc: ssrc,
		csrc: csrc,
		buf:  make([]byte, MaxRtpPacketSize),
		seq:  seq,
	}
}

type KeyframeReader struct {
	r    io.ReadCloser
	done atomic.Bool
}

func isVP8Keyframe(payload []byte) bool {
	if len(payload) < 3 {
		return false
	}

	descriptor := payload[0]
	startOfPartition := (descriptor & 0x10) != 0
	if !startOfPartition {
		return false
	}

	hasExtension := (descriptor & 0x80) != 0
	offset := 1

	if hasExtension {
		if len(payload) <= offset {
			return false
		}
		extended := payload[offset]
		offset++

		if (extended & 0x80) != 0 {
			if len(payload) <= offset {
				return false
			}
			if (payload[offset] & 0x80) != 0 {
				offset += 2
			} else {
				offset++
			}
		}

		if (extended & 0x40) != 0 {
			if len(payload) <= offset {
				return false
			}
			offset++
		}

		if (extended&0x20) != 0 || (extended&0x10) != 0 {
			if len(payload) <= offset {
				return false
			}
			offset++
		}
	}

	if len(payload) < offset+3 {
		return false
	}

	frameTag := uint32(payload[offset]) | (uint32(payload[offset+1]) << 8) | (uint32(payload[offset+2]) << 16)
	return (frameTag & 0x01) == 0
}

func (k *KeyframeReader) Read(p []byte) (int, error) {
	if k.done.Load() {
		return k.r.Read(p)
	}

	buf := make([]byte, MaxRtpPacketSize)
	var n int
	var err error
	for {
		println("waiting for keyframe")
		var pkt rtp.Packet

		n, err = k.r.Read(buf)
		if err != nil {
			return 0, err
		}

		if err = pkt.Unmarshal(buf[:n]); err != nil {
			return 0, err
		}

		if isVP8Keyframe(pkt.Payload) {
			break
		}

		// var vp8 codecs.VP8Packet
		// frame, err := vp8.Unmarshal(buf[:n])
		// if err != nil {
		// 	return 0, err
		// }

		// if !vp8.IsPartitionHead(frame) || vp8.PID != 0 {
		// 	continue
		// }
		// if (frame[0] & 0x01) == 0 {
		// 	break
		// }
	}

	n = copy(p, buf[:n])
	k.done.Store(true)
	println("keyframe received")
	return n, nil
}

func (k *KeyframeReader) Close() error {
	return k.r.Close()
}

type RtpRewriter struct {
	r    io.ReadCloser
	ssrc uint32
	csrc uint32
	buf  []byte
	seq  *atomic.Int32
}

func (r *RtpRewriter) Read(p []byte) (n int, err error) {
	var pkt rtp.Packet
	n, err = r.r.Read(r.buf)
	if err != nil {
		return 0, err
	}

	if err = pkt.Unmarshal(r.buf[:n]); err != nil {
		return 0, err
	}

	seq := uint16(r.seq.Add(1) & 0xFFFF)

	pkt.SSRC = r.ssrc
	pkt.SequenceNumber = seq
	pkt.CSRC = []uint32{r.csrc}

	b, err := pkt.Marshal()
	if err != nil {
		return 0, err
	}

	n = copy(p, b)

	return n, nil
}

func (r *RtpRewriter) Close() error {
	return r.r.Close()
}

type ParticipantTracks struct {
	camera *TrackInput
	screen *TrackInput
}

func NewTrackSwitcher() *TrackSwitcher {
	return &TrackSwitcher{
		tracks: make(map[string]*ParticipantTracks),
		ssrc:   rand.Uint32(),
		rtp:    NewSwitchReader(),
		rtcp:   NewSwitchReader(),
	}
}

type TrackSwitcher struct {
	mu     sync.Mutex
	tracks map[string]*ParticipantTracks
	active string
	ssrc   uint32
	seq    atomic.Int32

	onSwitch func()

	rtp  *SwitchReader
	rtcp *SwitchReader
}

func (ts *TrackSwitcher) RTP() io.ReadCloser {
	return ts.rtp
}

func (ts *TrackSwitcher) RTCP() io.ReadCloser {
	return ts.rtcp
}

func (ts *TrackSwitcher) OnSwitch(fn func()) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.onSwitch = fn
}

func (ts *TrackSwitcher) activeTrack() (bool, *TrackInput) {
	if ts.active == "" {
		return false, nil
	}
	tracks, ok := ts.tracks[ts.active]
	if !ok {
		return false, nil
	}
	if tracks.screen != nil {
		return true, tracks.screen
	}
	if tracks.camera != nil {
		return true, tracks.camera
	}
	return false, nil
}

func (ts *TrackSwitcher) ActiveTrack() (bool, *TrackInput) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.activeTrack()
}

func (ts *TrackSwitcher) DeleteTrack(participantID string, source livekit.TrackSource) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	tracks, ok := ts.tracks[participantID]
	if !ok {
		return
	}

	var (
		ti       *TrackInput
		isCamera bool
	)

	switch source {
	case livekit.TrackSource_CAMERA:
		ti = tracks.camera
		if ti == nil {
			return
		}
		isCamera = true
	case livekit.TrackSource_SCREEN_SHARE:
		ti = tracks.screen
		if ti == nil {
			return
		}
	default:
		slog.Warn("unsupported track source for track switcher deletion", "source", source)
		return
	}

	activeRemoved := false
	if active, curr := ts.activeTrack(); active && curr == ti {
		activeRemoved = true
	}

	if isCamera {
		tracks.camera = nil
	} else {
		tracks.screen = nil
	}

	// if ti != nil {
	// 	if ti.RtpIn != nil {
	// 		if err := ti.RtpIn.Close(); err != nil {
	// 			slog.Debug("failed closing RTP input for track", "participant", participantID, "source", source, "error", err)
	// 		}
	// 	}
	// 	if ti.RtcpIn != nil {
	// 		if err := ti.RtcpIn.Close(); err != nil {
	// 			slog.Debug("failed closing RTCP input for track", "participant", participantID, "source", source, "error", err)
	// 		}
	// 	}
	// }

	if tracks.camera == nil && tracks.screen == nil {
		delete(ts.tracks, participantID)
	}

	if !activeRemoved {
		return
	}

	if pt, ok := ts.tracks[participantID]; ok && pt != nil {
		if pt.camera != nil || pt.screen != nil {
			if err := ts.ulSetActive(participantID, true); err == nil {
				return
			}
		}
	}

	for id, pt := range ts.tracks {
		if pt == nil {
			continue
		}
		if pt.camera == nil && pt.screen == nil {
			continue
		}
		if err := ts.ulSetActive(id, true); err == nil {
			return
		}
	}

	ts.rtp.Swap(nil)
	ts.rtcp.Swap(nil)
	ts.active = ""
}

func (ts *TrackSwitcher) DeleteParticipant(participantID string) {
	ts.DeleteTrack(participantID, livekit.TrackSource_CAMERA)
	ts.DeleteTrack(participantID, livekit.TrackSource_SCREEN_SHARE)
}

// func (ts *TrackSwitcher) swapTrack(ti *TrackInput, activeParticipant string) error {


// }

func (ts *TrackSwitcher) ulSetActive(participantID string, force bool) error {
	if ts.active == participantID && !force {
		return nil
	}

	tracks, ok := ts.tracks[participantID]
	if !ok {
		return nil
	}

	if tracks.camera == nil && tracks.screen == nil {
		return nil
	}

	var rtp, rtcp io.ReadCloser
	if tracks.screen != nil {
		rtp = tracks.screen.RtpIn
		rtcp = tracks.screen.RtcpIn
	} else if tracks.camera != nil {
		rtp = tracks.camera.RtpIn
		rtcp = tracks.camera.RtcpIn
	}

	if ts.onSwitch != nil {
		ts.onSwitch()
	}

	// rtp = &KeyframeReader{r: rtp}

	ts.rtp.Swap(rtp)
	ts.rtcp.Swap(rtcp)
	ts.active = participantID

	return nil
}

func (ts *TrackSwitcher) SetActive(participantID string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.ulSetActive(participantID, false)
}

func (ts *TrackSwitcher) OnWebrtcTrack(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) {
	id := rp.Identity()

	ts.mu.Lock()
	defer ts.mu.Unlock()

	tracks, ok := ts.tracks[id]
	if !ok {
		tracks = &ParticipantTracks{}
		ts.tracks[id] = tracks
	}

	ti := NewTrackInput(track, pub, rp, conf)
	ti.RtpIn = NewRtpRewriter(ti.RtpIn, ts.ssrc, uint32(track.SSRC()), &ts.seq)

	switch pub.Source() {
	case livekit.TrackSource_CAMERA:
		tracks.camera = ti
	case livekit.TrackSource_SCREEN_SHARE:
		tracks.screen = ti
	default:
		slog.Warn("unsupported track source for track switcher", "source", pub.Source())
		return
	}

	if ts.active == "" || ts.active == id {
		if err := ts.ulSetActive(id, true); err != nil {
			slog.Error("could not set active track", "error", err)
		}
	}
}
