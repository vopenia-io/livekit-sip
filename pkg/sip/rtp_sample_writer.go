// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sip

import (
	"crypto/rand"
	"encoding/binary"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/livekit/sip/pkg/media/opus"
	"github.com/livekit/protocol/logger"
)

type rtpSampleWriter struct {
	track         *webrtc.TrackLocalStaticRTP
	audioLevel    *audioLevelWriter
	log           logger.Logger
	seqNum        atomic.Uint32
	timestamp     atomic.Uint32
	ssrc          uint32
	payloadType   uint8
	clockRate     uint32
	sampleRate    int
	frameDuration time.Duration
}

func (w *rtpSampleWriter) SetAudioLevelSource(audioLevel *audioLevelWriter) {
	w.audioLevel = audioLevel
}

func newRTPSampleWriter(
	track *webrtc.TrackLocalStaticRTP,
	audioLevel *audioLevelWriter,
	sampleRate int,
	frameDuration time.Duration,
	log logger.Logger,
) *rtpSampleWriter {
	var ssrcBuf [4]byte
	rand.Read(ssrcBuf[:])
	ssrc := binary.BigEndian.Uint32(ssrcBuf[:])

	var seqBuf [2]byte
	rand.Read(seqBuf[:])
	initialSeq := uint32(binary.BigEndian.Uint16(seqBuf[:]))

	var tsBuf [4]byte
	rand.Read(tsBuf[:])
	initialTs := binary.BigEndian.Uint32(tsBuf[:])

	w := &rtpSampleWriter{
		track:         track,
		audioLevel:    audioLevel,
		log:           log,
		ssrc:          ssrc,
		payloadType:   111,
		clockRate:     48000,
		sampleRate:    sampleRate,
		frameDuration: frameDuration,
	}

	w.seqNum.Store(initialSeq)
	w.timestamp.Store(initialTs)

	return w
}

func (w *rtpSampleWriter) WriteSample(sample opus.Sample) error {
	if len(sample) == 0 {
		return nil
	}

	level := uint8(127)
	if w.audioLevel != nil {
		level = w.audioLevel.GetCurrentLevel()
	}

	// VAD: LiveKit format is inverted (0=loudest), so lower values = more active
	const vadThreshold = 50
	voice := level <= vadThreshold

	audioLevelByte := level & 0x7F
	if voice {
		audioLevelByte |= 0x80
	}

	seq := uint16(w.seqNum.Add(1))
	tsIncrement := uint32(w.clockRate) * uint32(w.frameDuration.Milliseconds()) / 1000
	ts := w.timestamp.Add(tsIncrement)

	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:          2,
			Extension:        true,
			ExtensionProfile: rtp.ExtensionProfileOneByte,
			PayloadType:      w.payloadType,
			SequenceNumber:   seq,
			Timestamp:        ts,
			SSRC:             w.ssrc,
		},
		Payload: sample,
	}

	if err := packet.Header.SetExtension(1, []byte{audioLevelByte}); err != nil {
		w.log.Warnw("failed to set audio level extension", err)
	}

	return w.track.WriteRTP(packet)
}

func (w *rtpSampleWriter) Close() error {
	return nil
}

func (w *rtpSampleWriter) SampleRate() int {
	return w.sampleRate
}

func (w *rtpSampleWriter) String() string {
	return "rtp-audio-level-writer"
}
