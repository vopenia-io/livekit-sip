package rtph264capsintersect

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

// --- Unit tests for RFC 6184 profile-level-id parsing ---

func TestParseProfileLevelID_ConstrainedBaseline(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"0x42 with csf1", "42e01f"},
		{"0x42 with csf1 alt", "42c01f"},
		{"0x4D with csf0", "4de01f"},
		{"0x58 with csf0+csf1", "58c01f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseProfileLevelID(tt.input)
			if err != nil {
				t.Fatalf("parseProfileLevelID(%q) error: %v", tt.input, err)
			}
			if p.profile != profileConstrainedBaseline {
				t.Errorf("parseProfileLevelID(%q).profile = %d, want profileConstrainedBaseline", tt.input, p.profile)
			}
		})
	}
}

func TestParseProfileLevelID_Baseline(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"0x42 no csf1", "42001f"},
		{"0x58 csf0 only", "58801f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseProfileLevelID(tt.input)
			if err != nil {
				t.Fatalf("parseProfileLevelID(%q) error: %v", tt.input, err)
			}
			if p.profile != profileBaseline {
				t.Errorf("parseProfileLevelID(%q).profile = %d, want profileBaseline", tt.input, p.profile)
			}
		})
	}
}

func TestParseProfileLevelID_Main(t *testing.T) {
	p, err := parseProfileLevelID("4d001f")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if p.profile != profileMain {
		t.Errorf("profile = %d, want profileMain", p.profile)
	}
}

func TestParseProfileLevelID_High(t *testing.T) {
	p, err := parseProfileLevelID("640028")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if p.profile != profileHigh {
		t.Errorf("profile = %d, want profileHigh", p.profile)
	}
}

func TestParseProfileLevelID_ConstrainedHigh(t *testing.T) {
	p, err := parseProfileLevelID("640c28")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if p.profile != profileConstrainedHigh {
		t.Errorf("profile = %d, want profileConstrainedHigh", p.profile)
	}
}

func TestParseProfileLevelID_Level1b(t *testing.T) {
	// Baseline-family: levelIDC=11, csf3 set → 1b
	p, err := parseProfileLevelID("42f00b")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !p.isLevel1b {
		t.Error("expected Level 1b for 42f00b")
	}

	// Baseline-family: levelIDC=11, csf3 NOT set → 1.1
	p, err = parseProfileLevelID("42e00b")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if p.isLevel1b {
		t.Error("expected Level 1.1 (not 1b) for 42e00b")
	}

	// High profile: levelIDC=9 → 1b
	p, err = parseProfileLevelID("640009")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !p.isLevel1b {
		t.Error("expected Level 1b for 640009")
	}
}

func TestParseProfileLevelID_Invalid(t *testing.T) {
	for _, input := range []string{"", "42e0", "42e01", "zzzzzz", "42e01faa"} {
		_, err := parseProfileLevelID(input)
		if err == nil {
			t.Errorf("expected error for %q", input)
		}
	}
}

func TestIntersectProfileLevelID_SameProfile(t *testing.T) {
	// Both CB Level 3.1 via different encodings → returns downstream string verbatim
	result, ok := intersectProfileLevelID("42c01f", "42e01f")
	if !ok {
		t.Fatal("expected compatible profiles")
	}
	if result != "42e01f" {
		t.Errorf("intersect(42c01f, 42e01f) = %s, want 42e01f", result)
	}

	// CB Level 4.0 upstream vs CB Level 3.1 downstream → downstream level is lower, return downstream verbatim
	result, ok = intersectProfileLevelID("42e028", "42e01f")
	if !ok {
		t.Fatal("expected compatible profiles")
	}
	if result != "42e01f" {
		t.Errorf("intersect(42e028, 42e01f) = %s, want 42e01f", result)
	}

	// CB via 0x4D vs CB via 0x42 → returns downstream string verbatim
	result, ok = intersectProfileLevelID("4de01f", "42c01f")
	if !ok {
		t.Fatal("expected compatible profiles")
	}
	if result != "42c01f" {
		t.Errorf("intersect(4de01f, 42c01f) = %s, want 42c01f", result)
	}

	// Case preservation: downstream has uppercase → returned verbatim
	result, ok = intersectProfileLevelID("42c01f", "42E01F")
	if !ok {
		t.Fatal("expected compatible profiles")
	}
	if result != "42E01F" {
		t.Errorf("intersect(42c01f, 42E01F) = %s, want 42E01F (case preserved)", result)
	}
}

