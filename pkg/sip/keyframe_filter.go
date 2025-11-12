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
	"io"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/logger"
	"github.com/pion/rtp"
)

// KeyframeFilterReader wraps an io.Reader and transparently drops RTP packets
// until a VP8 keyframe is detected. After the first keyframe, all packets pass through.
// This allows swapping readers immediately without starving the pipeline.
// If no keyframe arrives within maxWaitDuration, the filter opens anyway to prevent freeze.
type KeyframeFilterReader struct {
	reader          io.Reader
	log             logger.Logger
	hasKeyframe     atomic.Bool
	droppedCount    atomic.Uint64
	passedCount     atomic.Uint64 // Count of packets passed through after keyframe
	startTime       time.Time
	maxWaitDuration time.Duration
	lastPacketTime  time.Time  // Time of last packet received
	firstSeqNum     atomic.Uint32 // Sequence number of the very first packet seen (even if dropped)
	firstSeqSet     atomic.Bool   // Whether firstSeqNum has been set
}

// NewKeyframeFilterReader creates a reader that filters out non-keyframe packets
// until the first keyframe is detected, then passes all packets through.
// maxWaitDuration: if no keyframe arrives within this time, filter opens anyway (prevents long freezes)
func NewKeyframeFilterReader(reader io.Reader, log logger.Logger) *KeyframeFilterReader {
	return &KeyframeFilterReader{
		reader:          reader,
		log:             log,
		startTime:       time.Now(),
		maxWaitDuration: 200 * time.Millisecond, // Open filter after 200ms even without keyframe (aggressive)
	}
}

// Read implements io.Reader. It reads RTP packets and drops non-keyframe packets
// until a keyframe is found. After that, all packets pass through unchanged.
func (f *KeyframeFilterReader) Read(p []byte) (n int, err error) {
	for {
		n, err = f.reader.Read(p)
		if err != nil {
			return n, err
		}

		f.lastPacketTime = time.Now()

		// After keyframe detected, just pass through all packets
		if f.hasKeyframe.Load() {
			passed := f.passedCount.Add(1)
			// Log periodically to track packet flow
			if passed%100 == 1 {
				f.log.Debugw("Filter passing packets", "passedCount", int(passed), "sinceKeyframeMs", int(time.Since(f.startTime).Milliseconds()))
			}
			return n, nil
		}

		// Check if we've waited too long for a keyframe - if so, give up and open filter
		elapsed := time.Since(f.startTime)
		if elapsed > f.maxWaitDuration {
			f.hasKeyframe.Store(true) // Mark as "opened" to stop filtering
			dropped := f.droppedCount.Load()
			f.log.Warnw("Keyframe filter timeout - opening filter without keyframe to prevent freeze", nil,
				"droppedPackets", int(dropped),
				"waitedMs", int(elapsed.Milliseconds()),
				"maxWaitMs", int(f.maxWaitDuration.Milliseconds()))
			return n, nil // Pass this packet through
		}

		// Parse RTP packet to check for keyframe
		var pkt rtp.Packet
		if unmarshalErr := pkt.Unmarshal(p[:n]); unmarshalErr != nil {
			// Not valid RTP, drop it
			f.droppedCount.Add(1)
			continue
		}

		// Capture the sequence number of the very first packet (for RTP rewriter continuity)
		if !f.firstSeqSet.Load() {
			f.firstSeqNum.Store(uint32(pkt.SequenceNumber))
			f.firstSeqSet.Store(true)
			f.log.Debugw("Captured first packet sequence number", "firstSeq", pkt.SequenceNumber, "ssrc", pkt.SSRC)
		}

		// Check for VP8 keyframe
		isKf, reason := isVP8KeyframeDebug(pkt.Payload)
		if isKf {
			// Found keyframe! From now on, pass everything through
			f.hasKeyframe.Store(true)
			dropped := f.droppedCount.Load()
			f.log.Infow("✅ Keyframe detected, filter opening",
				"droppedPackets", int(dropped),
				"marker", pkt.Marker,
				"payloadSize", len(pkt.Payload),
				"waitedMs", int(elapsed.Milliseconds()),
				"ssrc", pkt.SSRC)
			return n, nil
		}

		// Not a keyframe yet, drop and try next packet
		f.droppedCount.Add(1)
		if f.droppedCount.Load()%50 == 1 {
			f.log.Debugw("Keyframe filter dropping packets",
				"droppedSoFar", int(f.droppedCount.Load()),
				"reason", reason,
				"marker", pkt.Marker,
				"payloadSize", len(pkt.Payload),
				"elapsedMs", int(elapsed.Milliseconds()),
				"ssrc", pkt.SSRC)
		}
	}
}

