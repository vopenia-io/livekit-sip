package benchmarks

import (
	"fmt"
	"os"
	"testing"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/rtph264capsintersect"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/av1video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/factorybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/h264video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvav1video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvh264video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvideoav1"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvideoh264"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvideovp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvideovp9"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvp8video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvp9video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videoav1"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videoh264"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp9"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp8video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp9video"
)

func TestMain(m *testing.M) {
	// Leaks tracer is deliberately off: this package is for latency +
	// CPU comparisons, leak detection is covered by the per-element
	// smoke tests.
	if err := os.MkdirAll("testdata", 0755); err != nil {
		panic(err)
	}
	_ = os.Remove(TraceLogPath)
	glib.SetEnv("GST_TRACERS", "latency(flags=element);rusage", true)
	glib.SetEnv("GST_DEBUG", "GST_TRACER:7", true)
	glib.SetEnv("GST_DEBUG_FILE", TraceLogPath, true)
	gst.Init(nil)
	rtph264capsintersect.Register()
	factorybin.Register()
	vp9video.Register()
	videovp9.Register()
	nvvp9video.Register()
	nvvideovp9.Register()
	h264video.Register()
	videoh264.Register()
	nvh264video.Register()
	nvvideoh264.Register()
	vp8video.Register()
	videovp8.Register()
	nvvp8video.Register()
	nvvideovp8.Register()
	av1video.Register()
	videoav1.Register()
	nvav1video.Register()
	nvvideoav1.Register()
	os.Exit(m.Run())
}

// 16:9 widescreen resolution matrix, no upscaling.
var resolutionPairs = []struct {
	srcW, srcH int
	dstW, dstH int
}{
	{854, 480, 854, 480},
	{1280, 720, 854, 480},
	{1280, 720, 1280, 720},
	{1920, 1080, 854, 480},
	{1920, 1080, 1280, 720},
	{1920, 1080, 1920, 1080},
}

// Adding a new element is: import the package, add an entry here.
var elementsUnderTest = []Element{
	vp9video.Test(),
	videovp9.Test(),
	nvvp9video.Test(),
	nvvideovp9.Test(),
	h264video.Test(),
	videoh264.Test(),
	nvh264video.Test(),
	nvvideoh264.Test(),
	vp8video.Test(),
	videovp8.Test(),
	nvvp8video.Test(),
	nvvideovp8.Test(),
	av1video.Test(),
	videoav1.Test(),
	nvav1video.Test(),
	nvvideoav1.Test(),
}

func TestAllElements(t *testing.T) {
	const (
		fps        = 24
		numBuffers = 120 // 5 seconds at 24fps
	)

	var allResults []Result
	for _, elem := range elementsUnderTest {
		elem := elem
		t.Run(elem.Name(), func(t *testing.T) {
			for _, p := range resolutionPairs {
				p := p
				name := fmt.Sprintf("%dx%d_to_%dx%d", p.srcW, p.srcH, p.dstW, p.dstH)
				t.Run(name, func(t *testing.T) {
					r := Run(t, elem, Config{
						SourceWidth:  p.srcW,
						SourceHeight: p.srcH,
						SourceFPS:    fps,
						NumBuffers:   numBuffers,
						TargetWidth:  p.dstW,
						TargetHeight: p.dstH,
					})
					allResults = append(allResults, r)
				})
			}
		})
	}

	if len(allResults) == 0 {
		return
	}
	md := FormatMarkdown(allResults)
	t.Log("\n" + md)
	if err := os.WriteFile("testdata/results.md", []byte(md), 0644); err != nil {
		t.Logf("write results.md: %v", err)
	}
	if err := os.WriteFile("testdata/results.csv", []byte(FormatCSV(allResults)), 0644); err != nil {
		t.Logf("write results.csv: %v", err)
	}
}

