package livekitcompositor_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",livekit_compositor:5", true)
	gst.Init(nil)
	livekitcompositor.Register()
	os.Exit(m.Run())
}

// --- Helpers ---

func testDebugDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("testdata", t.Name())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create debug dir %s: %v", dir, err)
	}
	return dir
}

func dumpDot(t *testing.T, pipeline *gst.Pipeline, label string) {
	t.Helper()
	dir := testDebugDir(t)
	data := pipeline.Bin.DebugBinToDotData(gst.DebugGraphShowVerbose)
	path := filepath.Join(dir, label+".dot")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Logf("WARNING: failed to write dot file %s: %v", path, err)
	}
}

func newTestPipeline(t *testing.T, name string) (*gst.Pipeline, *gst.Element) {
	t.Helper()
	pipeline, err := gst.NewPipeline(name)
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}
	compositor, err := gst.NewElement("livekit_compositor")
	if err != nil {
		t.Fatal("failed to create livekit_compositor:", err)
	}
	if err := pipeline.Add(compositor); err != nil {
		t.Fatal("failed to add livekit_compositor to pipeline:", err)
	}
	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
	return pipeline, compositor
}

func addBufferProbe(t *testing.T, element *gst.Element) *atomic.Int32 {
	t.Helper()
	var count atomic.Int32
	sinkPad := element.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad for buffer probe")
	}
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		count.Add(1)
		return gst.PadProbeOK
	})
	return &count
}

// videotestsrcColors maps color indices to distinct foreground/background ARGB pairs.
var videotestsrcColors = [][2]uint32{
	{0xFFFFFFFF, 0xFF000000}, // white ball on black
	{0xFFFF0000, 0xFF0000FF}, // red ball on blue
	{0xFF00FF00, 0xFFFF00FF}, // green ball on magenta
	{0xFFFFFF00, 0xFF800080}, // yellow ball on purple
	{0xFF00FFFF, 0xFF804000}, // cyan ball on brown
}

// videoFileSink holds the video file output chain elements.
type videoFileSink struct {
	head     *gst.Element   // videoconvert — link compositor src to head's sink pad
	elements []*gst.Element // all elements in the chain (for SyncStateWithParent)
	count    *atomic.Int32  // buffer counter on head's sink pad
}

// newVideoFileSink creates a videoconvert → x264enc → matroskamux → filesink chain,
// adds all elements to the pipeline, and links them.
func newVideoFileSink(t *testing.T, pipeline *gst.Pipeline, filename string) videoFileSink {
	t.Helper()
	dir := testDebugDir(t)

	convert, err := gst.NewElement("videoconvert")
	if err != nil {
		t.Fatal("failed to create videoconvert:", err)
	}
	encoder, err := gst.NewElementWithProperties("x264enc", map[string]any{
		"tune":         0x4, // zerolatency
		"speed-preset": 1,   // ultrafast
		"key-int-max":  30,
	})
	if err != nil {
		t.Fatal("failed to create x264enc:", err)
	}
	mux, err := gst.NewElement("matroskamux")
	if err != nil {
		t.Fatal("failed to create matroskamux:", err)
	}
	sink, err := gst.NewElementWithProperties("filesink", map[string]any{
		"location": filepath.Join(dir, filename),
	})
	if err != nil {
		t.Fatal("failed to create filesink:", err)
	}

	elements := []*gst.Element{convert, encoder, mux, sink}
	if err := pipeline.AddMany(elements...); err != nil {
		t.Fatal("failed to add video file sink elements:", err)
	}
	if err := gst.ElementLinkMany(elements...); err != nil {
		t.Fatal("failed to link video file sink chain:", err)
	}

	count := addBufferProbe(t, convert)
	return videoFileSink{head: convert, elements: elements, count: count}
}

// syncVideoFileSinkState syncs all elements in the video file sink chain with parent.
func syncVideoFileSinkState(t *testing.T, elements []*gst.Element) {
	t.Helper()
	for _, e := range elements {
		if !e.SyncStateWithParent() {
			t.Logf("warning: failed to sync %s state with parent", e.GetName())
		}
	}
}

func emitActiveSpeakers(t *testing.T, compositor *gst.Element, sids []string, levels []float32) {
	t.Helper()
	info := livekittracks.ActiveSpeakerChangeInfo{
		ParticipantsSID:   sids,
		AudioLevels:       levels,
		ParticipantTracks: make(map[string][]string),
	}
	structure := info.Structure()
	runtime.SetFinalizer(structure, nil)

	done := make(chan struct{})
	go func() {
		compositor.Emit("active-speakers-changed", structure)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("emitActiveSpeakers deadlocked — Emit did not return within 5s")
	}
}

