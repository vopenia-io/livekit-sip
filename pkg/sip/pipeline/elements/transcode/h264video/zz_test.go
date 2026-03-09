package h264video

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",h264-video:5", true)
	gst.Init(nil)
	Register()
	os.Exit(m.Run())
}

func TestH264Video_Pipeline(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-h264-video")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

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

	transcoder, err := gst.NewElement("h264-video")
	if err != nil {
		t.Fatal("failed to create h264-video:", err)
	}

	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(videoSrc, capsFilter, encoder, payloader, transcoder, sink); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}

	if err := gst.ElementLinkMany(videoSrc, capsFilter, encoder, payloader, transcoder, sink); err != nil {
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
	dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
	if err := os.WriteFile("h264_video_test.dot", []byte(dotData), 0644); err != nil {
		t.Logf("failed to write DOT file: %v", err)
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers", count)
	if count <= 0 {
		t.Fatal("no buffers received through h264-video element")
	}
}