func TestIntersectProfileLevelID_CompatibleSubset(t *testing.T) {
	// CB (42c01f) vs Baseline (42801F) → compatible, returns downstream verbatim
	result, ok := intersectProfileLevelID("42c01f", "42801F")
	if !ok {
		t.Fatal("expected compatible for CB vs Baseline")
	}
	if result != "42801F" {
		t.Errorf("intersect(42c01f, 42801F) = %s, want 42801F (downstream verbatim)", result)
	}

	// Baseline vs CB (reversed order) — returns downstream verbatim
	result, ok = intersectProfileLevelID("42801f", "42e01f")
	if !ok {
		t.Fatal("expected compatible for Baseline vs CB")
	}
	if result != "42e01f" {
		t.Errorf("intersect(42801f, 42e01f) = %s, want 42e01f", result)
	}

	// Constrained High vs High → compatible, returns downstream verbatim
	result, ok = intersectProfileLevelID("640c28", "640028")
	if !ok {
		t.Fatal("expected compatible for CH vs High")
	}
	if result != "640028" {
		t.Errorf("intersect(640c28, 640028) = %s, want 640028", result)
	}
}

func TestIntersectProfileLevelID_DifferentProfile(t *testing.T) {
	_, ok := intersectProfileLevelID("42e01f", "4d001f")
	if ok {
		t.Error("expected incompatible for CB vs Main")
	}

	_, ok = intersectProfileLevelID("42e01f", "640028")
	if ok {
		t.Error("expected incompatible for CB vs High")
	}
}

func TestIntersectProfileLevelID_Level1b(t *testing.T) {
	// CB Level 1b (upstream) vs CB Level 1.1 (downstream) → min = 1b
	// Upstream level < downstream, so we emit downstream's encoding (0x42, 0xe0) + upstream's level 1b.
	// Level 1b for baseline-family: levelIDC=0x0B, csf3 set → 0xe0|0x10 = 0xf0
	result, ok := intersectProfileLevelID("42f00b", "42e00b")
	if !ok {
		t.Fatal("expected compatible")
	}
	if result != "42f00b" {
		t.Errorf("intersect(42f00b, 42e00b) = %s, want 42f00b", result)
	}

	// Same level on both sides → return downstream verbatim
	result, ok = intersectProfileLevelID("42f00b", "42f00b")
	if !ok {
		t.Fatal("expected compatible")
	}
	if result != "42f00b" {
		t.Errorf("intersect(42f00b, 42f00b) = %s, want 42f00b", result)
	}
}

func TestIntersectProfileLevelID_DefaultUpstream(t *testing.T) {
	// Empty upstream defaults to 42e01f (CB Level 3.1) vs CB Level 4.0
	// upstream level 3.1 < downstream level 4.0 → emit downstream encoding with upstream level
	result, ok := intersectProfileLevelID("", "42e028")
	if !ok {
		t.Fatal("expected compatible")
	}
	// downstream encoding (0x42, 0xe0) + upstream level 0x1f → "42e01f"
	if result != "42e01f" {
		t.Errorf("intersect('', 42e028) = %s, want 42e01f", result)
	}
}

func TestIntersectProfileLevelID_EmptyDownstream(t *testing.T) {
	// Empty downstream → pass through upstream
	result, ok := intersectProfileLevelID("42c01f", "")
	if !ok {
		t.Fatal("expected compatible")
	}
	if result != "42c01f" {
		t.Errorf("intersect(42c01f, '') = %s, want 42c01f", result)
	}
}

func TestLevelOrd(t *testing.T) {
	// Verify ordering: 1 < 1b < 1.1
	l1, _ := parseProfileLevelID("42e00a")   // Level 1.0
	l1b, _ := parseProfileLevelID("42f00b")  // Level 1b
	l11, _ := parseProfileLevelID("42e00b")  // Level 1.1
	l31, _ := parseProfileLevelID("42e01f")  // Level 3.1

	if levelOrd(l1) >= levelOrd(l1b) {
		t.Errorf("Level 1.0 (%d) should be < Level 1b (%d)", levelOrd(l1), levelOrd(l1b))
	}
	if levelOrd(l1b) >= levelOrd(l11) {
		t.Errorf("Level 1b (%d) should be < Level 1.1 (%d)", levelOrd(l1b), levelOrd(l11))
	}
	if levelOrd(l11) >= levelOrd(l31) {
		t.Errorf("Level 1.1 (%d) should be < Level 3.1 (%d)", levelOrd(l11), levelOrd(l31))
	}
}