func injectTrackSourceInfo(pad *gst.Pad, info livekittracks.TrackSourceInfo) {
	pad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(p *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		structure := info.Structure()
		runtime.SetFinalizer(structure, nil)
		event := gst.NewCustomEvent(gst.EventTypeCustomDownstreamSticky, structure)
		p.PushEvent(event)
		return gst.PadProbeRemove
	})
}

func waitForSrcPad(t *testing.T, compositor *gst.Element, padName string, timeout time.Duration) *gst.Pad {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pad := compositor.GetStaticPad(padName)
		if pad != nil {
			return pad
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for sometimes pad %s", padName)
	return nil
}

type participantSource struct {
	audioSrcPad  *gst.Pad // capsfilter src pad (audio)
	videoSrcPad  *gst.Pad // capsfilter src pad (video)
	audioSinkPad *gst.Pad // compositor ghost sink pad (microphone)
	videoSinkPad *gst.Pad // compositor ghost sink pad (camera)
}

func addParticipant(t *testing.T, pipeline *gst.Pipeline, compositor *gst.Element, sid string, audioSSRC, videoSSRC uint, colorIndex int) participantSource {
	t.Helper()

	// Audio chain: audiotestsrc -> capsfilter
	audioSrc, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc for", sid, ":", err)
	}
	audioCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create audio capsfilter for", sid, ":", err)
	}
	audioCaps.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=48000,channels=1,format=S16LE"))

	// Video chain: videotestsrc (ball pattern) -> capsfilter
	colors := videotestsrcColors[colorIndex%len(videotestsrcColors)]
	videoSrc, err := gst.NewElementWithProperties("videotestsrc", map[string]any{
		"is-live":          true,
		"pattern":          18, // ball
		"foreground-color": colors[0],
		"background-color": colors[1],
	})
	if err != nil {
		t.Fatal("failed to create videotestsrc for", sid, ":", err)
	}
	videoCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create video capsfilter for", sid, ":", err)
	}
	videoCaps.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=240,framerate=15/1"))

	if err := pipeline.AddMany(audioSrc, audioCaps, videoSrc, videoCaps); err != nil {
		t.Fatal("failed to add source elements for", sid, ":", err)
	}
	if err := audioSrc.Link(audioCaps); err != nil {
		t.Fatal("failed to link audio chain for", sid, ":", err)
	}
	if err := videoSrc.Link(videoCaps); err != nil {
		t.Fatal("failed to link video chain for", sid, ":", err)
	}

	// Request sink pads on compositor
	audioSinkPad := compositor.GetRequestPad(fmt.Sprintf("sink_2_%d_111", audioSSRC))
	if audioSinkPad == nil {
		t.Fatal("failed to request audio sink pad for", sid)
	}
	videoSinkPad := compositor.GetRequestPad(fmt.Sprintf("sink_1_%d_96", videoSSRC))
	if videoSinkPad == nil {
		t.Fatal("failed to request video sink pad for", sid)
	}

	// Link capsfilter src -> compositor sink pads
	audioCapsSrc := audioCaps.GetStaticPad("src")
	if ret := audioCapsSrc.Link(audioSinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link audio capsfilter to compositor for", sid, ":", ret)
	}
	videoCapsSrc := videoCaps.GetStaticPad("src")
	if ret := videoCapsSrc.Link(videoSinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link video capsfilter to compositor for", sid, ":", ret)
	}

	// Inject TrackSourceInfo events
	injectTrackSourceInfo(audioCapsSrc, livekittracks.TrackSourceInfo{
		ParticipantSID:  sid,
		ParticipantName: sid,
		TrackSID:        sid + "-audio",
		Source:          livekit.TrackSource_MICROPHONE,
		Kind:            "audio",
		MimeType:        "audio/x-raw",
		SSRC:            audioSSRC,
		PT:              111,
	})
	injectTrackSourceInfo(videoCapsSrc, livekittracks.TrackSourceInfo{
		ParticipantSID:  sid,
		ParticipantName: sid,
		TrackSID:        sid + "-video",
		Source:          livekit.TrackSource_CAMERA,
		Kind:            "video",
		MimeType:        "video/x-raw",
		SSRC:            videoSSRC,
		PT:              96,
	})

	return participantSource{
		audioSrcPad:  audioCapsSrc,
		videoSrcPad:  videoCapsSrc,
		audioSinkPad: audioSinkPad,
		videoSinkPad: videoSinkPad,
	}
}

