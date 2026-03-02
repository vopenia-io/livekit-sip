package h264vp8

import (
	"fmt"
	"os"
	"runtime"
	"strings"
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

func TestH264Vp8_Pipeline(t *testing.T) {
	// Create pipeline
	pipeline, err := gst.NewPipeline("test-h264-vp8")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	// Create source elements
	videoSrc, err := gst.NewElement("videotestsrc")
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoSrc.SetProperty("num-buffers", 150)

	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-raw,width=320,height=240,framerate=15/1"))

	encoder, err := gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"speed-preset": 1, // ultrafast
		"tune":         4, // zerolatency
		"key-int-max":  30,
	})
	if err != nil {
		t.Fatal("failed to create x264enc:", err)
	}

	payloader, err := gst.NewElement("rtph264pay")
	if err != nil {
		t.Fatal("failed to create rtph264pay:", err)
	}

	// Element under test
	transcoder, err := gst.NewElement("h264-vp8")
	if err != nil {
		t.Fatal("failed to create h264-vp8:", err)
	}

	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	// Add all elements to pipeline
	if err := pipeline.AddMany(videoSrc, capsFilter, encoder, payloader, transcoder, sink); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}

	// Link the full chain
	if err := gst.ElementLinkMany(videoSrc, capsFilter, encoder, payloader, transcoder, sink); err != nil {
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
	elementName := strings.ReplaceAll("h264-vp8", "-", "_")
	dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
	if err := os.WriteFile(fmt.Sprintf("%s_test.dot", elementName), []byte(dotData), 0644); err != nil {
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
		t.Fatal("no buffers received through h264-vp8 element")
	}
}
