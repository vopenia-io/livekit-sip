package rambench

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audiog711"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/opusaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp8video"
)

func TestMain(m *testing.M) {
	gst.Init(nil)
	opusaudio.Register()
	audiog711.Register()
	vp8video.Register()
	videovp8.Register()
	os.Exit(m.Run())
}

type memStats struct {
	GoAlloc   uint64
	GoSys     uint64
	HeapInuse uint64
	RSSPages  uint64
	PageSize  uint64
}

func readMemStats() memStats {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	s := memStats{
		GoAlloc:   ms.Alloc,
		GoSys:     ms.Sys,
		HeapInuse: ms.HeapInuse,
		PageSize:  uint64(os.Getpagesize()),
	}

	data, err := os.ReadFile("/proc/self/statm")
	if err == nil {
		fields := strings.Fields(strings.TrimSpace(string(data)))
		if len(fields) >= 2 {
			rss, _ := strconv.ParseUint(fields[1], 10, 64)
			s.RSSPages = rss
		}
	}

	return s
}

func (s memStats) RSSBytes() uint64 {
	return s.RSSPages * s.PageSize
}

func (s memStats) Log(t *testing.T, label string) {
	t.Logf("%s: RSS=%d MB, GoAlloc=%d MB, GoSys=%d MB, HeapInuse=%d MB",
		label,
		s.RSSBytes()/(1024*1024),
		s.GoAlloc/(1024*1024),
		s.GoSys/(1024*1024),
		s.HeapInuse/(1024*1024),
	)
}

func buildPipeline(t *testing.T, name string, videoActive bool) *gst.Pipeline {
	t.Helper()

	pipeline, err := gst.NewPipeline(name)
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	// --- Audio chain ---
	audioSrc, err := gst.NewElement("audiotestsrc")
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	audioSrc.SetProperty("is-live", true)

	audioCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create audio capsfilter:", err)
	}
	audioCaps.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=48000,channels=1,format=S16LE"))

	opusEnc, err := gst.NewElement("opusenc")
	if err != nil {
		t.Fatal("failed to create opusenc:", err)
	}

	rtpOpusPay, err := gst.NewElement("rtpopuspay")
	if err != nil {
		t.Fatal("failed to create rtpopuspay:", err)
	}

	opusAudio, err := gst.NewElement("opus-audio")
	if err != nil {
		t.Fatal("failed to create opus-audio:", err)
	}

	audioG711, err := gst.NewElement("audio-g711")
	if err != nil {
		t.Fatal("failed to create audio-g711:", err)
	}

	audioOutCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create audio output capsfilter:", err)
	}
	audioOutCaps.SetProperty("caps", gst.NewCapsFromString("application/x-rtp,media=audio,clock-rate=8000,encoding-name=PCMU"))

	audioSink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create audio fakesink:", err)
	}
	audioSink.SetProperty("sync", false)

	// --- Video chain ---
	videoSrc, err := gst.NewElement("videotestsrc")
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoSrc.SetProperty("is-live", true)

	videoCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create video capsfilter:", err)
	}
	videoCaps.SetProperty("caps", gst.NewCapsFromString("video/x-raw,width=320,height=240,framerate=15/1"))

	vp8Enc, err := gst.NewElement("vp8enc")
	if err != nil {
		t.Fatal("failed to create vp8enc:", err)
	}

	rtpVp8Pay, err := gst.NewElement("rtpvp8pay")
	if err != nil {
		t.Fatal("failed to create rtpvp8pay:", err)
	}

	valve, err := gst.NewElement("valve")
	if err != nil {
		t.Fatal("failed to create valve:", err)
	}
	if !videoActive {
		valve.SetProperty("drop", true)
		valve.SetProperty("drop-mode", 1)
	}

	vp8Video, err := gst.NewElement("vp8-video")
	if err != nil {
		t.Fatal("failed to create vp8-video:", err)
	}

	videoVp8, err := gst.NewElement("video-vp8")
	if err != nil {
		t.Fatal("failed to create video-vp8:", err)
	}

	videoSink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create video fakesink:", err)
	}
	videoSink.SetProperty("sync", false)

	// Add all elements
	if err := pipeline.AddMany(
		audioSrc, audioCaps, opusEnc, rtpOpusPay, opusAudio, audioG711, audioOutCaps, audioSink,
		videoSrc, videoCaps, vp8Enc, rtpVp8Pay, valve, vp8Video, videoVp8, videoSink,
	); err != nil {
		t.Fatal("failed to add elements:", err)
	}

	// Link audio chain
	if err := gst.ElementLinkMany(audioSrc, audioCaps, opusEnc, rtpOpusPay, opusAudio, audioG711, audioOutCaps, audioSink); err != nil {
		t.Fatal("failed to link audio chain:", err)
	}

	// Link video chain
	if err := gst.ElementLinkMany(videoSrc, videoCaps, vp8Enc, rtpVp8Pay, valve, vp8Video, videoVp8, videoSink); err != nil {
		t.Fatal("failed to link video chain:", err)
	}

	return pipeline
}

func TestRAMUsage(t *testing.T) {
	tests := []struct {
		name        string
		videoActive bool
	}{
		{"AllActive", true},
		{"VideoStarved", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runtime.GC()

			before := readMemStats()
			before.Log(t, "before")

			pipeline := buildPipeline(t, fmt.Sprintf("ram-%s", tc.name), tc.videoActive)

			if err := pipeline.SetState(gst.StatePlaying); err != nil {
				t.Fatal("failed to set pipeline to PLAYING:", err)
			}

			// Run for 10 seconds, checking for errors on the bus
			bus := pipeline.GetPipelineBus()
			deadline := time.Now().Add(10 * time.Second)
			timeout := gst.ClockTime(time.Second)

			for time.Now().Before(deadline) {
				msg := bus.TimedPop(timeout)
				if msg == nil {
					continue
				}
				if msg.Type() == gst.MessageError {
					gerr := msg.ParseError()
					t.Fatal("pipeline error:", gerr.Error())
				}
			}

			runtime.GC()
			after := readMemStats()
			after.Log(t, "after 10s")

			t.Logf("delta: RSS=%+d MB, GoAlloc=%+d MB, HeapInuse=%+d MB",
				(int64(after.RSSBytes())-int64(before.RSSBytes()))/(1024*1024),
				(int64(after.GoAlloc)-int64(before.GoAlloc))/(1024*1024),
				(int64(after.HeapInuse)-int64(before.HeapInuse))/(1024*1024),
			)

			if err := pipeline.SetState(gst.StateNull); err != nil {
				t.Fatal("failed to set pipeline to NULL:", err)
			}
		})
	}
}