func waitForSrcPadsAndLink(t *testing.T, pipeline *gst.Pipeline, compositor *gst.Element) (audioCount, videoCount *atomic.Int32) {
	t.Helper()

	audioSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create audio fakesink:", err)
	}
	if err := pipeline.Add(audioSink); err != nil {
		t.Fatal("failed to add audio fakesink:", err)
	}

	vfs := newVideoFileSink(t, pipeline, "video.mkv")

	audioCount = addBufferProbe(t, audioSink)
	videoCount = vfs.count

	audioName := fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE)
	videoName := fmt.Sprintf("src_%d", livekit.TrackSource_CAMERA)

	var mu sync.Mutex
	linked := make(map[string]bool)

	tryLink := func(pad *gst.Pad) {
		name := pad.GetName()
		if linked[name] {
			return
		}
		switch name {
		case audioName:
			if !audioSink.SyncStateWithParent() {
				t.Logf("warning: failed to sync audio fakesink state")
			}
			if ret := pad.Link(audioSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link audio src pad: %v", ret)
				return
			}
			linked[name] = true
		case videoName:
			syncVideoFileSinkState(t, vfs.elements)
			if ret := pad.Link(vfs.head.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link video src pad: %v", ret)
				return
			}
			linked[name] = true
		}
	}

	compositor.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		mu.Lock()
		defer mu.Unlock()
		tryLink(pad)
	})

	// Link any src pads that already exist (created during GetRequestPad)
	mu.Lock()
	if pad := compositor.GetStaticPad(audioName); pad != nil {
		tryLink(pad)
	}
	if pad := compositor.GetStaticPad(videoName); pad != nil {
		tryLink(pad)
	}
	mu.Unlock()

	return audioCount, videoCount
}

// --- Group 1: Basics ---

func TestCompositor_StateChanges(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, _ := newTestPipeline(t, "test-state-changes")
	pipeline.GetBus().SetFlushing(true)

	dumpDot(t, pipeline, "01_ready")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}
	dumpDot(t, pipeline, "02_playing")

	if err := pipeline.SetState(gst.StatePaused); err != nil {
		t.Fatal("failed to set pipeline to PAUSED:", err)
	}
	dumpDot(t, pipeline, "03_paused")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	pipeline = nil
}

