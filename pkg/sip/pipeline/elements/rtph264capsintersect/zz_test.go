package rtph264capsintersect

import (
	"fmt"
	"os"
	"sync"
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
	l1, _ := parseProfileLevelID("42e00a")  // Level 1.0
	l1b, _ := parseProfileLevelID("42f00b") // Level 1b
	l11, _ := parseProfileLevelID("42e00b") // Level 1.1
	l31, _ := parseProfileLevelID("42e01f") // Level 3.1

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

// --- Unit tests for level table helpers ---

func TestMinLevelForConstraints(t *testing.T) {
	tests := []struct {
		name      string
		maxFS     uint32
		maxMBPS   uint32
		wantLevel uint8
		wantIs1b  bool
		wantFound bool
	}{
		{"level 1.0", 99, 1485, 10, false, true},
		{"level 2.2", 1620, 20250, 22, false, true},
		{"level 3.1", 3600, 108000, 31, false, true},
		{"cisco case level 4.2", 8160, 490000, 42, false, true},
		{"exact level 4.0 by fs", 8192, 245760, 40, false, true},
		{"exceeds all levels", 99999, 99999999, 0, false, false},
		{"high mbps low fs", 99, 300000, 42, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			levelIDC, is1b, found := minLevelForConstraints(tt.maxFS, tt.maxMBPS)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if !found {
				return
			}
			if levelIDC != tt.wantLevel {
				t.Errorf("levelIDC = %d, want %d", levelIDC, tt.wantLevel)
			}
			if is1b != tt.wantIs1b {
				t.Errorf("isLevel1b = %v, want %v", is1b, tt.wantIs1b)
			}
		})
	}
}

func TestBuildProfileLevelID(t *testing.T) {
	tests := []struct {
		name       string
		profileIDC uint8
		profileIOP uint8
		levelIDC   uint8
		isLevel1b  bool
		want       string
	}{
		{"CB level 3.1", 0x42, 0xe0, 0x1f, false, "42e01f"},
		{"Baseline level 4.2 (Cisco)", 0x42, 0x80, 0x2a, false, "42802a"},
		{"High level 4.0", 0x64, 0x00, 0x28, false, "640028"},
		{"CB level 1b baseline-family", 0x42, 0xe0, 0x0b, true, "42f00b"},
		{"High level 1b", 0x64, 0x00, 0x09, true, "640009"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildProfileLevelID(tt.profileIDC, tt.profileIOP, tt.levelIDC, tt.isLevel1b)
			if got != tt.want {
				t.Errorf("buildProfileLevelID(%02x, %02x, %02x, %v) = %s, want %s",
					tt.profileIDC, tt.profileIOP, tt.levelIDC, tt.isLevel1b, got, tt.want)
			}
		})
	}
}

// --- Unit tests for level limits and max resolution ---

func TestLimitsForLevel(t *testing.T) {
	tests := []struct {
		levelIDC  uint8
		isLevel1b bool
		wantFS    uint32
		wantMBPS  uint32
	}{
		{10, false, 99, 1485},
		{11, true, 99, 1485},
		{22, false, 1620, 20250},
		{31, false, 3600, 108000},
		{40, false, 8192, 245760},
	}
	for _, tt := range tests {
		name := fmt.Sprintf("level_%d_1b=%v", tt.levelIDC, tt.isLevel1b)
		t.Run(name, func(t *testing.T) {
			l := limitsForLevel(tt.levelIDC, tt.isLevel1b)
			if l == nil {
				t.Fatal("expected non-nil limits")
			}
			if l.maxFS != tt.wantFS {
				t.Errorf("maxFS = %d, want %d", l.maxFS, tt.wantFS)
			}
			if l.maxMBPS != tt.wantMBPS {
				t.Errorf("maxMBPS = %d, want %d", l.maxMBPS, tt.wantMBPS)
			}
		})
	}

	if l := limitsForLevel(99, false); l != nil {
		t.Error("expected nil for unknown level")
	}
}

func TestMaxResolutionForLevel(t *testing.T) {
	tests := []struct {
		name          string
		plid          string
		fps           int
		wantOK        bool
		minWidth      int
		maxWidth      int
		minHeight     int
		maxHeight     int
	}{
		// Level 3.1: maxFS=3600 MBs, maxMBPS=108000 at 30fps -> effectiveFS=3600
		{"CB level 3.1 at 30fps", "42e01f", 30, true, 960, 1280, 528, 720},
		// Level 2.2: maxFS=1620, maxMBPS=20250 at 30fps -> effectiveFS=min(1620,675)=675
		{"CB level 2.2 at 30fps", "428016", 30, true, 400, 640, 240, 400},
		// Level 4.0: maxFS=8192
		{"CB level 4.0 at 30fps", "42e028", 30, true, 1280, 2048, 720, 1200},
		// Invalid plid
		{"invalid plid", "zzzzzz", 30, false, 0, 0, 0, 0},
		// Default fps (0 -> 30)
		{"default fps", "42e01f", 0, true, 960, 1280, 528, 720},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h, ok := maxResolutionForLevel(tt.plid, tt.fps)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if w < tt.minWidth || w > tt.maxWidth {
				t.Errorf("width = %d, want [%d, %d]", w, tt.minWidth, tt.maxWidth)
			}
			if h < tt.minHeight || h > tt.maxHeight {
				t.Errorf("height = %d, want [%d, %d]", h, tt.minHeight, tt.maxHeight)
			}
		})
	}
}

