// Copyright 2024 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sip

import (
	"sync"
	"time"

	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/protocol/logger"
	pionrtp "github.com/pion/rtp"
)

// VideoSwitcher provides artifact-free video transitions by buffering packets
// until a VP8 keyframe arrives, ensuring smooth speaker switches without visual glitches.
// This works in conjunction with the RTPRewriter to maintain stream continuity.
type VideoSwitcher struct {
	mu     sync.RWMutex
	log    logger.Logger
	output rtp.WriteStream

	currentSSRC      uint32
	transitioning    bool
	pendingSSRC      uint32
	transitionBuffer []bufferedPacket
	transitionStart  time.Time
	keyframeTimeout  *time.Timer
	timeoutCount     int

	lastSeq uint16
}

type bufferedPacket struct {
	header  *pionrtp.Header
	payload []byte
}

// NewVideoSwitcher creates a new VideoSwitcher that writes to the given output stream.
func NewVideoSwitcher(log logger.Logger, output rtp.WriteStream) *VideoSwitcher {
	return &VideoSwitcher{
		log:              log,
		output:           output,
		transitionBuffer: make([]bufferedPacket, 0, 100),
	}
}

// SetActiveSource initiates a transition to a new video source.
// Waits for a keyframe before switching to avoid artifacts.
func (vs *VideoSwitcher) SetActiveSource(ssrc uint32, reason string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	if vs.currentSSRC == ssrc {
		vs.log.Debugw("SetActiveSource: already active", "ssrc", ssrc, "reason", reason)
		return
	}

	// First participant - set immediately without transition
	if vs.currentSSRC == 0 {
		vs.log.Infow("SetActiveSource: first source", "ssrc", ssrc, "reason", reason)
		vs.currentSSRC = ssrc
		vs.transitioning = false
		return
	}

	// Switching from one source to another - initiate smooth transition
	vs.log.Infow("SetActiveSource: initiating transition",
		"fromSSRC", vs.currentSSRC,
		"toSSRC", ssrc,
		"reason", reason)

	vs.transitioning = true
	vs.pendingSSRC = ssrc
	vs.transitionBuffer = vs.transitionBuffer[:0]
	vs.timeoutCount = 0
	vs.transitionStart = time.Now()

	if vs.keyframeTimeout != nil {
		vs.keyframeTimeout.Stop()
	}
	vs.keyframeTimeout = time.AfterFunc(5*time.Second, func() {
		vs.forceTransition()
	})
}

// GetCurrentSSRC returns the currently active SSRC.
func (vs *VideoSwitcher) GetCurrentSSRC() uint32 {
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	return vs.currentSSRC
}

// WriteRTP writes an RTP packet to the switcher. Packets from the current source
// pass through immediately. Packets from the pending source are buffered until
// a keyframe is detected, at which point the switch occurs.
func (vs *VideoSwitcher) WriteRTP(header *pionrtp.Header, payload []byte) (int, error) {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	ssrc := header.SSRC

	// Current source - pass through immediately
	if ssrc == vs.currentSSRC {
		return vs.writeToOutput(header, payload)
	}

	// Pending source - buffer until keyframe
	if vs.transitioning && ssrc == vs.pendingSSRC {
		return vs.handleTransitionPacket(header, payload)
	}

	// Other sources - drop silently
	return len(payload), nil
}

// String implements fmt.Stringer for logging.
func (vs *VideoSwitcher) String() string {
	return "VideoSwitcher"
}

// handleTransitionPacket buffers packets from the pending source until a keyframe arrives.
func (vs *VideoSwitcher) handleTransitionPacket(header *pionrtp.Header, payload []byte) (int, error) {
	// Check for VP8 keyframe using the same logic as KeyframeFilterReader
	if isVP8Keyframe(payload) {
		latency := time.Since(vs.transitionStart)
		vs.log.Infow("video switched on keyframe",
			"latencyMs", latency.Milliseconds(),
			"bufferedPackets", len(vs.transitionBuffer),
			"fromSSRC", vs.currentSSRC,
			"toSSRC", vs.pendingSSRC)

		if vs.keyframeTimeout != nil {
			vs.keyframeTimeout.Stop()
			vs.keyframeTimeout = nil
		}

		vs.currentSSRC = vs.pendingSSRC
		vs.transitioning = false
		vs.timeoutCount = 0
		vs.transitionBuffer = vs.transitionBuffer[:0]

		return vs.writeToOutput(header, payload)
	}

	// Not a keyframe yet - buffer packet
	if len(vs.transitionBuffer) < 240 {
		headerCopy := *header
		payloadCopy := make([]byte, len(payload))
		copy(payloadCopy, payload)

		vs.transitionBuffer = append(vs.transitionBuffer, bufferedPacket{
			header:  &headerCopy,
			payload: payloadCopy,
		})
	}

	return len(payload), nil
}

// forceTransition is called after timeout to force a transition even without a keyframe.
func (vs *VideoSwitcher) forceTransition() {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	if !vs.transitioning {
		return
	}

	vs.timeoutCount++
	vs.transitionBuffer = vs.transitionBuffer[:0]

	if vs.timeoutCount >= 2 {
		latency := time.Since(vs.transitionStart)
		vs.log.Warnw("forcing switch without keyframe",
			nil,
			"latencyMs", latency.Milliseconds(),
			"fromSSRC", vs.currentSSRC,
			"toSSRC", vs.pendingSSRC,
			"note", "VP8 encoder not generating keyframes")

		vs.currentSSRC = vs.pendingSSRC
		vs.transitioning = false
		vs.timeoutCount = 0
		return
	}

	vs.log.Warnw("keyframe timeout - retrying",
		nil,
		"attempt", vs.timeoutCount,
		"bufferedPackets", len(vs.transitionBuffer))

	vs.keyframeTimeout = time.AfterFunc(5*time.Second, func() {
		vs.forceTransition()
	})
}

// writeToOutput writes a packet to the output stream, maintaining sequence continuity.
func (vs *VideoSwitcher) writeToOutput(header *pionrtp.Header, payload []byte) (int, error) {
	if vs.lastSeq != 0 {
		header.SequenceNumber = vs.lastSeq + 1
	}
	vs.lastSeq = header.SequenceNumber
	header.SSRC = vs.currentSSRC

	return vs.output.WriteRTP(header, payload)
}

// Close cleans up the switcher resources.
func (vs *VideoSwitcher) Close() error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	if vs.keyframeTimeout != nil {
		vs.keyframeTimeout.Stop()
		vs.keyframeTimeout = nil
	}

	vs.transitioning = false
	vs.transitionBuffer = nil
	vs.currentSSRC = 0
	vs.pendingSSRC = 0

	return nil
}