func TestCompositor_SinkPadRequest(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-sink-pad-request")

	// Microphone pad (session=2)
	micPad := compositor.GetRequestPad("sink_2_1234_111")
	if micPad == nil {
		t.Fatal("GetRequestPad returned nil for microphone pad sink_2_1234_111")
	}

	// Camera pad (session=1)
	camPad := compositor.GetRequestPad("sink_1_5678_96")
	if camPad == nil {
		t.Fatal("GetRequestPad returned nil for camera pad sink_1_5678_96")
	}

	dumpDot(t, pipeline, "after_request")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestCompositor_SinkPadRequest_InvalidNames(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-sink-pad-invalid")

	if pad := compositor.GetRequestPad(""); pad != nil {
		t.Fatal("expected nil pad for empty name")
	}
	if pad := compositor.GetRequestPad("bad_name"); pad != nil {
		t.Fatal("expected nil pad for malformed name")
	}
	if pad := compositor.GetRequestPad("sink_99_1_1"); pad != nil {
		t.Fatal("expected nil pad for unknown session 99")
	}

	dumpDot(t, pipeline, "after_invalid_requests")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestCompositor_SourcePadCreation_Microphone(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-src-pad-mic")

	audioSrc, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	audioCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	audioCaps.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=48000,channels=1,format=S16LE"))

	if err := pipeline.AddMany(audioSrc, audioCaps); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := audioSrc.Link(audioCaps); err != nil {
		t.Fatal("failed to link audio chain:", err)
	}

	sinkPad := compositor.GetRequestPad("sink_2_1234_111")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	capsSrc := audioCaps.GetStaticPad("src")
	if ret := capsSrc.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link capsfilter to compositor:", ret)
	}

	injectTrackSourceInfo(capsSrc, livekittracks.TrackSourceInfo{
		ParticipantSID:  "p1",
		ParticipantName: "P1",
		TrackSID:        "t1",
		Source:          livekit.TrackSource_MICROPHONE,
		Kind:            "audio",
		MimeType:        "audio/x-raw",
		SSRC:            1234,
		PT:              111,
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	srcPad := waitForSrcPad(t, compositor, fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE), 5*time.Second)
	if srcPad == nil {
		t.Fatal("microphone src pad did not appear")
	}

	dumpDot(t, pipeline, "src_pad_appeared")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestCompositor_SourcePadCreation_Camera(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-src-pad-cam")

	videoSrc, err := gst.NewElementWithProperties("videotestsrc", map[string]any{
		"is-live":          true,
		"pattern":          18, // ball
		"foreground-color": videotestsrcColors[0][0],
		"background-color": videotestsrcColors[0][1],
	})
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	videoCaps.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=240,framerate=15/1"))

	if err := pipeline.AddMany(videoSrc, videoCaps); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := videoSrc.Link(videoCaps); err != nil {
		t.Fatal("failed to link video chain:", err)
	}

	sinkPad := compositor.GetRequestPad("sink_1_5678_96")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	capsSrc := videoCaps.GetStaticPad("src")
	if ret := capsSrc.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link capsfilter to compositor:", ret)
	}

	injectTrackSourceInfo(capsSrc, livekittracks.TrackSourceInfo{
		ParticipantSID:  "p1",
		ParticipantName: "P1",
		TrackSID:        "t1",
		Source:          livekit.TrackSource_CAMERA,
		Kind:            "video",
		MimeType:        "video/x-raw",
		SSRC:            5678,
		PT:              96,
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	srcPad := waitForSrcPad(t, compositor, fmt.Sprintf("src_%d", livekit.TrackSource_CAMERA), 5*time.Second)
	if srcPad == nil {
		t.Fatal("camera src pad did not appear")
	}

	dumpDot(t, pipeline, "src_pad_appeared")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

// --- Group 2: Audio Flow ---

func TestCompositor_AudioFlow_SingleParticipant(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-audio-flow")

	audioSrc, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=48000,channels=1,format=S16LE"))

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}

	if err := pipeline.AddMany(audioSrc, capsFilter, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := audioSrc.Link(capsFilter); err != nil {
		t.Fatal("failed to link audio chain:", err)
	}

	bufferCount := addBufferProbe(t, sink)

	sinkPad := compositor.GetRequestPad("sink_2_1234_111")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	// Link the src pad that was created during GetRequestPad (initMicrophone)
	srcPadName := fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE)
	srcPad := compositor.GetStaticPad(srcPadName)
	if srcPad == nil {
		t.Fatalf("src pad %s not found after requesting sink pad", srcPadName)
	}
	if ret := srcPad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatal("failed to link compositor src to fakesink:", ret)
	}

	capsSrc := capsFilter.GetStaticPad("src")
	if ret := capsSrc.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link capsfilter to compositor:", ret)
	}

	injectTrackSourceInfo(capsSrc, livekittracks.TrackSourceInfo{
		ParticipantSID:  "participant1",
		ParticipantName: "Test User",
		TrackSID:        "track1",
		Source:          livekit.TrackSource_MICROPHONE,
		Kind:            "audio",
		MimeType:        "audio/x-raw",
		SSRC:            1234,
		PT:              111,
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(2 * time.Second)

	emitActiveSpeakers(t, compositor, []string{"participant1"}, []float32{0.8})

	time.Sleep(3 * time.Second)

	dumpDot(t, pipeline, "audio_flow")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d audio buffers", count)
	if count <= 0 {
		t.Fatal("no audio buffers received")
	}
}

func TestCompositor_AudioFlow_NoActiveSpeakersNeeded(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-audio-no-speakers")

	audioSrc, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=48000,channels=1,format=S16LE"))

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}

	if err := pipeline.AddMany(audioSrc, capsFilter, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := audioSrc.Link(capsFilter); err != nil {
		t.Fatal("failed to link audio chain:", err)
	}

	bufferCount := addBufferProbe(t, sink)

	sinkPad := compositor.GetRequestPad("sink_2_1234_111")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	// Link the src pad created during initMicrophone
	srcPadName := fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE)
	srcPad := compositor.GetStaticPad(srcPadName)
	if srcPad == nil {
		t.Fatalf("src pad %s not found", srcPadName)
	}
	if ret := srcPad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatal("failed to link compositor src to fakesink:", ret)
	}

	capsSrc := capsFilter.GetStaticPad("src")
	if ret := capsSrc.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link capsfilter to compositor:", ret)
	}

	injectTrackSourceInfo(capsSrc, livekittracks.TrackSourceInfo{
		ParticipantSID:  "participant1",
		ParticipantName: "Test User",
		TrackSID:        "track1",
		Source:          livekit.TrackSource_MICROPHONE,
		Kind:            "audio",
		MimeType:        "audio/x-raw",
		SSRC:            1234,
		PT:              111,
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// Deliberately do NOT emit active speakers — audio should flow anyway
	time.Sleep(3 * time.Second)

	dumpDot(t, pipeline, "audio_no_speakers")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d audio buffers without active-speakers signal", count)
	if count <= 0 {
		t.Fatal("no audio buffers received — audiomixer should pass through without active-speakers")
	}
}

// --- Group 3: Video Flow ---

func TestCompositor_VideoFlow_SingleParticipant(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-video-flow")

	videoSrc, err := gst.NewElementWithProperties("videotestsrc", map[string]any{
		"is-live":          true,
		"pattern":          18, // ball
		"foreground-color": videotestsrcColors[0][0],
		"background-color": videotestsrcColors[0][1],
	})
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=240,framerate=15/1"))

	if err := pipeline.AddMany(videoSrc, capsFilter); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := videoSrc.Link(capsFilter); err != nil {
		t.Fatal("failed to link video chain:", err)
	}

	vfs := newVideoFileSink(t, pipeline, "video.mkv")

	sinkPad := compositor.GetRequestPad("sink_1_5678_96")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	// Link the src pad created during initCamera
	srcPadName := fmt.Sprintf("src_%d", livekit.TrackSource_CAMERA)
	srcPad := compositor.GetStaticPad(srcPadName)
	if srcPad == nil {
		t.Fatalf("src pad %s not found after requesting sink pad", srcPadName)
	}
	if ret := srcPad.Link(vfs.head.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatal("failed to link compositor src to video file sink:", ret)
	}

	capsSrc := capsFilter.GetStaticPad("src")
	if ret := capsSrc.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link capsfilter to compositor:", ret)
	}

	injectTrackSourceInfo(capsSrc, livekittracks.TrackSourceInfo{
		ParticipantSID:  "participant1",
		ParticipantName: "Test User",
		TrackSID:        "track1",
		Source:          livekit.TrackSource_CAMERA,
		Kind:            "video",
		MimeType:        "video/x-raw",
		SSRC:            5678,
		PT:              96,
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// Wait for TrackSourceInfo to propagate
	time.Sleep(2 * time.Second)

	// Active speakers required to trigger camera layout
	emitActiveSpeakers(t, compositor, []string{"participant1"}, []float32{0.8})

	time.Sleep(3 * time.Second)

	dumpDot(t, pipeline, "video_flow")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := vfs.count.Load()
	t.Logf("received %d video buffers → testdata/%s/video.mkv", count, t.Name())
	if count <= 0 {
		t.Fatal("no video buffers received")
	}
}

func TestCompositor_VideoFlow_BackgroundAlwaysProduces(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-video-background")

	// Request a camera pad to trigger camera subsystem init (creates internal videotestsrc)
	videoSrc, err := gst.NewElementWithProperties("videotestsrc", map[string]any{
		"is-live":          true,
		"pattern":          18, // ball
		"foreground-color": videotestsrcColors[0][0],
		"background-color": videotestsrcColors[0][1],
	})
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	videoCaps.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=240,framerate=15/1"))

	if err := pipeline.AddMany(videoSrc, videoCaps); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := videoSrc.Link(videoCaps); err != nil {
		t.Fatal("failed to link video chain:", err)
	}

	vfs := newVideoFileSink(t, pipeline, "video.mkv")

	sinkPad := compositor.GetRequestPad("sink_1_5678_96")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	// Link the src pad created during initCamera
	srcPadName := fmt.Sprintf("src_%d", livekit.TrackSource_CAMERA)
	srcPad := compositor.GetStaticPad(srcPadName)
	if srcPad == nil {
		t.Fatalf("src pad %s not found", srcPadName)
	}
	if ret := srcPad.Link(vfs.head.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatal("failed to link compositor src to video file sink:", ret)
	}

	capsSrc := videoCaps.GetStaticPad("src")
	if ret := capsSrc.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link capsfilter to compositor:", ret)
	}

	injectTrackSourceInfo(capsSrc, livekittracks.TrackSourceInfo{
		ParticipantSID:  "p1",
		ParticipantName: "P1",
		TrackSID:        "t1",
		Source:          livekit.TrackSource_CAMERA,
		Kind:            "video",
		MimeType:        "video/x-raw",
		SSRC:            5678,
		PT:              96,
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// Do NOT emit active speakers — black background should still produce frames via sink_0
	time.Sleep(3 * time.Second)

	dumpDot(t, pipeline, "video_background")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := vfs.count.Load()
	t.Logf("received %d video buffers from background (no active speakers) → testdata/%s/video.mkv", count, t.Name())
	if count <= 0 {
		t.Fatal("no video buffers received — internal videotestsrc(black) should always produce frames")
	}
}

// --- Group 4: Active Speaker Layout ---

func TestCompositor_ThreeParticipants_ActiveSpeakerSwitch(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-3p-speaker-switch")

	// 3 participants, but only 2 active speaker slots at a time
	// This forces real patchbay switches as participants rotate in/out.
	addParticipant(t, pipeline, compositor, "alice", 1001, 2001, 0) // white on black
	addParticipant(t, pipeline, compositor, "bob", 1002, 2002, 1)   // red on blue
	addParticipant(t, pipeline, compositor, "carol", 1003, 2003, 2) // green on magenta

	audioCount, videoCount := waitForSrcPadsAndLink(t, pipeline, compositor)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "01_playing")

	// Wait for track registration
	time.Sleep(2 * time.Second)

	// 1. Alice and Bob speak (carol is out)
	t.Log("speakers: alice, bob")
	emitActiveSpeakers(t, compositor, []string{"alice", "bob"}, []float32{0.9, 0.5})
	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "02_alice_bob")

	countAfterAliceBob := audioCount.Load()
	t.Logf("after alice+bob: audio=%d video=%d", countAfterAliceBob, videoCount.Load())
	if countAfterAliceBob <= 0 {
		t.Fatal("no audio buffers after alice+bob speak")
	}

	// 2. Carol replaces Bob (alice keeps her slot, carol takes bob's)
	t.Log("speakers: alice, carol — bob swapped out")
	emitActiveSpeakers(t, compositor, []string{"alice", "carol"}, []float32{0.8, 0.7})
	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "03_alice_carol")

	countAfterAliceCarol := audioCount.Load()
	t.Logf("after alice+carol: audio=%d video=%d", countAfterAliceCarol, videoCount.Load())
	if countAfterAliceCarol <= countAfterAliceBob {
		t.Fatal("audio stalled after carol replaced bob")
	}

	// 3. Bob replaces Alice (carol keeps her slot, bob takes alice's)
	t.Log("speakers: bob, carol — alice swapped out")
	emitActiveSpeakers(t, compositor, []string{"bob", "carol"}, []float32{0.9, 0.3})
	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "04_bob_carol")

	countAfterBobCarol := audioCount.Load()
	t.Logf("after bob+carol: audio=%d video=%d", countAfterBobCarol, videoCount.Load())
	if countAfterBobCarol <= countAfterAliceCarol {
		t.Fatal("audio stalled after bob replaced alice")
	}

	// 4. Alice comes back, replaces carol
	t.Log("speakers: bob, alice — carol swapped out")
	emitActiveSpeakers(t, compositor, []string{"bob", "alice"}, []float32{0.6, 0.9})
	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "05_bob_alice")

	countAfterBobAlice := audioCount.Load()
	t.Logf("after bob+alice: audio=%d video=%d", countAfterBobAlice, videoCount.Load())
	if countAfterBobAlice <= countAfterBobCarol {
		t.Fatal("audio stalled after alice replaced carol")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	finalVideo := videoCount.Load()
	t.Logf("final: audio=%d video=%d", audioCount.Load(), finalVideo)
	if finalVideo <= 0 {
		t.Fatal("no video buffers received")
	}
}

func TestCompositor_ParticipantDrop(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-participant-drop")

	addParticipant(t, pipeline, compositor, "alice", 5001, 6001, 0)
	addParticipant(t, pipeline, compositor, "bob", 5002, 6002, 1)
	addParticipant(t, pipeline, compositor, "carol", 5003, 6003, 2)

	audioCount, videoCount := waitForSrcPadsAndLink(t, pipeline, compositor)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(2 * time.Second)

	// All three active
	t.Log("all three active")
	emitActiveSpeakers(t, compositor, []string{"alice", "bob", "carol"}, []float32{0.8, 0.6, 0.4})
	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "01_all_active")

	countAllActive := audioCount.Load()
	if countAllActive <= 0 {
		t.Fatal("no audio buffers with all active")
	}

	// Bob drops — layout shrinks from 3 to 2, carol should move up
	t.Log("bob drops, alice+carol remain")
	emitActiveSpeakers(t, compositor, []string{"alice", "carol"}, []float32{0.9, 0.5})
	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "02_bob_dropped")

	countAfterDrop := audioCount.Load()
	if countAfterDrop <= countAllActive {
		t.Fatal("audio stalled after bob dropped from active speakers")
	}

	// Bob returns, replacing carol
	t.Log("bob comes back, carol drops")
	emitActiveSpeakers(t, compositor, []string{"alice", "bob"}, []float32{0.5, 0.9})
	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "03_bob_returned")

	countAfterReturn := audioCount.Load()
	if countAfterReturn <= countAfterDrop {
		t.Fatal("audio stalled after bob returned to active speakers")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	t.Logf("final: audio=%d video=%d", audioCount.Load(), videoCount.Load())
	if videoCount.Load() <= 0 {
		t.Fatal("no video buffers received")
	}
}

