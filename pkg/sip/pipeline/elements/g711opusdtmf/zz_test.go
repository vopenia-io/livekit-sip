package g711opusdtmf

import (
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
)

func TestMain(m *testing.M) {
	gst.Init(nil)
	Register()

	code := m.Run()

	for i := 0; i < 5; i++ {
		runtime.GC()
		time.Sleep(100 * time.Millisecond)
	}

	syscall.Kill(syscall.Getpid(), syscall.SIGUSR1)
	time.Sleep(1 * time.Second)

	os.Exit(code)
}

func TestG711OpusDtmf_Inband(t *testing.T) {
	tests := []struct {
		name      string
		encoder   string
		payloader string
		dotFile   string
	}{
		{
			name:      "PCMU/Inband",
			encoder:   "mulawenc",
			payloader: "rtppcmupay",
			dotFile:   "g711_opus_dtmf_pcmu_inband_test.dot",
		},
		{
			name:      "PCMA/Inband",
			encoder:   "alawenc",
			payloader: "rtppcmapay",
			dotFile:   "g711_opus_dtmf_pcma_inband_test.dot",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pipeline, err := gst.NewPipeline(fmt.Sprintf("test-g711-opus-dtmf-inband-%s", tc.name))
			if err != nil {
				t.Fatal("failed to create pipeline:", err)
			}

			// Two sine waves at DTMF digit '1' frequencies
			audioSrc1, err := gst.NewElement("audiotestsrc")
			if err != nil {
				t.Fatal("failed to create audiotestsrc (697Hz):", err)
			}
			audioSrc1.SetProperty("wave", 0) // sine
			audioSrc1.SetProperty("freq", 697.0)
			audioSrc1.SetProperty("volume", 0.5)
			audioSrc1.SetProperty("num-buffers", 150)

			audioSrc2, err := gst.NewElement("audiotestsrc")
			if err != nil {
				t.Fatal("failed to create audiotestsrc (1209Hz):", err)
			}
			audioSrc2.SetProperty("wave", 0) // sine
			audioSrc2.SetProperty("freq", 1209.0)
			audioSrc2.SetProperty("volume", 0.5)
			audioSrc2.SetProperty("num-buffers", 150)

			adder, err := gst.NewElement("adder")
			if err != nil {
				t.Fatal("failed to create adder:", err)
			}

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

			transcoder, err := gst.NewElement("g711-opus-dtmf")
			if err != nil {
				t.Fatal("failed to create g711-opus-dtmf:", err)
			}

			sink, err := gst.NewElement("fakesink")
			if err != nil {
				t.Fatal("failed to create fakesink:", err)
			}
			sink.SetProperty("sync", false)

			if err := pipeline.AddMany(audioSrc1, audioSrc2, adder, capsFilter, encoder, payloader, transcoder, sink); err != nil {
				t.Fatal("failed to add elements to pipeline:", err)
			}

			// Manual pad linking for adder request pads
			if ret := audioSrc1.GetStaticPad("src").Link(adder.GetRequestPad("sink_%u")); ret != gst.PadLinkOK {
				t.Fatalf("failed to link audioSrc1 to adder: %v", ret)
			}
			if ret := audioSrc2.GetStaticPad("src").Link(adder.GetRequestPad("sink_%u")); ret != gst.PadLinkOK {
				t.Fatalf("failed to link audioSrc2 to adder: %v", ret)
			}

			// Link rest of the chain
			if err := gst.ElementLinkMany(adder, capsFilter, encoder, payloader, transcoder, sink); err != nil {
				t.Fatal("failed to link elements:", err)
			}

			// Pad probe to count output buffers
			var bufferCount atomic.Int32
			sinkPad := sink.GetStaticPad("sink")
			if sinkPad == nil {
				t.Fatal("failed to get sink pad from fakesink")
			}
			sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
				bufferCount.Add(1)
				return gst.PadProbeOK
			})

			// DTMF detection counter
			var dtmfDetected atomic.Int32

			if err := pipeline.SetState(gst.StatePlaying); err != nil {
				t.Fatal("failed to set pipeline to PLAYING:", err)
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
					t.Fatal("pipeline error:", gerr.Error())
				case gst.MessageElement:
					structure := msg.GetStructure()
					if structure != nil && structure.Name() == "dtmf-event" {
						dtmfDetected.Add(1)
						if nbVal, err := structure.GetValue("number"); err == nil {
							t.Logf("DTMF event detected: number=%v", nbVal)
						}
					}
				}
			}
			t.Fatal("pipeline timed out waiting for EOS")

		done:
			dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
			if err := os.WriteFile(tc.dotFile, []byte(dotData), 0644); err != nil {
				t.Logf("failed to write DOT file: %v", err)
			}

			if err := pipeline.SetState(gst.StateNull); err != nil {
				t.Fatal("failed to set pipeline to NULL:", err)
			}

			count := bufferCount.Load()
			t.Logf("received %d buffers", count)
			if count <= 0 {
				t.Fatalf("no buffers received through g711-opus-dtmf element (%s path)", tc.name)
			}

			dtmfCount := dtmfDetected.Load()
			t.Logf("received %d DTMF events", dtmfCount)
			if dtmfCount <= 0 {
				t.Fatalf("no DTMF events detected (%s path)", tc.name)
			}

			for i := 0; i < 5; i++ {
				runtime.GC()
				time.Sleep(100 * time.Millisecond)
			}

			syscall.Kill(syscall.Getpid(), syscall.SIGUSR1)
			time.Sleep(1 * time.Second)
		})
	}
}

