package wavsource

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"golang.org/x/sys/unix"

	"github.com/livekit/sip/res"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",wavsource:5", true)
	gst.Init(nil)
	Register()
	os.Exit(m.Run())
}

// openWavFd creates a memfd from the embedded RoomJoinWav and returns an
// independent per-call fd opened via /proc/self/fd/N. The masterFd is closed
// on test cleanup; the playFd is owned by whichever element receives it.
func openWavFd(t *testing.T) int {
	t.Helper()
	masterFd, err := res.MemfdFromBytes("test-wav", res.RoomJoinWav)
	if err != nil {
		t.Fatal("failed to create memfd:", err)
	}
	t.Cleanup(func() { unix.Close(masterFd) })

	playFd, err := unix.Open(
		fmt.Sprintf("/proc/self/fd/%d", masterFd),
		unix.O_RDONLY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		t.Fatal("failed to open per-call fd:", err)
	}
	return playFd
}

// dumpDot writes a DOT graph of the pipeline to disk.
func dumpDot(t *testing.T, pipeline *gst.Pipeline, name string) {
	t.Helper()
	data := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
	if err := os.WriteFile(name, []byte(data), 0644); err != nil {
		t.Logf("failed to write DOT file %s: %v", name, err)
	} else {
		t.Logf("wrote DOT graph to %s", name)
	}
}

// runForDurationThenEOS pumps the bus for `runFor` then sends EOS into the
// pipeline and waits up to `eosTimeout` for the EOS message. Errors short-
// circuit. Returns when EOS is processed or eosTimeout elapses.
func runForDurationThenEOS(t *testing.T, pipeline *gst.Pipeline, runFor, eosTimeout time.Duration) {
	t.Helper()
	bus := pipeline.GetPipelineBus()
	tick := gst.ClockTime(100 * time.Millisecond)
	deadline := time.Now().Add(runFor)
	for time.Now().Before(deadline) {
		msg := bus.TimedPop(tick)
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageError:
			gerr := msg.ParseError()
			t.Fatal("pipeline error:", gerr.Error())
		case gst.MessageEOS:
			t.Log("received EOS during run window (early end)")
			return
		}
	}

	t.Log("sending EOS to pipeline")
	pipeline.SendEvent(gst.NewEOSEvent())

	deadline = time.Now().Add(eosTimeout)
	for time.Now().Before(deadline) {
		msg := bus.TimedPop(tick)
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			t.Log("received EOS")
			return
		case gst.MessageError:
			gerr := msg.ParseError()
			t.Fatal("pipeline error after EOS sent:", gerr.Error())
		}
	}
	t.Log("did not receive EOS in window; proceeding to teardown anyway")
}