func TestCompositor_RapidSpeakerSwitch(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-rapid-switch")

	addParticipant(t, pipeline, compositor, "alice", 3001, 4001, 0)
	addParticipant(t, pipeline, compositor, "bob", 3002, 4002, 1)
	addParticipant(t, pipeline, compositor, "carol", 3003, 4003, 2)

	audioCount, videoCount := waitForSrcPadsAndLink(t, pipeline, compositor)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(2 * time.Second)

	emitActiveSpeakers(t, compositor, []string{"alice", "bob"}, []float32{0.9, 0.1})
	time.Sleep(500 * time.Millisecond)

	dumpDot(t, pipeline, "01_before_rapid")

	// 12 rapid rotations at 200ms — cycles all 3 pairs so each participant
	// gets swapped in and out, forcing real patchbay switches.
	pairs := [][2]string{
		{"alice", "bob"},
		{"bob", "carol"},
		{"carol", "alice"},
	}
	for i := range 12 {
		p := pairs[i%len(pairs)]
		emitActiveSpeakers(t, compositor, []string{p[0], p[1]}, []float32{0.9, 0.5})
		time.Sleep(200 * time.Millisecond)
	}

	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "02_after_rapid")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	finalAudio := audioCount.Load()
	finalVideo := videoCount.Load()
	t.Logf("after rapid switching: audio=%d video=%d", finalAudio, finalVideo)
	if finalAudio <= 0 {
		t.Fatal("no audio buffers after rapid switching")
	}
	if finalVideo <= 0 {
		t.Fatal("no video buffers after rapid switching")
	}
}