func TestG711OpusDtmf_Outofband(t *testing.T) {
	tests := []struct {
		name      string
		encoder   string
		payloader string
		dotFile   string
	}{
		{
			name:      "PCMU/Outofband",
			encoder:   "mulawenc",
			payloader: "rtppcmupay",
			dotFile:   "g711_opus_dtmf_pcmu_outofband_test.dot",
		},
		{
			name:      "PCMA/Outofband",
			encoder:   "alawenc",
			payloader: "rtppcmapay",
			dotFile:   "g711_opus_dtmf_pcma_outofband_test.dot",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pipeline, err := gst.NewPipeline(fmt.Sprintf("test-g711-opus-dtmf-outofband-%s", tc.name))
			if err != nil {
				t.Fatal("failed to create pipeline:", err)
			}

			// Audio source (live, so it paces itself and gives DTMF goroutine time)
			audioSrc, err := gst.NewElement("audiotestsrc")
			if err != nil {
				t.Fatal("failed to create audiotestsrc:", err)
			}
			audioSrc.SetProperty("is-live", true)
			audioSrc.SetProperty("num-buffers", 10)

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

			transcoder, err := gst.NewElement("g711-opus-dtmf")
			if err != nil {
				t.Fatal("failed to create g711-opus-dtmf:", err)
			}

			sink, err := gst.NewElement("fakesink")
			if err != nil {
				t.Fatal("failed to create fakesink:", err)
			}
			sink.SetProperty("sync", false)

			// DTMF RTP source
			dtmfSrc, err := gst.NewElement("rtpdtmfsrc")
			if err != nil {
				t.Fatal("failed to create rtpdtmfsrc:", err)
			}
			dtmfSrc.SetProperty("clock-rate", 8000)

			if err := pipeline.AddMany(audioSrc, capsFilter, encoder, payloader, transcoder, sink, dtmfSrc); err != nil {
				t.Fatal("failed to add elements to pipeline:", err)
			}

			// Link audio chain normally
			if err := gst.ElementLinkMany(audioSrc, capsFilter, encoder, payloader, transcoder, sink); err != nil {
				t.Fatal("failed to link audio elements:", err)
			}

			// Manual pad link: rtpdtmfsrc → sink_dtmf
			dtmfSrcPad := dtmfSrc.GetStaticPad("src")
			if dtmfSrcPad == nil {
				t.Fatal("failed to get src pad from rtpdtmfsrc")
			}
			dtmfSinkPad := transcoder.GetStaticPad("sink_dtmf")
			if dtmfSinkPad == nil {
				t.Fatal("failed to get sink_dtmf pad from transcoder")
			}
			if ret := dtmfSrcPad.Link(dtmfSinkPad); ret != gst.PadLinkOK {
				t.Fatalf("failed to link rtpdtmfsrc to sink_dtmf: %v", ret)
			}

			// Pad probe to count output buffers
			var bufferCount atomic.Int32
			sinkPad := sink.GetStaticPad("sink")
			if sinkPad == nil {
				t.Fatal("failed to get sink pad from fakesink")
			}
			sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
				bufferCount.Add(1)
				return gst.PadProbeOK
			})

			// DTMF detection counter
			var dtmfDetected atomic.Int32

			if err := pipeline.SetState(gst.StatePlaying); err != nil {
				t.Fatal("failed to set pipeline to PLAYING:", err)
			}

			// Goroutine to trigger DTMF event on rtpdtmfsrc
			go func() {
				time.Sleep(200 * time.Millisecond)

				// Start DTMF tone
				startStructure := gst.NewStructure("dtmf-event")
				startStructure.SetValue("type", 1)
				startStructure.SetValue("number", 1)
				startStructure.SetValue("volume", 25)
				startStructure.SetValue("start", true)
				startStructure.SetValue("method", 2)
				runtime.SetFinalizer(startStructure, nil)
				startEvent := gst.NewCustomEvent(gst.EventTypeCustomUpstream, startStructure)
				if !dtmfSrc.SendEvent(startEvent) {
					t.Log("warning: failed to send DTMF start event")
				}

				time.Sleep(300 * time.Millisecond)

				// Stop DTMF tone
				stopStructure := gst.NewStructure("dtmf-event")
				stopStructure.SetValue("type", 1)
				stopStructure.SetValue("number", 1)
				stopStructure.SetValue("volume", 25)
				stopStructure.SetValue("start", false)
				stopStructure.SetValue("method", 2)
				runtime.SetFinalizer(stopStructure, nil)
				stopEvent := gst.NewCustomEvent(gst.EventTypeCustomUpstream, stopStructure)
				if !dtmfSrc.SendEvent(stopEvent) {
					t.Log("warning: failed to send DTMF stop event")
				}
			}()

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
				case gst.MessageElement:
					structure := msg.GetStructure()
					if structure != nil && structure.Name() == "dtmf-event" {
						dtmfDetected.Add(1)
						if nbVal, err := structure.GetValue("number"); err == nil {
							t.Logf("DTMF event detected: number=%v", nbVal)
						}
					}
				}
			}
			t.Fatal("pipeline timed out waiting for EOS")

		done:
			dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
			if err := os.WriteFile(tc.dotFile, []byte(dotData), 0644); err != nil {
				t.Logf("failed to write DOT file: %v", err)
			}

			if err := pipeline.SetState(gst.StateNull); err != nil {
				t.Fatal("failed to set pipeline to NULL:", err)
			}

			count := bufferCount.Load()
			t.Logf("received %d buffers", count)
			if count <= 0 {
				t.Fatalf("no buffers received through g711-opus-dtmf element (%s path)", tc.name)
			}

			dtmfCount := dtmfDetected.Load()
			t.Logf("received %d DTMF events", dtmfCount)
			if dtmfCount <= 0 {
				t.Fatalf("no DTMF events detected (%s path)", tc.name)
			}

			for i := 0; i < 5; i++ {
				runtime.GC()
				time.Sleep(100 * time.Millisecond)
			}

			syscall.Kill(syscall.Getpid(), syscall.SIGUSR1)
			time.Sleep(1 * time.Second)
		})
	}
}