// --- Integration tests (pipeline-based) ---
//
// Both tests use a 720p source with videoscale + scaleCapsFilter wired to the
// max-resolution signal. The dot files show the negotiated resolution at each
// point in the pipeline:
//   - Compatible (level 3.1): 720p passes through videoscale untouched.
//   - Low level  (level 2.2): videoscale downscales 720p to fit the level.

// buildTestPipeline creates a pipeline for integration tests:
//
//	videotestsrc → rawCaps(720p30) → videoscale → scaleCaps → videoconvert
//	  → x264enc → rtph264pay → rtph264capsintersect → rtpCaps(plid) → fakesink
//
// Returns all elements needed for assertions and the pipeline itself.
func buildTestPipeline(t *testing.T, name, downstreamPLID string) (
	pipeline *gst.Pipeline,
	intersect *gst.Element,
	scaleCapsFilter *gst.Element,
	sink *gst.Element,
) {
	t.Helper()

	var err error
	pipeline, err = gst.NewPipeline(name)
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
	rawCapsFilter.SetProperty("caps", gst.NewCapsFromString(
		"video/x-raw, width=1280, height=720, framerate=30/1"))

	videoscale, err := gst.NewElement("videoscale")
	if err != nil {
		t.Fatal("failed to create videoscale:", err)
	}

	scaleCapsFilter, err = gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create scale capsfilter:", err)
	}

	videoconvert, err := gst.NewElement("videoconvert")
	if err != nil {
		t.Fatal("failed to create videoconvert:", err)
	}

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

	intersect, err = gst.NewElement("rtph264capsintersect")
	if err != nil {
		t.Fatal("failed to create rtph264capsintersect:", err)
	}

	rtpCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create rtp capsfilter:", err)
	}
	rtpCapsFilter.SetProperty("caps", gst.NewCapsFromString(
		"application/x-rtp, media=(string)video, encoding-name=(string)H264, "+
			"profile-level-id=(string)"+downstreamPLID))

	sink, err = gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(videoSrc, rawCapsFilter, videoscale, scaleCapsFilter,
		videoconvert, encoder, payloader, intersect, rtpCapsFilter, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}

	if err := gst.ElementLinkMany(videoSrc, rawCapsFilter, videoscale, scaleCapsFilter,
		videoconvert, encoder, payloader, intersect, rtpCapsFilter, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	return
}

func runTestPipeline(t *testing.T, pipeline *gst.Pipeline, sink *gst.Element, dotFile string) int32 {
	t.Helper()

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
	if err := os.WriteFile(dotFile, []byte(dotData), 0644); err != nil {
		t.Logf("failed to write DOT file: %v", err)
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	return bufferCount.Load()
}

// runPipelineTest is the core test logic shared by all pipeline subtests.
// It wires the max-resolution signal to the scaleCapsFilter, runs the pipeline,
// writes a dot file, and asserts that the signal fired and buffers flowed.
// If the downstream level's max resolution is below 720p, it also asserts that
// the signal reported a downscaled resolution.
func runPipelineTest(t *testing.T, name, plid string, expectDownscale bool) {
	t.Helper()
	defer testutils.AssertNoLeaks(t)

	pipeline, intersect, scaleCapsFilter, sink := buildTestPipeline(t, name, plid)

	var signalWidth, signalHeight atomic.Int32
	intersect.Connect("max-resolution", func(_ *gst.Element, w, h int) {
		signalWidth.Store(int32(w))
		signalHeight.Store(int32(h))
		scaleCapsFilter.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("video/x-raw, width=[1,%d], height=[1,%d], pixel-aspect-ratio=1/1", w, h)))
	})

	dotFile := fmt.Sprintf("rtph264capsintersect_%s_test.dot", name)
	count := runTestPipeline(t, pipeline, sink, dotFile)
	if count <= 0 {
		t.Fatal("no buffers received")
	}
	t.Logf("received %d buffers", count)

	w, h := signalWidth.Load(), signalHeight.Load()
	t.Logf("max-resolution signal: %dx%d", w, h)
	if w <= 0 || h <= 0 {
		t.Fatal("max-resolution signal was not emitted")
	}

	if expectDownscale && (w >= 1280 || h >= 720) {
		t.Errorf("expected downscale from 720p, got %dx%d", w, h)
	}
	if !expectDownscale && (w < 1280 || h < 720) {
		t.Errorf("expected 720p passthrough, got %dx%d", w, h)
	}
}

