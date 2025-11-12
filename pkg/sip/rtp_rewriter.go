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
	"sync"
	"sync/atomic"

	"github.com/pion/rtp"

	"github.com/livekit/protocol/logger"
)

// RTPRewriter rewrites RTP packet headers to maintain SSRC, sequence number,
// and timestamp continuity across video stream switches (e.g., active speaker changes).
type RTPRewriter struct {
	log logger.Logger

	// Fixed SSRC sent to SIP endpoint (maintains decoder state across switches)
	targetSSRC uint32

	// Current source stream SSRC (changes on participant switch)
	currentSrcSSRC atomic.Uint32

	// Sequence number tracking
	lastSeqNum     atomic.Uint32 // Last sequence number we emitted
	seqOffset      atomic.Int32  // Offset to add to incoming sequence numbers
	seqInitialized atomic.Bool   // Whether we've processed the first packet

	// Timestamp tracking
	lastTimestamp   atomic.Uint64 // Last timestamp we emitted
	timestampOffset atomic.Int64  // Offset to add to incoming timestamps
	tsInitialized   atomic.Bool   // Whether we've initialized timestamp tracking

	// Continuity tracking for source switches
	needsOffsetCalc atomic.Bool   // Whether we need to calculate new offsets
	seqContinueFrom atomic.Uint32 // Sequence number to continue from after switch
	tsContinueFrom  atomic.Uint64 // Timestamp to continue from after switch

	// Mutex for source switching operations
	switchMu sync.Mutex
}

// NewRTPRewriter creates a new RTP rewriter with the specified target SSRC.
func NewRTPRewriter(targetSSRC uint32, log logger.Logger) *RTPRewriter {
	return &RTPRewriter{
		targetSSRC: targetSSRC,
		log:        log,
	}
}

// RewritePacket rewrites an RTP packet's header to maintain continuity.
// This should be called for every outgoing RTP packet to the SIP endpoint.
func (r *RTPRewriter) RewritePacket(pkt *rtp.Packet) {
	originalSSRC := pkt.SSRC
	originalSeq := pkt.SequenceNumber
	originalTS := pkt.Timestamp

	// Always rewrite SSRC to our target
	pkt.SSRC = r.targetSSRC

	// Handle source switch - calculate new offsets for continuity
	if r.needsOffsetCalc.Load() {
		// First packet from new source after switch
		seqContinue := uint16(r.seqContinueFrom.Load())
		tsContinue := uint32(r.tsContinueFrom.Load())

		// Calculate offsets so this packet appears continuous with previous stream
		// Simple: offset = where_we_should_be - where_we_are
		newSeqOffset := int32(seqContinue) - int32(originalSeq)
		newTsOffset := int64(tsContinue) - int64(originalTS)

		r.seqOffset.Store(newSeqOffset)
		r.timestampOffset.Store(newTsOffset)
		r.needsOffsetCalc.Store(false)

		r.log.Infow("RTP rewriter: calculated continuity offsets",
			"seqOffset", newSeqOffset,
			"tsOffset", newTsOffset,
			"originalSeq", originalSeq,
			"continuingFrom", seqContinue,
			"originalTS", originalTS,
			"continuingFromTS", tsContinue)
	}

	// Rewrite sequence number
	if !r.seqInitialized.Load() {
		// Very first packet ever: initialize with no offset
		r.lastSeqNum.Store(uint32(originalSeq))
		r.seqOffset.Store(0)
		r.seqInitialized.Store(true)
		r.log.Debugw("RTP rewriter initialized",
			"targetSSRC", r.targetSSRC,
			"srcSSRC", originalSSRC,
			"initialSeq", originalSeq)
	} else {
		// Apply sequence offset for continuity
		offset := r.seqOffset.Load()
		newSeq := uint16(int32(originalSeq) + offset)
		pkt.SequenceNumber = newSeq
		r.lastSeqNum.Store(uint32(newSeq))
	}

	// Rewrite timestamp
	if !r.tsInitialized.Load() {
		// Very first packet ever: initialize with no offset
		r.lastTimestamp.Store(uint64(originalTS))
		r.timestampOffset.Store(0)
		r.tsInitialized.Store(true)
	} else {
		// Apply timestamp offset for continuity
		offset := r.timestampOffset.Load()
		newTS := uint32(int64(originalTS) + offset)
		pkt.Timestamp = newTS
		r.lastTimestamp.Store(uint64(newTS))
	}
}