func TestCompositor_LayoutResize(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-layout-resize")

	addParticipant(t, pipeline, compositor, "alice", 7001, 8001, 0) // white on black
	addParticipant(t, pipeline, compositor, "bob", 7002, 8002, 1)   // red on blue
	addParticipant(t, pipeline, compositor, "carol", 7003, 8003, 2) // green on magenta
	addParticipant(t, pipeline, compositor, "dave", 7004, 8004, 3)  // yellow on purple

	audioCount, videoCount := waitForSrcPadsAndLink(t, pipeline, compositor)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(2 * time.Second)

	// 1 speaker → 1×1 grid (full frame)
	t.Log("layout: 1 speaker (alice)")
	emitActiveSpeakers(t, compositor, []string{"alice"}, []float32{0.9})
	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "01_1speaker")

	count1 := videoCount.Load()
	if count1 <= 0 {
		t.Fatal("no video buffers with 1 speaker")
	}

	// 2 speakers → 2×1 grid
	t.Log("layout: 2 speakers (alice, bob)")
	emitActiveSpeakers(t, compositor, []string{"alice", "bob"}, []float32{0.9, 0.7})
	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "02_2speakers")

	count2 := videoCount.Load()
	if count2 <= count1 {
		t.Fatal("video stalled after resize to 2 speakers")
	}

	// 3 speakers → 2×2 grid (one cell empty)
	t.Log("layout: 3 speakers (alice, bob, carol)")
	emitActiveSpeakers(t, compositor, []string{"alice", "bob", "carol"}, []float32{0.9, 0.7, 0.5})
	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "03_3speakers")

	count3 := videoCount.Load()
	if count3 <= count2 {
		t.Fatal("video stalled after resize to 3 speakers")
	}

	// 4 speakers → 2×2 grid (full)
	t.Log("layout: 4 speakers (alice, bob, carol, dave)")
	emitActiveSpeakers(t, compositor, []string{"alice", "bob", "carol", "dave"}, []float32{0.9, 0.7, 0.5, 0.3})
	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "04_4speakers")

	count4 := videoCount.Load()
	if count4 <= count3 {
		t.Fatal("video stalled after resize to 4 speakers")
	}

	// Back to 2 speakers — grid shrinks, surplus compositor pads released
	t.Log("layout: 2 speakers (carol, dave) — alice+bob out")
	emitActiveSpeakers(t, compositor, []string{"carol", "dave"}, []float32{0.9, 0.8})
	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "05_back_to_2speakers")

	count5 := videoCount.Load()
	if count5 <= count4 {
		t.Fatal("video stalled after shrinking back to 2 speakers")
	}

	// Back to 1 speaker
	t.Log("layout: 1 speaker (dave)")
	emitActiveSpeakers(t, compositor, []string{"dave"}, []float32{0.9})
	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "06_back_to_1speaker")

	count6 := videoCount.Load()
	if count6 <= count5 {
		t.Fatal("video stalled after shrinking to 1 speaker")
	}

	// Back to 4 — full expansion again
	t.Log("layout: 4 speakers again (dave, carol, bob, alice)")
	emitActiveSpeakers(t, compositor, []string{"dave", "carol", "bob", "alice"}, []float32{0.9, 0.7, 0.5, 0.3})
	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "07_back_to_4speakers")

	count7 := videoCount.Load()
	if count7 <= count6 {
		t.Fatal("video stalled after re-expanding to 4 speakers")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	t.Logf("final: audio=%d video=%d", audioCount.Load(), videoCount.Load())
}