// TestPipeline_PreservesPTS verifies that rtph264capsintersect forwards
// buffers through GstBaseTransform passthrough mode without altering PTS.
// We tap the element's own sink and src pads and assert that the sequence
// of PTS values observed on the way in matches the sequence on the way out.
func TestPipeline_PreservesPTS(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, intersect, scaleCapsFilter, sink := buildTestPipeline(t, "preserves_pts", "42e01f")

	intersect.Connect("max-resolution", func(_ *gst.Element, w, h int) {
		scaleCapsFilter.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("video/x-raw, width=[1,%d], height=[1,%d], pixel-aspect-ratio=1/1", w, h)))
	})

	var inPTS, outPTS []gst.ClockTime
	var mu sync.Mutex

	// Use BUFFER-only mask on both sides. GStreamer's push_list falls back to
	// per-buffer pushes when no BUFFER_LIST probe is registered, so a pure
	// BUFFER probe sees every buffer exactly once regardless of whether the
	// upstream element pushes lists or individual buffers.
	collect := func(slot *[]gst.ClockTime) func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		return func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if buf := info.GetBuffer(); buf != nil {
				mu.Lock()
				*slot = append(*slot, buf.PresentationTimestamp())
				mu.Unlock()
			}
			return gst.PadProbeOK
		}
	}

	sinkPad := intersect.GetStaticPad("sink")
	srcPad := intersect.GetStaticPad("src")
	sinkPad.AddProbe(gst.PadProbeTypeBuffer, collect(&inPTS))
	srcPad.AddProbe(gst.PadProbeTypeBuffer, collect(&outPTS))

	count := runTestPipeline(t, pipeline, sink, "rtph264capsintersect_preserves_pts_test.dot")
	if count <= 0 {
		t.Fatal("no buffers received at fakesink")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(inPTS) == 0 {
		t.Fatal("no buffers observed on intersect sink pad")
	}
	if len(inPTS) != len(outPTS) {
		t.Fatalf("buffer count mismatch: in=%d out=%d", len(inPTS), len(outPTS))
	}
	for i := range inPTS {
		if inPTS[i] != outPTS[i] {
			t.Errorf("PTS[%d] mismatch: in=%v out=%v", i, inPTS[i], outPTS[i])
		}
	}
	t.Logf("verified %d buffers with identical PTS across rtph264capsintersect", len(inPTS))
}

func TestPipeline_ProfileLevelID(t *testing.T) {
	// Source is 1280x720@30fps = 3600 MBs at 108000 MB/s.
	// Levels with maxFS >= 3600 AND maxMBPS >= 108000 can handle 720p (level 3.1+).
	tests := []struct {
		name            string
		plid            string
		expectDownscale bool
	}{
		// --- Constrained Baseline (0x42, csf1 set → profileIOP 0xE0) ---
		{"cb_level_1.2", "42e00c", true},
		{"cb_level_1.3", "42e00d", true},
		{"cb_level_2.0", "42e014", true},
		{"cb_level_2.1", "42e015", true},
		{"cb_level_2.2", "42e016", true},
		{"cb_level_3.0", "42e01e", true},
		{"cb_level_3.1", "42e01f", false},
		{"cb_level_3.2", "42e020", false},
		{"cb_level_4.0", "42e028", false},
		{"cb_level_4.1", "42e029", false},
		{"cb_level_4.2", "42e02a", false},
		{"cb_level_5.0", "42e032", false},

		// --- Baseline (0x42, csf1 clear → profileIOP 0x00) ---
		{"baseline_level_1.2", "42000c", true},
		{"baseline_level_2.0", "420014", true},
		{"baseline_level_2.2", "420016", true},
		{"baseline_level_3.0", "42001e", true},
		{"baseline_level_3.1", "42001f", false},
		{"baseline_level_4.0", "420028", false},
		{"baseline_level_5.0", "420032", false},

		// --- Main (0x4D, profileIOP 0x00) ---
		{"main_level_1.2", "4d000c", true},
		{"main_level_2.0", "4d0014", true},
		{"main_level_2.2", "4d0016", true},
		{"main_level_3.0", "4d001e", true},
		{"main_level_3.1", "4d001f", false},
		{"main_level_4.0", "4d0028", false},
		{"main_level_5.0", "4d0032", false},

		// --- High (0x64, profileIOP 0x00) ---
		{"high_level_1.2", "64000c", true},
		{"high_level_2.0", "640014", true},
		{"high_level_2.2", "640016", true},
		{"high_level_3.0", "64001e", true},
		{"high_level_3.1", "64001f", false},
		{"high_level_4.0", "640028", false},
		{"high_level_5.0", "640032", false},

		// --- Constrained High (0x64, csf4+csf5 set → profileIOP 0x0C) ---
		{"constrained_high_level_3.1", "640c1f", false},
		{"constrained_high_level_4.0", "640c28", false},
		{"constrained_high_level_5.0", "640c32", false},

		// --- Constrained Baseline via Main IDC (0x4D, csf0 set → profileIOP 0x80) ---
		{"cb_via_main_level_2.2", "4d8016", true},
		{"cb_via_main_level_3.1", "4d801f", false},
		{"cb_via_main_level_4.0", "4d8028", false},

		// --- Mixed case (uppercase plid from SDP) ---
		{"cb_level_3.1_uppercase", "42E01F", false},
		{"high_level_4.0_uppercase", "640028", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runPipelineTest(t, tt.name, tt.plid, tt.expectDownscale)
		})
	}
}
