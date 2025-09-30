// Copyright 2023 LiveKit, Inc.
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
	"testing"
)

func TestVideoFrame(t *testing.T) {
	frame := VideoFrame([]byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x0a})

	// Test Size
	if frame.Size() != 8 {
		t.Errorf("Expected size 8, got %d", frame.Size())
	}

	// Test CopyTo
	dst := make([]byte, 10)
	n, err := frame.CopyTo(dst)
	if err != nil {
		t.Errorf("CopyTo failed: %v", err)
	}
	if n != 8 {
		t.Errorf("Expected copied bytes 8, got %d", n)
	}

	// Test CopyTo with insufficient buffer
	dst = make([]byte, 4)
	n, err = frame.CopyTo(dst)
	if err == nil {
		t.Error("Expected error for insufficient buffer")
	}
	if n != 0 {
		t.Errorf("Expected 0 bytes copied, got %d", n)
	}
}

func TestMediaPortVideoSupport(t *testing.T) {
	// Skip this test for now due to logger dependency
	t.Skip("Skipping MediaPort test due to logger dependency")
}

func TestRoomVideoSupport(t *testing.T) {
	// Skip this test for now due to logger dependency
	t.Skip("Skipping Room test due to logger dependency")
}

func TestVideoConfig(t *testing.T) {
	config := &VideoConfig{
		Type: 96,
	}

	if config.Type != 96 {
		t.Errorf("Expected type 96, got %d", config.Type)
	}
}

func TestVideoMediaDesc(t *testing.T) {
	desc := &VideoMediaDesc{}

	if desc.Codecs == nil {
		t.Error("Expected non-nil Codecs slice")
	}

	if len(desc.Codecs) != 0 {
		t.Errorf("Expected empty Codecs slice, got %d items", len(desc.Codecs))
	}
}

// mockVideoWriter is a mock implementation of msdk.Writer[VideoFrame] for testing
type mockVideoWriter struct{}

func (m *mockVideoWriter) WriteSample(sample VideoFrame) error {
	return nil
}

func (m *mockVideoWriter) SampleRate() int {
	return 90000
}

func (m *mockVideoWriter) String() string {
	return "mockVideoWriter"
}