// Close implements io.Closer
func (f *KeyframeFilterReader) Close() error {
	if closer, ok := f.reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// GetFirstSeqNum returns the sequence number of the very first packet received,
// even if it was dropped by the keyframe filter. Returns 0 if no packets received yet.
// This is used by the RTP rewriter to calculate correct offsets despite dropped packets.
func (f *KeyframeFilterReader) GetFirstSeqNum() (uint16, bool) {
	if !f.firstSeqSet.Load() {
		return 0, false
	}
	return uint16(f.firstSeqNum.Load()), true
}

// isVP8KeyframeDebug detects VP8 keyframes with detailed reason reporting
func isVP8KeyframeDebug(payload []byte) (bool, string) {
	if len(payload) < 1 {
		return false, "empty_payload"
	}

	// Parse VP8 payload descriptor
	firstByte := payload[0]
	isStart := (firstByte & 0x10) != 0 // S bit (bit 4)

	// Skip the payload descriptor to get to the frame header
	hasExtension := (firstByte & 0x80) != 0 // X bit (bit 7)
	descriptorSize := 1

	if hasExtension && len(payload) > 1 {
		extByte := payload[1]
		descriptorSize = 2

		if (extByte & 0x80) != 0 { // I bit - PictureID present
			descriptorSize++
			if len(payload) > descriptorSize && (payload[descriptorSize-1]&0x80) != 0 {
				descriptorSize++ // PictureID is 2 bytes
			}
		}
		if (extByte & 0x40) != 0 { // L bit - TL0PICIDX present
			descriptorSize++
		}
		if (extByte & 0x20) != 0 { // T or K bit - TID/KEYIDX present
			descriptorSize++
		}
	}

	if len(payload) <= descriptorSize {
		return false, "no_frame_header"
	}

	if !isStart {
		return false, "not_start_of_partition"
	}

	frameHeader := payload[descriptorSize]
	isKeyframe := (frameHeader & 0x01) == 0

	if !isKeyframe {
		return false, "p_bit_set_interframe"
	}

	return true, "keyframe_found"
}

// isVP8Keyframe detects VP8 keyframes according to RFC 7741.
// VP8 payload descriptor format:
//
//	0 1 2 3 4 5 6 7
//	+-+-+-+-+-+-+-+-+
//	|X|R|N|S|R| PID | (Required)
//	+-+-+-+-+-+-+-+-+
//
// S bit (bit 4): Start of VP8 partition
// P bit (bit 0 in frame header after descriptor): 0 for keyframe, 1 for interframe
//
// For a keyframe:
// - S bit must be 1 (start of partition)
// - After skipping the descriptor, the frame header's P bit must be 0
func isVP8Keyframe(payload []byte) bool {
	if len(payload) < 1 {
		return false
	}

	// Parse VP8 payload descriptor
	firstByte := payload[0]
	isStart := (firstByte & 0x10) != 0 // S bit (bit 4)

	// IMPORTANT: For VP8, we don't need S bit to be set
	// The S bit is for partition boundaries within a frame
	// Instead, we just need to check the P bit in the frame header
	// So we'll skip the S bit check and go straight to checking P bit

	// Skip the payload descriptor to get to the frame header
	// The descriptor size varies based on X bit
	hasExtension := (firstByte & 0x80) != 0 // X bit (bit 7)
	descriptorSize := 1

	if hasExtension && len(payload) > 1 {
		// Check extension bits to determine descriptor size
		extByte := payload[1]
		descriptorSize = 2

		if (extByte & 0x80) != 0 { // I bit - PictureID present
			descriptorSize++
			if len(payload) > descriptorSize && (payload[descriptorSize-1]&0x80) != 0 {
				descriptorSize++ // PictureID is 2 bytes
			}
		}
		if (extByte & 0x40) != 0 { // L bit - TL0PICIDX present
			descriptorSize++
		}
		if (extByte & 0x20) != 0 { // T or K bit - TID/KEYIDX present
			descriptorSize++
		}
	}

	// Check if we have enough data for the frame header
	if len(payload) <= descriptorSize {
		return false
	}

	// Only check P bit if this is the start of a partition
	if !isStart {
		return false
	}

	// Read the P bit from VP8 frame header (first bit of first byte after descriptor)
	// P=0 means keyframe, P=1 means interframe
	frameHeader := payload[descriptorSize]
	isKeyframe := (frameHeader & 0x01) == 0

	return isKeyframe
}
