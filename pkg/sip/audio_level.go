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
	"math"
	"sync/atomic"

	msdk "github.com/livekit/media-sdk"
	"github.com/livekit/protocol/logger"
)

// audioLevelWriter wraps a PCM16Writer and calculates audio levels from samples.
type audioLevelWriter struct {
	w            msdk.PCM16Writer
	log          logger.Logger
	currentLevel atomic.Uint32
	sampleCount  atomic.Uint64
}

func newAudioLevelWriter(w msdk.PCM16Writer, log logger.Logger) *audioLevelWriter {
	return &audioLevelWriter{
		w:   w,
		log: log,
	}
}

func (a *audioLevelWriter) WriteSample(sample msdk.PCM16Sample) error {
	level := calculateAudioLevel(sample)
	a.currentLevel.Store(uint32(level))
	return a.w.WriteSample(sample)
}

func (a *audioLevelWriter) Close() error {
	return a.w.Close()
}

func (a *audioLevelWriter) SampleRate() int {
	return a.w.SampleRate()
}

func (a *audioLevelWriter) String() string {
	return a.w.String()
}

func (a *audioLevelWriter) GetCurrentLevel() uint8 {
	return uint8(a.currentLevel.Load())
}

// calculateAudioLevel calculates audio level in LiveKit format (0=loudest, 127=silence).
// This is inverted from RFC 6464 to match LiveKit's internal representation.
func calculateAudioLevel(sample msdk.PCM16Sample) uint8 {
	if len(sample) == 0 {
		return 127
	}

	var sum uint64
	for _, s := range sample {
		v := int64(s)
		sum += uint64(v * v)
	}

	if sum == 0 {
		return 127
	}

	rms := math.Sqrt(float64(sum) / float64(len(sample)))
	dbfs := 20 * math.Log10(rms/32768.0)
	level := int(math.Round(-dbfs))

	if level < 0 {
		return 0
	}
	if level > 127 {
		return 127
	}

	return uint8(level)
}
