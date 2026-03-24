package samplewriter

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	msdk "github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
	"github.com/livekit/sip/res"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",samplewriter:5", true)
	gst.Init(nil)
	Register()
	os.Exit(m.Run())
}

const perFrame = res.SampleRate / msdk.DefFramesPerSec // 960 samples

func newTestFrames(n int) []msdk.PCM16Sample {
	frames := make([]msdk.PCM16Sample, n)
	for i := range frames {
		frames[i] = make(msdk.PCM16Sample, perFrame)
	}
	return frames
}

func newSampleWriterElement(t *testing.T, frames []msdk.PCM16Sample) *gst.Element {
	t.Helper()
	element, err := gst.NewElement("samplewriter")
	if err != nil {
		t.Fatal("failed to create samplewriter element:", err)
	}
	sw, ok := gst.SubclassFromElement[*SampleWriter](element)
	if !ok {
		t.Fatal("failed to get SampleWriter subclass from element")
	}
	sw.frames = frames
	sw.rate = res.SampleRate
	sw.sampleDur = rtp.DefFrameDur
	return element
}

func waitForEOSOrError(t *testing.T, pipeline *gst.Pipeline, timeout time.Duration) {
	t.Helper()
	bus := pipeline.GetPipelineBus()
	pollTimeout := gst.ClockTime(time.Second)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		msg := bus.TimedPop(pollTimeout)
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			t.Log("received EOS")
			return
		case gst.MessageError:
			gerr := msg.ParseError()
			t.Fatal("pipeline error:", gerr.Error())
		}
	}
	t.Fatal("timed out waiting for EOS")
}

func TestSampleWriter_ProducesBuffersAndEOS(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	const numFrames = 5
	pipeline, err := gst.NewPipeline("test-produces-buffers")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	src := newSampleWriterElement(t, newTestFrames(numFrames))

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}

	if err := pipeline.AddMany(src, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := gst.ElementLinkMany(src, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	var bufferCount atomic.Int32
	sinkPad := sink.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad")
	}
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	waitForEOSOrError(t, pipeline, 30*time.Second)

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers (expected %d)", count, numFrames)
	if count != numFrames {
		t.Fatalf("expected %d buffers, got %d", numFrames, count)
	}
}

func TestSampleWriter_StateChanges(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-state-changes")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	src := newSampleWriterElement(t, newTestFrames(3))
	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}

	if err := pipeline.AddMany(src, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := gst.ElementLinkMany(src, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	if err := pipeline.SetState(gst.StatePaused); err != nil {
		t.Fatal("failed to set pipeline to PAUSED:", err)
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestSampleWriter_EmptyFrames(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-empty-frames")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	src := newSampleWriterElement(t, nil)

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}

	if err := pipeline.AddMany(src, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := gst.ElementLinkMany(src, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	var bufferCount atomic.Int32
	sinkPad := sink.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad")
	}
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	waitForEOSOrError(t, pipeline, 10*time.Second)

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers (expected 0)", count)
	if count != 0 {
		t.Fatalf("expected 0 buffers for empty frames, got %d", count)
	}
}

func TestSampleWriter_CapsNegotiation(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-caps-negotiation")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	src := newSampleWriterElement(t, newTestFrames(3))

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}

	if err := pipeline.AddMany(src, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := gst.ElementLinkMany(src, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	capsReceived := make(chan string, 1)
	sinkPad := sink.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad")
	}
	sinkPad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		caps := pad.GetCurrentCaps()
		if caps != nil {
			select {
			case capsReceived <- caps.String():
			default:
			}
		}
		return gst.PadProbeRemove
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	select {
	case capsStr := <-capsReceived:
		t.Logf("negotiated caps: %s", capsStr)
		if !strings.Contains(capsStr, "S16LE") {
			t.Errorf("expected caps to contain S16LE, got: %s", capsStr)
		}
		if !strings.Contains(capsStr, fmt.Sprintf("rate=(int)%d", res.SampleRate)) {
			t.Errorf("expected caps to contain rate=%d, got: %s", res.SampleRate, capsStr)
		}
		if !strings.Contains(capsStr, "channels=(int)1") {
			t.Errorf("expected caps to contain channels=1, got: %s", capsStr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for caps")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestSampleWriter_UnlockStopsPipeline(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-unlock")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	// 1000 frames at 20ms each = 20 seconds — we'll stop early
	src := newSampleWriterElement(t, newTestFrames(1000))

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}

	if err := pipeline.AddMany(src, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := gst.ElementLinkMany(src, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// Let some buffers flow
	time.Sleep(500 * time.Millisecond)

	// Setting to NULL triggers Unlock → cancel → Fill returns FlowFlushing
	done := make(chan struct{})
	go func() {
		pipeline.SetState(gst.StateNull)
		close(done)
	}()

	select {
	case <-done:
		t.Log("pipeline stopped successfully")
	case <-time.After(10 * time.Second):
		t.Fatal("pipeline did not stop within 10s — Unlock may be broken")
	}
}