// SwitchSource is called when switching to a new video source (e.g., new active speaker).
// It sets up continuation points so the next packet maintains sequence and timestamp continuity.
func (r *RTPRewriter) SwitchSource(newSSRC uint32) {
	r.switchMu.Lock()
	defer r.switchMu.Unlock()

	oldSSRC := r.currentSrcSSRC.Load()
	if oldSSRC == newSSRC {
		// Same source, no switch needed
		return
	}

	r.currentSrcSSRC.Store(newSSRC)

	if !r.seqInitialized.Load() {
		// First source ever, no continuity needed
		r.log.Infow("RTP rewriter: first source",
			"targetSSRC", r.targetSSRC,
			"srcSSRC", newSSRC)
		return
	}

	// Store continuation points for seamless transition
	// Next packet should continue from lastSeq+1 and lastTS+frameDuration
	lastSeq := uint16(r.lastSeqNum.Load())
	lastTS := uint32(r.lastTimestamp.Load())

	// Continue sequence from next number
	r.seqContinueFrom.Store(uint32(lastSeq) + 1)

	// Continue timestamp with ~33ms increment (assuming 30fps at 90kHz clock)
	// This creates a smooth transition even if packets are delayed
	r.tsContinueFrom.Store(uint64(lastTS) + 3000)

	// Mark that we need to calculate offsets on next packet
	r.needsOffsetCalc.Store(true)

	r.log.Infow("RTP rewriter: switching source - will maintain continuity",
		"targetSSRC", r.targetSSRC,
		"oldSrcSSRC", oldSSRC,
		"newSrcSSRC", newSSRC,
		"continueFromSeq", lastSeq+1,
		"continueFromTS", lastTS+3000)
}

// GetTargetSSRC returns the fixed SSRC being sent to the SIP endpoint.
func (r *RTPRewriter) GetTargetSSRC() uint32 {
	return r.targetSSRC
}

// Reset resets the rewriter state. Used for testing or error recovery.
func (r *RTPRewriter) Reset() {
	r.currentSrcSSRC.Store(0)
	r.seqInitialized.Store(false)
	r.tsInitialized.Store(false)
	r.lastSeqNum.Store(0)
	r.seqOffset.Store(0)
	r.lastTimestamp.Store(0)
	r.timestampOffset.Store(0)
}

// RewritingReader wraps an io.Reader to intercept and rewrite RTP packets.
type RewritingReader struct {
	reader   io.Reader
	rewriter *RTPRewriter
	log      logger.Logger
}

// NewRewritingReader creates a reader that rewrites RTP packets using the provided rewriter.
func NewRewritingReader(reader io.Reader, rewriter *RTPRewriter, log logger.Logger) *RewritingReader {
	return &RewritingReader{
		reader:   reader,
		rewriter: rewriter,
		log:      log,
	}
}

// Read implements io.Reader by reading RTP packets, rewriting them, and returning the modified data.
func (r *RewritingReader) Read(p []byte) (n int, err error) {
	// Read from underlying reader
	n, err = r.reader.Read(p)
	if err != nil {
		return n, err
	}

	// Parse RTP packet
	var pkt rtp.Packet
	if unmarshalErr := pkt.Unmarshal(p[:n]); unmarshalErr != nil {
		// Not a valid RTP packet, pass through unchanged
		r.log.Debugw("Failed to unmarshal RTP packet", "error", unmarshalErr)
		return n, nil
	}

	// Rewrite the packet
	r.rewriter.RewritePacket(&pkt)

	// Marshal back to bytes
	data, marshalErr := pkt.Marshal()
	if marshalErr != nil {
		r.log.Errorw("Failed to marshal rewritten RTP packet", marshalErr)
		return n, nil // Return original data on error
	}

	// Copy rewritten data back to buffer
	copy(p, data)
	return len(data), nil
}
