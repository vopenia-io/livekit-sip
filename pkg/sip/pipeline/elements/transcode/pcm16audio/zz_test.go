package pcm16audio

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/samplewriter"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
	"github.com/livekit/sip/res"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",pcm16-audio:5,samplewriter:5", true)
	gst.Init(nil)
	Register()
	samplewriter.Register()
	os.Exit(m.Run())
}

func TestPCM16Audio_Pipeline(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-pcm16-audio")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	audioSrc, err := gst.NewElement("audiotestsrc")
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	audioSrc.SetProperty("num-buffers", 150)

	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=48000,channels=1,format=S16LE"))

	transcoder, err := gst.NewElement("pcm16-audio")
	if err != nil {
		t.Fatal("failed to create pcm16-audio:", err)
	}

	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(audioSrc, capsFilter, transcoder, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := gst.ElementLinkMany(audioSrc, capsFilter, transcoder, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	var bufferCount atomic.Int32
	sinkPad := sink.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad from fakesink")
	}
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	bus := pipeline.GetPipelineBus()
	pollTimeout := gst.ClockTime(time.Second)
	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		msg := bus.TimedPop(pollTimeout)
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
	t.Fatal("timed out waiting for EOS")

done:
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers", count)
	if count <= 0 {
		t.Fatal("no buffers received through pcm16-audio element")
	}
}

func TestPCM16Audio_StateChanges(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-pcm16-audio-states")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	transcoder, err := gst.NewElement("pcm16-audio")
	if err != nil {
		t.Fatal("failed to create pcm16-audio:", err)
	}

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}

	src, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}

	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=48000,channels=1,format=S16LE"))

	if err := pipeline.AddMany(src, capsFilter, transcoder, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := gst.ElementLinkMany(src, capsFilter, transcoder, sink); err != nil {
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

func TestPCM16Audio_WithSampleWriter_WAV(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	// Decode the embedded OGG file into PCM16 frames
	frames := res.ReadOggAudioFile(res.RoomJoinOgg)
	t.Logf("decoded %d PCM16 frames from room_join.ogg", len(frames))
	if len(frames) == 0 {
		t.Fatal("no frames decoded from room_join.ogg")
	}

	// Output file
	outDir := filepath.Join("testdata")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal("failed to create testdata dir:", err)
	}
	outPath := filepath.Join(outDir, "room_join.wav")
	t.Logf("WAV output path: %s", outPath)

	pipeline, err := gst.NewPipeline("test-pcm16-samplewriter-wav")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	// Create samplewriter with real audio frames
	src, err := samplewriter.NewSampleWriter(context.Background(), rtp.DefFrameDur, res.SampleRate, frames)
	if err != nil {
		t.Fatal("failed to create samplewriter:", err)
	}

	transcoder, err := gst.NewElement("pcm16-audio")
	if err != nil {
		t.Fatal("failed to create pcm16-audio:", err)
	}

	wavEnc, err := gst.NewElement("wavenc")
	if err != nil {
		t.Fatal("failed to create wavenc:", err)
	}

	sink, err := gst.NewElementWithProperties("filesink", map[string]any{
		"location": outPath,
	})
	if err != nil {
		t.Fatal("failed to create filesink:", err)
	}

	if err := pipeline.AddMany(src, transcoder, wavEnc, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := gst.ElementLinkMany(src, transcoder, wavEnc, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	// Count buffers flowing through pcm16-audio
	var bufferCount atomic.Int32
	transcoderSrcPad := transcoder.GetStaticPad("src")
	if transcoderSrcPad == nil {
		t.Fatal("failed to get src pad from pcm16-audio")
	}
	transcoderSrcPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	bus := pipeline.GetPipelineBus()
	pollTimeout := gst.ClockTime(time.Second)
	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		msg := bus.TimedPop(pollTimeout)
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
	t.Fatal("timed out waiting for EOS")

done:
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers through pcm16-audio", count)
	if count <= 0 {
		t.Fatal("no buffers flowed through pcm16-audio")
	}

	// Verify the WAV file was created and is valid
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatal("output WAV file not found:", err)
	}
	t.Logf("output WAV file size: %d bytes", info.Size())
	if info.Size() <= 44 {
		t.Fatal("WAV file too small — only header or empty")
	}
}