// TestWavSource_LiveMixer_Alone reproduces the production scenario where the
// audiomixer has only the wavsource feeding it (no concurrent RTP track).
// Mixer is configured force-live=true, ignore-inactive-pads=true, matching
// the gateway. Output is written as a WAV file plus a DOT dump for inspection.
func TestWavSource_LiveMixer_Alone(t *testing.T) {
	const outputWav = "test_output_wavsource_alone.wav"
	const outputDot = "test_output_wavsource_alone.dot"

	pipeline, err := gst.NewPipeline("test-wavsource-alone")
	if err != nil {
		t.Fatal(err)
	}

	playFd := openWavFd(t)

	wav, err := gst.NewElementWithProperties("wavsource", map[string]interface{}{
		"fd": playFd,
	})
	if err != nil {
		unix.Close(playFd)
		t.Fatal(err)
	}

	mixer, err := gst.NewElementWithProperties("audiomixer", map[string]interface{}{
		"force-live":           true,
		"ignore-inactive-pads": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	conv, err := gst.NewElement("audioconvert")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := gst.NewElement("wavenc")
	if err != nil {
		t.Fatal(err)
	}
	sink, err := gst.NewElementWithProperties("filesink", map[string]interface{}{
		"location": outputWav,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := pipeline.AddMany(wav, mixer, conv, enc, sink); err != nil {
		t.Fatal(err)
	}

	mixerSink := mixer.GetRequestPad("sink_%u")
	if mixerSink == nil {
		t.Fatal("failed to get mixer request pad for wavsource")
	}
	wavSrcPad := wav.GetStaticPad("src")
	if ret := wavSrcPad.Link(mixerSink); ret != gst.PadLinkOK {
		t.Fatal("failed to link wavsource to mixer:", ret)
	}

	if err := gst.ElementLinkMany(mixer, conv, enc, sink); err != nil {
		t.Fatal(err)
	}

	var bufferCount atomic.Int32
	sinkPad := sink.GetStaticPad("sink")
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// Let the WAV play out, then send EOS so wavenc can finalize the file.
	// RoomJoinWav is ~1.3s @ 16kHz; give a comfortable margin.
	runForDurationThenEOS(t, pipeline, 5*time.Second, 3*time.Second)

	dumpDot(t, pipeline, outputDot)

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers at filesink (output: %s)", count, outputWav)
	if count == 0 {
		t.Fatal("no buffers reached the filesink")
	}
}

// TestWavSource_LiveMixer_WithSilenceSrc reproduces the scenario where there
// is a concurrent live source feeding the mixer (e.g. an active RTP track).
// audiotestsrc with wave=silence is_live=true acts as that always-on peer.
// Same mixer config as the gateway. Output: WAV file + DOT dump.
func TestWavSource_LiveMixer_WithSilenceSrc(t *testing.T) {
	const outputWav = "test_output_wavsource_with_silence.wav"
	const outputDot = "test_output_wavsource_with_silence.dot"

	pipeline, err := gst.NewPipeline("test-wavsource-with-silence")
	if err != nil {
		t.Fatal(err)
	}

	playFd := openWavFd(t)

	wav, err := gst.NewElementWithProperties("wavsource", map[string]interface{}{
		"fd": playFd,
	})
	if err != nil {
		unix.Close(playFd)
		t.Fatal(err)
	}

	silence, err := gst.NewElementWithProperties("audiotestsrc", map[string]interface{}{
		"wave":    int(4), // silence
		"is-live": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	mixer, err := gst.NewElementWithProperties("audiomixer", map[string]interface{}{
		"force-live":           true,
		"ignore-inactive-pads": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	conv, err := gst.NewElement("audioconvert")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := gst.NewElement("wavenc")
	if err != nil {
		t.Fatal(err)
	}
	sink, err := gst.NewElementWithProperties("filesink", map[string]interface{}{
		"location": outputWav,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := pipeline.AddMany(wav, silence, mixer, conv, enc, sink); err != nil {
		t.Fatal(err)
	}

	silenceSink := mixer.GetRequestPad("sink_%u")
	if silenceSink == nil {
		t.Fatal("failed to get mixer request pad for silence")
	}
	if ret := silence.GetStaticPad("src").Link(silenceSink); ret != gst.PadLinkOK {
		t.Fatal("failed to link silence to mixer:", ret)
	}

	wavMixerSink := mixer.GetRequestPad("sink_%u")
	if wavMixerSink == nil {
		t.Fatal("failed to get mixer request pad for wavsource")
	}
	if ret := wav.GetStaticPad("src").Link(wavMixerSink); ret != gst.PadLinkOK {
		t.Fatal("failed to link wavsource to mixer:", ret)
	}

	if err := gst.ElementLinkMany(mixer, conv, enc, sink); err != nil {
		t.Fatal(err)
	}

	var bufferCount atomic.Int32
	sinkPad := sink.GetStaticPad("sink")
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	runForDurationThenEOS(t, pipeline, 5*time.Second, 3*time.Second)

	dumpDot(t, pipeline, outputDot)

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers at filesink (output: %s)", count, outputWav)
	if count == 0 {
		t.Fatal("no buffers reached the filesink")
	}
}

// TestWavSource_LiveMixer_Alone_DownstreamClockSync is the alone scenario but
// with a clocksync inserted between the mixer and wavenc. The hypothesis is
// that without a live peer the mixer free-runs and dumps silence at full
// speed; clocksync downstream may force-pace the mixer's output.
func TestWavSource_LiveMixer_Alone_DownstreamClockSync(t *testing.T) {
	const outputWav = "test_output_wavsource_alone_downstream_clocksync.wav"
	const outputDot = "test_output_wavsource_alone_downstream_clocksync.dot"

	pipeline, err := gst.NewPipeline("test-wavsource-alone-cs")
	if err != nil {
		t.Fatal(err)
	}

	playFd := openWavFd(t)

	wav, err := gst.NewElementWithProperties("wavsource", map[string]interface{}{
		"fd": playFd,
	})
	if err != nil {
		unix.Close(playFd)
		t.Fatal(err)
	}

	mixer, err := gst.NewElementWithProperties("audiomixer", map[string]interface{}{
		"force-live":           true,
		"ignore-inactive-pads": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	cs, err := gst.NewElement("clocksync")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := gst.NewElement("audioconvert")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := gst.NewElement("wavenc")
	if err != nil {
		t.Fatal(err)
	}
	sink, err := gst.NewElementWithProperties("filesink", map[string]interface{}{
		"location": outputWav,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := pipeline.AddMany(wav, mixer, cs, conv, enc, sink); err != nil {
		t.Fatal(err)
	}

	mixerSink := mixer.GetRequestPad("sink_%u")
	if mixerSink == nil {
		t.Fatal("failed to get mixer request pad for wavsource")
	}
	if ret := wav.GetStaticPad("src").Link(mixerSink); ret != gst.PadLinkOK {
		t.Fatal("failed to link wavsource to mixer:", ret)
	}

	if err := gst.ElementLinkMany(mixer, cs, conv, enc, sink); err != nil {
		t.Fatal(err)
	}

	var bufferCount atomic.Int32
	sinkPad := sink.GetStaticPad("sink")
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	runForDurationThenEOS(t, pipeline, 5*time.Second, 3*time.Second)

	dumpDot(t, pipeline, outputDot)

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers at filesink (output: %s)", count, outputWav)
	if count == 0 {
		t.Fatal("no buffers reached the filesink")
	}
}

// TestWavSource_LiveMixer_LateInject simulates the production "second prompt"
// scenario: the pipeline runs with a live peer for ~2s before the wavsource
// joins. At link time, the mixer pad's offset is set to the current running
// time so that the wavsource's PTS=0 buffers are mapped to "now" rather than
// being dropped as ancient. Output: WAV file (should have ~2s of silence
// followed by the WAV audio) + DOT dump.
func TestWavSource_LiveMixer_LateInject(t *testing.T) {
	const outputWav = "test_output_wavsource_late_inject.wav"
	const outputDot = "test_output_wavsource_late_inject.dot"
	const warmupDuration = 2 * time.Second

	pipeline, err := gst.NewPipeline("test-wavsource-late-inject")
	if err != nil {
		t.Fatal(err)
	}

	silence, err := gst.NewElementWithProperties("audiotestsrc", map[string]interface{}{
		"wave":    int(4), // silence
		"is-live": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	mixer, err := gst.NewElementWithProperties("audiomixer", map[string]interface{}{
		"force-live":           true,
		"ignore-inactive-pads": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	conv, err := gst.NewElement("audioconvert")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := gst.NewElement("wavenc")
	if err != nil {
		t.Fatal(err)
	}
	sink, err := gst.NewElementWithProperties("filesink", map[string]interface{}{
		"location": outputWav,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := pipeline.AddMany(silence, mixer, conv, enc, sink); err != nil {
		t.Fatal(err)
	}

	silenceMixerSink := mixer.GetRequestPad("sink_%u")
	if silenceMixerSink == nil {
		t.Fatal("failed to get mixer request pad for silence")
	}
	if ret := silence.GetStaticPad("src").Link(silenceMixerSink); ret != gst.PadLinkOK {
		t.Fatal("failed to link silence to mixer:", ret)
	}

	if err := gst.ElementLinkMany(mixer, conv, enc, sink); err != nil {
		t.Fatal(err)
	}

	var bufferCount atomic.Int32
	sinkPad := sink.GetStaticPad("sink")
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// Let the silence-only pipeline run for warmupDuration so running time
	// advances. This is what makes a fresh wavsource's PTS=0 buffers look
	// "ancient" to the mixer without an offset adjustment.
	t.Logf("warming up for %s before injecting wavsource", warmupDuration)
	bus := pipeline.GetPipelineBus()
	tick := gst.ClockTime(100 * time.Millisecond)
	warmupDeadline := time.Now().Add(warmupDuration)
	for time.Now().Before(warmupDeadline) {
		msg := bus.TimedPop(tick)
		if msg == nil {
			continue
		}
		if msg.Type() == gst.MessageError {
			t.Fatal("pipeline error during warmup:", msg.ParseError().Error())
		}
	}

	// Now inject the wavsource dynamically. Pass the current running time as
	// running-time-base so the wavsource's internal pad offset aligns its
	// PTS=0 buffers to "now".
	clock := pipeline.GetPipelineClock()
	if clock == nil {
		t.Fatal("pipeline has no clock")
	}
	runningTime := int64(clock.GetTime() - mixer.GetBaseTime())
	t.Logf("injecting wavsource with running-time-base=%d ns (%s)", runningTime, time.Duration(runningTime))

	playFd := openWavFd(t)
	wav, err := gst.NewElementWithProperties("wavsource", map[string]interface{}{
		"fd":                playFd,
		"running-time-base": runningTime,
	})
	if err != nil {
		unix.Close(playFd)
		t.Fatal(err)
	}
	if err := pipeline.Add(wav); err != nil {
		t.Fatal(err)
	}

	wavMixerSink := mixer.GetRequestPad("sink_%u")
	if wavMixerSink == nil {
		t.Fatal("failed to get mixer request pad for wavsource")
	}

	if ret := wav.GetStaticPad("src").Link(wavMixerSink); ret != gst.PadLinkOK {
		t.Fatal("failed to link wavsource to mixer:", ret)
	}
	if !wav.SyncStateWithParent() {
		t.Log("warning: failed to sync wavsource state with parent")
	}

	// Run for additional 5s so the WAV plays (~1.3s @ 16kHz) and we capture
	// the result, then EOS to finalize the file.
	runForDurationThenEOS(t, pipeline, 5*time.Second, 3*time.Second)

	dumpDot(t, pipeline, outputDot)

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers at filesink (output: %s)", count, outputWav)
	if count == 0 {
		t.Fatal("no buffers reached the filesink")
	}
}

// TestWavSource_Pipeline is a smoke test that runs the wavsource bin end-to-
// end against a fakesink (no mixer, no live timing). EOS is expected because
// fdsrc reaches EOF and propagates EOS through the chain.
func TestWavSource_Pipeline(t *testing.T) {
	playFd := openWavFd(t)

	pipeline, err := gst.NewPipeline("test-wavsource")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	source, err := gst.NewElementWithProperties("wavsource", map[string]interface{}{
		"fd": playFd,
	})
	if err != nil {
		unix.Close(playFd)
		t.Fatal("failed to create wavsource:", err)
	}

	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(source, sink); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}

	if err := gst.ElementLinkMany(source, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	var bufferCount atomic.Int32
	sinkPad := sink.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad from fakesink")
	}
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	bus := pipeline.GetPipelineBus()
	timeout := gst.ClockTime(time.Second)
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		msg := bus.TimedPop(timeout)
		if msg == nil {
			continue
		}

		switch msg.Type() {
		case gst.MessageEOS:
			t.Log("received EOS")
			goto done
		case gst.MessageError:
			gerr := msg.ParseError()
			t.Fatal("pipeline error:", gerr.Error())
		}
	}
	t.Fatal("pipeline timed out waiting for EOS")

done:
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers", count)
	if count <= 0 {
		t.Fatal("no buffers received through wavsource element")
	}
}
