package g711opus

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	gst.Init(nil)
	Register()
	os.Exit(m.Run())
}

func TestG711Opus_Pipeline(t *testing.T) {
	tests := []struct {
		name      string
		encoder   string
		payloader string
		dotFile   string
	}{
		{
			name:      "PCMU",
			encoder:   "mulawenc",
			payloader: "rtppcmupay",
			dotFile:   "g711_opus_pcmu_test.dot",
		},
		{
			name:      "PCMA",
			encoder:   "alawenc",
			payloader: "rtppcmapay",
			dotFile:   "g711_opus_pcma_test.dot",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer testutils.AssertNoLeaks(t)

			// Create pipeline
			pipeline, err := gst.NewPipeline(fmt.Sprintf("test-g711-opus-%s", tc.name))
			if err != nil {
				t.Fatal("failed to create pipeline:", err)
			}

			// Create source elements
			audioSrc, err := gst.NewElement("audiotestsrc")
			if err != nil {
				t.Fatal("failed to create audiotestsrc:", err)
			}
			audioSrc.SetProperty("num-buffers", 150)

			capsFilter, err := gst.NewElement("capsfilter")
			if err != nil {
				t.Fatal("failed to create capsfilter:", err)
			}
			capsFilter.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=8000,channels=1,format=S16LE"))

			encoder, err := gst.NewElement(tc.encoder)
			if err != nil {
				t.Fatalf("failed to create %s: %v", tc.encoder, err)
			}

			payloader, err := gst.NewElement(tc.payloader)
			if err != nil {
				t.Fatalf("failed to create %s: %v", tc.payloader, err)
			}

			// Element under test
			transcoder, err := gst.NewElement("g711-opus")
			if err != nil {
				t.Fatal("failed to create g711-opus:", err)
			}

			sink, err := gst.NewElement("fakesink")
			if err != nil {
				t.Fatal("failed to create fakesink:", err)
			}
			sink.SetProperty("sync", false)

			// Add all elements to pipeline
			if err := pipeline.AddMany(audioSrc, capsFilter, encoder, payloader, transcoder, sink); err != nil {
				t.Fatal("failed to add elements to pipeline:", err)
			}

			// Link the full chain
			if err := gst.ElementLinkMany(audioSrc, capsFilter, encoder, payloader, transcoder, sink); err != nil {
				t.Fatal("failed to link elements:", err)
			}

			// Install pad probe to count output buffers
			var bufferCount atomic.Int32
			sinkPad := sink.GetStaticPad("sink")
			if sinkPad == nil {
				t.Fatal("failed to get sink pad from fakesink")
			}
			sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
				bufferCount.Add(1)
				return gst.PadProbeOK
			})

			// Start pipeline
			if err := pipeline.SetState(gst.StatePlaying); err != nil {
				t.Fatal("failed to set pipeline to PLAYING:", err)
			}

			// Poll bus for EOS or error
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
					t.Fatal("pipeline error:", gerr.Error())
				}
			}
			t.Fatal("pipeline timed out waiting for EOS")

		done:
			// Dump pipeline DOT graph before teardown
			dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
			if err := os.WriteFile(tc.dotFile, []byte(dotData), 0644); err != nil {
				t.Logf("failed to write DOT file: %v", err)
			}

			// Stop pipeline
			if err := pipeline.SetState(gst.StateNull); err != nil {
				t.Fatal("failed to set pipeline to NULL:", err)
			}

			// Verify data flowed through the element
			count := bufferCount.Load()
			t.Logf("received %d buffers", count)
			if count <= 0 {
				t.Fatalf("no buffers received through g711-opus element (%s path)", tc.name)
			}
		})
	}
}
