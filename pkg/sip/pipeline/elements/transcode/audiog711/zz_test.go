package audiog711

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",audio-g711:5", true)
	gst.Init(nil)
	Register()
	os.Exit(m.Run())
}

func TestAudioG711_Pipeline(t *testing.T) {
	tests := []struct {
		name           string
		downstreamCaps string
		dotFile        string
	}{
		{
			name:           "PCMU",
			downstreamCaps: "application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)PCMU",
			dotFile:        "audio_g711_pcmu_test.dot",
		},
		{
			name:           "PCMA",
			downstreamCaps: "application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)PCMA",
			dotFile:        "audio_g711_pcma_test.dot",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer testutils.AssertNoLeaks(t)
			if err := runAudioG711Pipeline(t, tc.name, tc.downstreamCaps, tc.dotFile); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// runAudioG711Pipeline runs in a separate function so local GStreamer
// variables go out of scope before AssertNoLeaks runs GC.
// It returns an error instead of calling t.Fatal to avoid runtime.Goexit
// keeping local variables on the stack.
func runAudioG711Pipeline(t *testing.T, name, downstreamCaps, dotFile string) error {
	t.Helper()

	pipeline, err := gst.NewPipeline(fmt.Sprintf("test-audio-g711-%s", name))
	if err != nil {
		return err
	}

	audioSrc, err := gst.NewElement("audiotestsrc")
	if err != nil {
		return err
	}
	audioSrc.SetProperty("num-buffers", 150)

	inputCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	inputCapsFilter.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=8000,channels=1,format=S16LE"))

	transcoder, err := gst.NewElement("audio-g711")
	if err != nil {
		return err
	}

	outputCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	outputCapsFilter.SetProperty("caps", gst.NewCapsFromString(downstreamCaps))

	sink, err := gst.NewElement("fakesink")
	if err != nil {
		return err
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(audioSrc, inputCapsFilter, transcoder, outputCapsFilter, sink); err != nil {
		return err
	}

	if err := gst.ElementLinkMany(audioSrc, inputCapsFilter, transcoder, outputCapsFilter, sink); err != nil {
		pipeline.SetState(gst.StateNull)
		return err
	}

	var bufferCount atomic.Int32
	sinkPad := sink.GetStaticPad("sink")
	if sinkPad == nil {
		pipeline.SetState(gst.StateNull)
		return fmt.Errorf("failed to get sink pad from fakesink")
	}
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		pipeline.SetState(gst.StateNull)
		return err
	}

	bus := pipeline.GetPipelineBus()
	timeout := gst.ClockTime(time.Second)
	deadline := time.Now().Add(60 * time.Second)

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
			pipeline.SetState(gst.StateNull)
			return fmt.Errorf("pipeline error: %s", gerr.Error())
		}
	}
	pipeline.SetState(gst.StateNull)
	return fmt.Errorf("pipeline timed out waiting for EOS")

done:
	dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
	if err := os.WriteFile(dotFile, []byte(dotData), 0644); err != nil {
		t.Logf("failed to write DOT file: %v", err)
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		return err
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers", count)
	if count <= 0 {
		return fmt.Errorf("no buffers received through audio-g711 element (%s path)", name)
	}
	return nil
}