func TestCompositor_ActiveSpeakersBeforeTracks(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-speakers-before-tracks")

	// Set PLAYING with empty compositor
	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// Emit active speakers before any tracks exist — must not panic or deadlock
	emitActiveSpeakers(t, compositor, []string{"participant1"}, []float32{0.8})
	t.Log("emitted active speakers with no tracks — no crash")

	dumpDot(t, pipeline, "01_no_tracks")

	// Now add a participant
	audioSrc, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	audioCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	audioCaps.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=48000,channels=1,format=S16LE"))

	audioSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}

	if err := pipeline.AddMany(audioSrc, audioCaps, audioSink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := audioSrc.Link(audioCaps); err != nil {
		t.Fatal("failed to link audio chain:", err)
	}

	bufferCount := addBufferProbe(t, audioSink)

	sinkPad := compositor.GetRequestPad("sink_2_1234_111")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	// Link the src pad created during initMicrophone
	srcPadName := fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE)
	srcPad := compositor.GetStaticPad(srcPadName)
	if srcPad == nil {
		t.Fatalf("src pad %s not found", srcPadName)
	}
	if ret := srcPad.Link(audioSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatal("failed to link compositor src to fakesink:", ret)
	}

	capsSrc := audioCaps.GetStaticPad("src")
	if ret := capsSrc.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link capsfilter to compositor:", ret)
	}

	injectTrackSourceInfo(capsSrc, livekittracks.TrackSourceInfo{
		ParticipantSID:  "participant1",
		ParticipantName: "Test User",
		TrackSID:        "track1",
		Source:          livekit.TrackSource_MICROPHONE,
		Kind:            "audio",
		MimeType:        "audio/x-raw",
		SSRC:            1234,
		PT:              111,
	})

	// Sync new elements
	for _, e := range []*gst.Element{audioSrc, audioCaps, audioSink} {
		e.SyncStateWithParent()
	}

	// Wait for TrackSourceInfo to propagate
	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "02_track_added")

	// Emit again — now the track should be registered
	emitActiveSpeakers(t, compositor, []string{"participant1"}, []float32{0.8})

	time.Sleep(3 * time.Second)

	dumpDot(t, pipeline, "03_after_second_emit")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d audio buffers", count)
	if count <= 0 {
		t.Fatal("no audio buffers received after late track arrival")
	}
}