// --- Integration tests (pipeline-based) ---

func TestPipeline_CompatibleCaps(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-rtph264capsintersect")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	videoSrc, err := gst.NewElement("videotestsrc")
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoSrc.SetProperty("num-buffers", 50)

	rawCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	rawCapsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-raw,width=320,height=240,framerate=15/1"))

	encoder, err := gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"speed-preset": 1,
		"tune":         4,
		"key-int-max":  30,
	})
	if err != nil {
		t.Fatal("failed to create x264enc:", err)
	}

	payloader, err := gst.NewElement("rtph264pay")
	if err != nil {
		t.Fatal("failed to create rtph264pay:", err)
	}

	intersect, err := gst.NewElement("rtph264capsintersect")
	if err != nil {
		t.Fatal("failed to create rtph264capsintersect:", err)
	}

	// Downstream capsfilter with a CB profile-level-id that may differ in string form
	rtpCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	rtpCapsFilter.SetProperty("caps", gst.NewCapsFromString(
		"application/x-rtp, media=(string)video, encoding-name=(string)H264, profile-level-id=(string)42e01f"))

	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(videoSrc, rawCapsFilter, encoder, payloader, intersect, rtpCapsFilter, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}

	if err := gst.ElementLinkMany(videoSrc, rawCapsFilter, encoder, payloader, intersect, rtpCapsFilter, sink); err != nil {
		t.Fatal("failed to link elements:", err)
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
	dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
	if err := os.WriteFile("rtph264capsintersect_test.dot", []byte(dotData), 0644); err != nil {
		t.Logf("failed to write DOT file: %v", err)
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers", count)
	if count <= 0 {
		t.Fatal("no buffers received through rtph264capsintersect element")
	}
}

func TestPipeline_IncompatibleCaps(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-incompatible")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	videoSrc, err := gst.NewElement("videotestsrc")
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoSrc.SetProperty("num-buffers", 10)

	rawCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	rawCapsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-raw,width=320,height=240,framerate=15/1"))

	encoder, err := gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"speed-preset": 1,
		"tune":         4,
		"key-int-max":  30,
	})
	if err != nil {
		t.Fatal("failed to create x264enc:", err)
	}

	payloader, err := gst.NewElement("rtph264pay")
	if err != nil {
		t.Fatal("failed to create rtph264pay:", err)
	}

	intersectElem, err := gst.NewElement("rtph264capsintersect")
	if err != nil {
		t.Fatal("failed to create rtph264capsintersect:", err)
	}

	// Downstream requires High profile — incompatible with x264enc's CB/Baseline output
	rtpCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	rtpCapsFilter.SetProperty("caps", gst.NewCapsFromString(
		"application/x-rtp, media=(string)video, encoding-name=(string)H264, profile-level-id=(string)640028"))

	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(videoSrc, rawCapsFilter, encoder, payloader, intersectElem, rtpCapsFilter, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}

	if err := gst.ElementLinkMany(videoSrc, rawCapsFilter, encoder, payloader, intersectElem, rtpCapsFilter, sink); err != nil {
		// Link failure is expected — incompatible caps
		t.Logf("link failed as expected: %v", err)
		pipeline.SetState(gst.StateNull)
		return
	}

	// If linking succeeded, try to play — should fail during negotiation
	err = pipeline.SetState(gst.StatePlaying)
	if err != nil {
		t.Logf("set state failed as expected: %v", err)
		pipeline.SetState(gst.StateNull)
		return
	}

	// Wait briefly for error on bus
	bus := pipeline.GetPipelineBus()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := bus.TimedPop(gst.ClockTime(time.Second))
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageError:
			t.Logf("pipeline error as expected: %v", msg.ParseError())
			pipeline.SetState(gst.StateNull)
			return
		case gst.MessageEOS:
			t.Fatal("unexpected EOS — negotiation should have failed")
		}
	}

	pipeline.SetState(gst.StateNull)
	t.Log("pipeline did not produce data (negotiation blocked as expected)")
	_ = fmt.Sprintf("") // keep fmt imported
}