// --- Group 5: Pad Release ---

func TestCompositor_ReleaseMicrophoneSinkPad(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-release-mic-pad")

	alice := addParticipant(t, pipeline, compositor, "alice", 1001, 2001, 0)
	addParticipant(t, pipeline, compositor, "bob", 1002, 2002, 1)

	audioCount, videoCount := waitForSrcPadsAndLink(t, pipeline, compositor)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(2 * time.Second)

	emitActiveSpeakers(t, compositor, []string{"alice", "bob"}, []float32{0.8, 0.6})
	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "01_before_release")

	beforeRelease := audioCount.Load()
	if beforeRelease <= 0 {
		t.Fatal("no audio buffers before release")
	}

	// Release alice's microphone pad
	compositor.ReleaseRequestPad(alice.audioSinkPad)

	dumpDot(t, pipeline, "02_after_release")

	time.Sleep(2 * time.Second)

	afterRelease := audioCount.Load()
	t.Logf("before release: %d, after release: %d", beforeRelease, afterRelease)
	if afterRelease <= beforeRelease {
		t.Fatal("audio stalled after releasing alice's microphone pad — bob's audio should still flow")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	t.Logf("final: audio=%d video=%d", audioCount.Load(), videoCount.Load())
}

func TestCompositor_ReleaseCameraSinkPad(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-release-cam-pad")

	alice := addParticipant(t, pipeline, compositor, "alice", 1001, 2001, 0)
	addParticipant(t, pipeline, compositor, "bob", 1002, 2002, 1)

	audioCount, videoCount := waitForSrcPadsAndLink(t, pipeline, compositor)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(2 * time.Second)

	emitActiveSpeakers(t, compositor, []string{"alice", "bob"}, []float32{0.8, 0.6})
	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "01_before_release")

	beforeRelease := videoCount.Load()
	if beforeRelease <= 0 {
		t.Fatal("no video buffers before release")
	}

	// Release alice's camera pad
	compositor.ReleaseRequestPad(alice.videoSinkPad)

	dumpDot(t, pipeline, "02_after_release")

	time.Sleep(2 * time.Second)

	afterRelease := videoCount.Load()
	t.Logf("before release: %d, after release: %d", beforeRelease, afterRelease)
	if afterRelease <= beforeRelease {
		t.Fatal("video stalled after releasing alice's camera pad — background should still produce")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	t.Logf("final: audio=%d video=%d", audioCount.Load(), videoCount.Load())
}
