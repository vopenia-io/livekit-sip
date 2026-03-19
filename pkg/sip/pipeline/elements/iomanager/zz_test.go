package iomanager_test

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
	"github.com/livekit/sip/pkg/sip/pipeline/elements/iomanager"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipcompositor"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audiog711"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audioopus"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/dtmfaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/g711audio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/h264video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/opusaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videoh264"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp8video"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",io_manager:5,livekit_compositor:5,sip_compositor:5", true)
	gst.Init(nil)
	livekitcompositor.Register()
	sipcompositor.Register()
	opusaudio.Register()
	audioopus.Register()
	audiog711.Register()
	g711audio.Register()
	dtmfaudio.Register()
	h264video.Register()
	vp8video.Register()
	videoh264.Register()
	videovp8.Register()
	iomanager.Register()
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
	ioManager, err := gst.NewElement("io_manager_livekit")
	if err != nil {
		t.Fatal("failed to create io_manager_livekit:", err)
	}
	if err := pipeline.Add(ioManager); err != nil {
		t.Fatal("failed to add io_manager_livekit to pipeline:", err)
	}
	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
	return pipeline, ioManager
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

var videotestsrcColors = [][2]uint32{
	{0xFFFFFFFF, 0xFF000000}, // white ball on black
	{0xFFFF0000, 0xFF0000FF}, // red ball on blue
	{0xFF00FF00, 0xFFFF00FF}, // green ball on magenta
	{0xFFFFFF00, 0xFF800080}, // yellow ball on purple
	{0xFF00FFFF, 0xFF804000}, // cyan ball on brown
}

func emitActiveSpeakers(t *testing.T, ioManager *gst.Element, sids []string, levels []float32) {
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
		ioManager.Emit("active-speakers-changed", structure)
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

func waitForSrcPad(t *testing.T, ioManager *gst.Element, padName string, timeout time.Duration) *gst.Pad {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pad := ioManager.GetStaticPad(padName)
		if pad != nil {
			return pad
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for sometimes pad %s", padName)
	return nil
}

// newOpusRTPSource creates: audiotestsrc(is-live) → audioconvert → opusenc → rtpopuspay
// Returns the rtpopuspay element and its src pad.
func newOpusRTPSource(t *testing.T, pipeline *gst.Pipeline) (*gst.Element, *gst.Pad) {
	t.Helper()

	audioSrc, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	audioConvert, err := gst.NewElement("audioconvert")
	if err != nil {
		t.Fatal("failed to create audioconvert:", err)
	}
	opusEnc, err := gst.NewElement("opusenc")
	if err != nil {
		t.Fatal("failed to create opusenc:", err)
	}
	rtpPay, err := gst.NewElement("rtpopuspay")
	if err != nil {
		t.Fatal("failed to create rtpopuspay:", err)
	}

	elements := []*gst.Element{audioSrc, audioConvert, opusEnc, rtpPay}
	if err := pipeline.AddMany(elements...); err != nil {
		t.Fatal("failed to add opus RTP source elements:", err)
	}
	if err := gst.ElementLinkMany(elements...); err != nil {
		t.Fatal("failed to link opus RTP source chain:", err)
	}

	return rtpPay, rtpPay.GetStaticPad("src")
}

// newVP8RTPSource creates: videotestsrc(is-live, ball) → videoconvert → vp8enc(deadline=1) → rtpvp8pay
// Returns the rtpvp8pay element and its src pad.
func newVP8RTPSource(t *testing.T, pipeline *gst.Pipeline, colorIndex int) (*gst.Element, *gst.Pad) {
	t.Helper()

	colors := videotestsrcColors[colorIndex%len(videotestsrcColors)]
	videoSrc, err := gst.NewElementWithProperties("videotestsrc", map[string]any{
		"is-live":          true,
		"pattern":          18, // ball
		"foreground-color": colors[0],
		"background-color": colors[1],
	})
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		t.Fatal("failed to create videoconvert:", err)
	}
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=240,framerate=15/1"))

	vp8Enc, err := gst.NewElementWithProperties("vp8enc", map[string]any{
		"deadline": 1,
	})
	if err != nil {
		t.Fatal("failed to create vp8enc:", err)
	}
	rtpPay, err := gst.NewElement("rtpvp8pay")
	if err != nil {
		t.Fatal("failed to create rtpvp8pay:", err)
	}

	elements := []*gst.Element{videoSrc, videoConvert, capsFilter, vp8Enc, rtpPay}
	if err := pipeline.AddMany(elements...); err != nil {
		t.Fatal("failed to add VP8 RTP source elements:", err)
	}
	if err := gst.ElementLinkMany(elements...); err != nil {
		t.Fatal("failed to link VP8 RTP source chain:", err)
	}

	return rtpPay, rtpPay.GetStaticPad("src")
}

type participantSource struct {
	audioPaySrcPad *gst.Pad // rtpopuspay src pad
	videoPaySrcPad *gst.Pad // rtpvp8pay src pad
	audioSinkPad   *gst.Pad // io_manager_livekit ghost sink pad (microphone)
	videoSinkPad   *gst.Pad // io_manager_livekit ghost sink pad (camera)
}

func addParticipant(t *testing.T, pipeline *gst.Pipeline, ioManager *gst.Element, sid string, audioSSRC, videoSSRC uint, colorIndex int) participantSource {
	t.Helper()

	// Audio chain: audiotestsrc → audioconvert → opusenc → rtpopuspay
	_, audioSrcPad := newOpusRTPSource(t, pipeline)

	// Video chain: videotestsrc → videoconvert → capsfilter → vp8enc → rtpvp8pay
	_, videoSrcPad := newVP8RTPSource(t, pipeline, colorIndex)

	// Request sink pads on io_manager_livekit
	audioSinkPad := ioManager.GetRequestPad(fmt.Sprintf("recv_rtp_sink_2_%d_111", audioSSRC))
	if audioSinkPad == nil {
		t.Fatal("failed to request audio sink pad for", sid)
	}
	videoSinkPad := ioManager.GetRequestPad(fmt.Sprintf("recv_rtp_sink_1_%d_96", videoSSRC))
	if videoSinkPad == nil {
		t.Fatal("failed to request video sink pad for", sid)
	}

	// Link RTP source src pads → io_manager_livekit sink pads
	if ret := audioSrcPad.Link(audioSinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link opus RTP to io_manager_livekit for", sid, ":", ret)
	}
	if ret := videoSrcPad.Link(videoSinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link VP8 RTP to io_manager_livekit for", sid, ":", ret)
	}

	// Inject TrackSourceInfo events
	injectTrackSourceInfo(audioSrcPad, livekittracks.TrackSourceInfo{
		ParticipantSID:  sid,
		ParticipantName: sid,
		TrackSID:        sid + "-audio",
		Source:          livekit.TrackSource_MICROPHONE,
		Kind:            "audio",
		MimeType:        "audio/opus",
		SSRC:            audioSSRC,
		PT:              111,
	})
	injectTrackSourceInfo(videoSrcPad, livekittracks.TrackSourceInfo{
		ParticipantSID:  sid,
		ParticipantName: sid,
		TrackSID:        sid + "-video",
		Source:          livekit.TrackSource_CAMERA,
		Kind:            "video",
		MimeType:        "video/VP8",
		SSRC:            videoSSRC,
		PT:              96,
	})

	return participantSource{
		audioPaySrcPad: audioSrcPad,
		videoPaySrcPad: videoSrcPad,
		audioSinkPad:   audioSinkPad,
		videoSinkPad:   videoSinkPad,
	}
}

func waitForSrcPadsAndLink(t *testing.T, pipeline *gst.Pipeline, ioManager *gst.Element) (audioCount, videoCount *atomic.Int32) {
	t.Helper()

	audioSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create audio fakesink:", err)
	}
	videoSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create video fakesink:", err)
	}

	if err := pipeline.AddMany(audioSink, videoSink); err != nil {
		t.Fatal("failed to add fakesinks:", err)
	}

	audioCount = addBufferProbe(t, audioSink)
	videoCount = addBufferProbe(t, videoSink)

	audioName := fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_MICROPHONE)
	videoName := fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_CAMERA)

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
			if !videoSink.SyncStateWithParent() {
				t.Logf("warning: failed to sync video fakesink state")
			}
			if ret := pad.Link(videoSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link video src pad: %v", ret)
				return
			}
			linked[name] = true
		}
	}

	ioManager.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		mu.Lock()
		defer mu.Unlock()
		tryLink(pad)
	})

	// Link any src pads that already exist
	mu.Lock()
	if pad := ioManager.GetStaticPad(audioName); pad != nil {
		tryLink(pad)
	}
	if pad := ioManager.GetStaticPad(videoName); pad != nil {
		tryLink(pad)
	}
	mu.Unlock()

	return audioCount, videoCount
}

// --- Group 1: Basics ---

func TestIoManagerLivekit_StateChanges(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, _ := newTestPipeline(t, "test-state-changes")

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
}

func TestIoManagerLivekit_SinkPadRequest(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newTestPipeline(t, "test-sink-pad-request")

	// Microphone pad (session=2)
	micPad := ioManager.GetRequestPad("recv_rtp_sink_2_1234_111")
	if micPad == nil {
		t.Fatal("GetRequestPad returned nil for microphone pad recv_rtp_sink_2_1234_111")
	}

	// Camera pad (session=1)
	camPad := ioManager.GetRequestPad("recv_rtp_sink_1_5678_96")
	if camPad == nil {
		t.Fatal("GetRequestPad returned nil for camera pad recv_rtp_sink_1_5678_96")
	}

	dumpDot(t, pipeline, "after_request")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestIoManagerLivekit_SinkPadRequest_InvalidNames(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newTestPipeline(t, "test-sink-pad-invalid")

	if pad := ioManager.GetRequestPad(""); pad != nil {
		t.Fatal("expected nil pad for empty name")
	}
	if pad := ioManager.GetRequestPad("bad_name"); pad != nil {
		t.Fatal("expected nil pad for malformed name")
	}
	if pad := ioManager.GetRequestPad("recv_rtp_sink_99_1_1"); pad != nil {
		t.Fatal("expected nil pad for unknown session 99")
	}
	if pad := ioManager.GetRequestPad("recv_rtp_sink_2_1_200"); pad != nil {
		t.Fatal("expected nil pad for PT=200 (out of range)")
	}

	dumpDot(t, pipeline, "after_invalid_requests")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestIoManagerLivekit_SourcePadCreation_Microphone(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newTestPipeline(t, "test-src-pad-mic")

	_, audioSrcPad := newOpusRTPSource(t, pipeline)

	sinkPad := ioManager.GetRequestPad("recv_rtp_sink_2_1234_111")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	if ret := audioSrcPad.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link opus RTP to io_manager_livekit:", ret)
	}

	injectTrackSourceInfo(audioSrcPad, livekittracks.TrackSourceInfo{
		ParticipantSID:  "p1",
		ParticipantName: "P1",
		TrackSID:        "t1",
		Source:          livekit.TrackSource_MICROPHONE,
		Kind:            "audio",
		MimeType:        "audio/opus",
		SSRC:            1234,
		PT:              111,
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	srcPad := waitForSrcPad(t, ioManager, fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_MICROPHONE), 5*time.Second)
	if srcPad == nil {
		t.Fatal("microphone src pad did not appear")
	}

	dumpDot(t, pipeline, "src_pad_appeared")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestIoManagerLivekit_SourcePadCreation_Camera(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newTestPipeline(t, "test-src-pad-cam")

	_, videoSrcPad := newVP8RTPSource(t, pipeline, 0)

	sinkPad := ioManager.GetRequestPad("recv_rtp_sink_1_5678_96")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	if ret := videoSrcPad.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link VP8 RTP to io_manager_livekit:", ret)
	}

	injectTrackSourceInfo(videoSrcPad, livekittracks.TrackSourceInfo{
		ParticipantSID:  "p1",
		ParticipantName: "P1",
		TrackSID:        "t1",
		Source:          livekit.TrackSource_CAMERA,
		Kind:            "video",
		MimeType:        "video/VP8",
		SSRC:            5678,
		PT:              96,
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	srcPad := waitForSrcPad(t, ioManager, fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_CAMERA), 5*time.Second)
	if srcPad == nil {
		t.Fatal("camera src pad did not appear")
	}

	dumpDot(t, pipeline, "src_pad_appeared")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

// --- Group 2: Audio Flow ---

func TestIoManagerLivekit_AudioFlow_SingleParticipant(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newTestPipeline(t, "test-audio-flow")

	_, audioSrcPad := newOpusRTPSource(t, pipeline)

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	if err := pipeline.Add(sink); err != nil {
		t.Fatal("failed to add fakesink:", err)
	}

	bufferCount := addBufferProbe(t, sink)

	sinkPad := ioManager.GetRequestPad("recv_rtp_sink_2_1234_111")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	if ret := audioSrcPad.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link opus RTP to io_manager_livekit:", ret)
	}

	injectTrackSourceInfo(audioSrcPad, livekittracks.TrackSourceInfo{
		ParticipantSID:  "participant1",
		ParticipantName: "Test User",
		TrackSID:        "track1",
		Source:          livekit.TrackSource_MICROPHONE,
		Kind:            "audio",
		MimeType:        "audio/opus",
		SSRC:            1234,
		PT:              111,
	})

	// Wait for audio src pad and link to fakesink
	srcPadName := fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_MICROPHONE)
	ioManager.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if pad.GetName() == srcPadName {
			if !sink.SyncStateWithParent() {
				t.Logf("warning: failed to sync fakesink state")
			}
			if ret := pad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link audio src pad: %v", ret)
			}
		}
	})

	// Link if the pad already exists
	if srcPad := ioManager.GetStaticPad(srcPadName); srcPad != nil {
		if !sink.SyncStateWithParent() {
			t.Logf("warning: failed to sync fakesink state")
		}
		if ret := srcPad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
			t.Logf("warning: failed to link audio src pad: %v", ret)
		}
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(3 * time.Second)

	emitActiveSpeakers(t, ioManager, []string{"participant1"}, []float32{0.8})

	time.Sleep(4 * time.Second)

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

func TestIoManagerLivekit_AudioFlow_NoActiveSpeakersNeeded(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newTestPipeline(t, "test-audio-no-speakers")

	_, audioSrcPad := newOpusRTPSource(t, pipeline)

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	if err := pipeline.Add(sink); err != nil {
		t.Fatal("failed to add fakesink:", err)
	}

	bufferCount := addBufferProbe(t, sink)

	sinkPad := ioManager.GetRequestPad("recv_rtp_sink_2_1234_111")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	if ret := audioSrcPad.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link opus RTP to io_manager_livekit:", ret)
	}

	injectTrackSourceInfo(audioSrcPad, livekittracks.TrackSourceInfo{
		ParticipantSID:  "participant1",
		ParticipantName: "Test User",
		TrackSID:        "track1",
		Source:          livekit.TrackSource_MICROPHONE,
		Kind:            "audio",
		MimeType:        "audio/opus",
		SSRC:            1234,
		PT:              111,
	})

	// Wait for audio src pad and link to fakesink
	srcPadName := fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_MICROPHONE)
	ioManager.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if pad.GetName() == srcPadName {
			if !sink.SyncStateWithParent() {
				t.Logf("warning: failed to sync fakesink state")
			}
			if ret := pad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link audio src pad: %v", ret)
			}
		}
	})

	if srcPad := ioManager.GetStaticPad(srcPadName); srcPad != nil {
		if !sink.SyncStateWithParent() {
			t.Logf("warning: failed to sync fakesink state")
		}
		if ret := srcPad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
			t.Logf("warning: failed to link audio src pad: %v", ret)
		}
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// Deliberately do NOT emit active speakers — audio should flow anyway
	time.Sleep(4 * time.Second)

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

func TestIoManagerLivekit_VideoFlow_SingleParticipant(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newTestPipeline(t, "test-video-flow")

	_, videoSrcPad := newVP8RTPSource(t, pipeline, 0)

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	if err := pipeline.Add(sink); err != nil {
		t.Fatal("failed to add fakesink:", err)
	}

	bufferCount := addBufferProbe(t, sink)

	sinkPad := ioManager.GetRequestPad("recv_rtp_sink_1_5678_96")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	if ret := videoSrcPad.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link VP8 RTP to io_manager_livekit:", ret)
	}

	injectTrackSourceInfo(videoSrcPad, livekittracks.TrackSourceInfo{
		ParticipantSID:  "participant1",
		ParticipantName: "Test User",
		TrackSID:        "track1",
		Source:          livekit.TrackSource_CAMERA,
		Kind:            "video",
		MimeType:        "video/VP8",
		SSRC:            5678,
		PT:              96,
	})

	// Wait for video src pad and link to fakesink
	srcPadName := fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_CAMERA)
	ioManager.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if pad.GetName() == srcPadName {
			if !sink.SyncStateWithParent() {
				t.Logf("warning: failed to sync fakesink state")
			}
			if ret := pad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link video src pad: %v", ret)
			}
		}
	})

	if srcPad := ioManager.GetStaticPad(srcPadName); srcPad != nil {
		if !sink.SyncStateWithParent() {
			t.Logf("warning: failed to sync fakesink state")
		}
		if ret := srcPad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
			t.Logf("warning: failed to link video src pad: %v", ret)
		}
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// Wait for TrackSourceInfo to propagate
	time.Sleep(3 * time.Second)

	// Active speakers required to trigger camera layout
	emitActiveSpeakers(t, ioManager, []string{"participant1"}, []float32{0.8})

	time.Sleep(4 * time.Second)

	dumpDot(t, pipeline, "video_flow")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d video buffers", count)
	if count <= 0 {
		t.Fatal("no video buffers received")
	}
}

func TestIoManagerLivekit_VideoFlow_BackgroundAlwaysProduces(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newTestPipeline(t, "test-video-background")

	_, videoSrcPad := newVP8RTPSource(t, pipeline, 0)

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	if err := pipeline.Add(sink); err != nil {
		t.Fatal("failed to add fakesink:", err)
	}

	bufferCount := addBufferProbe(t, sink)

	sinkPad := ioManager.GetRequestPad("recv_rtp_sink_1_5678_96")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	if ret := videoSrcPad.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link VP8 RTP to io_manager_livekit:", ret)
	}

	injectTrackSourceInfo(videoSrcPad, livekittracks.TrackSourceInfo{
		ParticipantSID:  "participant1",
		ParticipantName: "Test User",
		TrackSID:        "track1",
		Source:          livekit.TrackSource_CAMERA,
		Kind:            "video",
		MimeType:        "video/VP8",
		SSRC:            5678,
		PT:              96,
	})

	// Wait for video src pad and link to fakesink
	srcPadName := fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_CAMERA)
	ioManager.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if pad.GetName() == srcPadName {
			if !sink.SyncStateWithParent() {
				t.Logf("warning: failed to sync fakesink state")
			}
			if ret := pad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link video src pad: %v", ret)
			}
		}
	})

	if srcPad := ioManager.GetStaticPad(srcPadName); srcPad != nil {
		if !sink.SyncStateWithParent() {
			t.Logf("warning: failed to sync fakesink state")
		}
		if ret := srcPad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
			t.Logf("warning: failed to link video src pad: %v", ret)
		}
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// Deliberately do NOT emit active speakers — background videotestsrc(black) should still produce
	time.Sleep(5 * time.Second)

	dumpDot(t, pipeline, "video_background")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d video buffers from background (no active speakers)", count)
	if count <= 0 {
		t.Fatal("no video buffers received — internal videotestsrc(black) should always produce frames")
	}
}

// ===== io_manager_sip Tests =====

func newSipTestPipeline(t *testing.T, name string) (*gst.Pipeline, *gst.Element) {
	t.Helper()
	pipeline, err := gst.NewPipeline(name)
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}
	ioManager, err := gst.NewElement("io_manager_sip")
	if err != nil {
		t.Fatal("failed to create io_manager_sip:", err)
	}
	if err := pipeline.Add(ioManager); err != nil {
		t.Fatal("failed to add io_manager_sip to pipeline:", err)
	}
	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
	return pipeline, ioManager
}

// newG711RTPSource creates: audiotestsrc(is-live) → capsfilter(8kHz) → mulawenc → rtppcmupay
func newG711RTPSource(t *testing.T, pipeline *gst.Pipeline) (*gst.Element, *gst.Pad) {
	t.Helper()

	audioSrc, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=8000,channels=1,format=S16LE"))

	mulawEnc, err := gst.NewElement("mulawenc")
	if err != nil {
		t.Fatal("failed to create mulawenc:", err)
	}
	rtpPay, err := gst.NewElement("rtppcmupay")
	if err != nil {
		t.Fatal("failed to create rtppcmupay:", err)
	}

	elements := []*gst.Element{audioSrc, capsFilter, mulawEnc, rtpPay}
	if err := pipeline.AddMany(elements...); err != nil {
		t.Fatal("failed to add G711 RTP source elements:", err)
	}
	if err := gst.ElementLinkMany(elements...); err != nil {
		t.Fatal("failed to link G711 RTP source chain:", err)
	}

	return rtpPay, rtpPay.GetStaticPad("src")
}

// newH264RTPSource creates: videotestsrc(is-live) → videoconvert → capsfilter → x264enc → rtph264pay
func newH264RTPSource(t *testing.T, pipeline *gst.Pipeline) (*gst.Element, *gst.Pad) {
	t.Helper()

	videoSrc, err := gst.NewElementWithProperties("videotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		t.Fatal("failed to create videoconvert:", err)
	}
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=240,framerate=15/1"))

	x264Enc, err := gst.NewElementWithProperties("x264enc", map[string]any{
		"tune":         0x4, // zerolatency
		"speed-preset": 1,   // ultrafast
		"key-int-max":  30,
		"bframes":      0,
	})
	if err != nil {
		t.Fatal("failed to create x264enc:", err)
	}
	rtpPay, err := gst.NewElement("rtph264pay")
	if err != nil {
		t.Fatal("failed to create rtph264pay:", err)
	}

	elements := []*gst.Element{videoSrc, videoConvert, capsFilter, x264Enc, rtpPay}
	if err := pipeline.AddMany(elements...); err != nil {
		t.Fatal("failed to add H264 RTP source elements:", err)
	}
	if err := gst.ElementLinkMany(elements...); err != nil {
		t.Fatal("failed to link H264 RTP source chain:", err)
	}

	return rtpPay, rtpPay.GetStaticPad("src")
}

func TestIoManagerSip_StateChanges(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, _ := newSipTestPipeline(t, "test-sip-state-changes")

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
}

func TestIoManagerSip_SinkPadRequest(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newSipTestPipeline(t, "test-sip-sink-pad-request")

	// Microphone pad (session=2)
	micPad := ioManager.GetRequestPad("recv_rtp_sink_2_1234_0")
	if micPad == nil {
		t.Fatal("GetRequestPad returned nil for microphone pad recv_rtp_sink_2_1234_0")
	}

	// Camera pad (session=1)
	camPad := ioManager.GetRequestPad("recv_rtp_sink_1_5678_96")
	if camPad == nil {
		t.Fatal("GetRequestPad returned nil for camera pad recv_rtp_sink_1_5678_96")
	}

	dumpDot(t, pipeline, "after_request")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestIoManagerSip_SinkPadRequest_InvalidNames(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newSipTestPipeline(t, "test-sip-sink-pad-invalid")

	if pad := ioManager.GetRequestPad(""); pad != nil {
		t.Fatal("expected nil pad for empty name")
	}
	if pad := ioManager.GetRequestPad("bad_name"); pad != nil {
		t.Fatal("expected nil pad for malformed name")
	}
	if pad := ioManager.GetRequestPad("recv_rtp_sink_99_1_1"); pad != nil {
		t.Fatal("expected nil pad for unknown session 99")
	}
	if pad := ioManager.GetRequestPad("recv_rtp_sink_2_1_200"); pad != nil {
		t.Fatal("expected nil pad for PT=200 (out of range)")
	}

	dumpDot(t, pipeline, "after_invalid_requests")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestIoManagerSip_AudioFlow_G711(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newSipTestPipeline(t, "test-sip-audio-flow-g711")

	_, audioSrcPad := newG711RTPSource(t, pipeline)

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	if err := pipeline.Add(sink); err != nil {
		t.Fatal("failed to add fakesink:", err)
	}

	bufferCount := addBufferProbe(t, sink)

	sinkPad := ioManager.GetRequestPad("recv_rtp_sink_2_1234_0")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	if ret := audioSrcPad.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link G711 RTP to io_manager_sip:", ret)
	}

	srcPadName := fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_MICROPHONE)
	ioManager.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if pad.GetName() == srcPadName {
			if !sink.SyncStateWithParent() {
				t.Logf("warning: failed to sync fakesink state")
			}
			if ret := pad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link audio src pad: %v", ret)
			}
		}
	})

	if srcPad := ioManager.GetStaticPad(srcPadName); srcPad != nil {
		if !sink.SyncStateWithParent() {
			t.Logf("warning: failed to sync fakesink state")
		}
		if ret := srcPad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
			t.Logf("warning: failed to link audio src pad: %v", ret)
		}
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(4 * time.Second)

	dumpDot(t, pipeline, "audio_flow_g711")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d audio buffers (G711 → opus)", count)
	if count <= 0 {
		t.Fatal("no audio buffers received")
	}

	ioManager = nil
	pipeline = nil
}

func TestIoManagerSip_VideoFlow_H264(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, ioManager := newSipTestPipeline(t, "test-sip-video-flow-h264")

	_, videoSrcPad := newH264RTPSource(t, pipeline)

	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	if err := pipeline.Add(sink); err != nil {
		t.Fatal("failed to add fakesink:", err)
	}

	bufferCount := addBufferProbe(t, sink)

	sinkPad := ioManager.GetRequestPad("recv_rtp_sink_1_5678_96")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil")
	}

	if ret := videoSrcPad.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link H264 RTP to io_manager_sip:", ret)
	}

	srcPadName := fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_CAMERA)
	ioManager.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if pad.GetName() == srcPadName {
			if !sink.SyncStateWithParent() {
				t.Logf("warning: failed to sync fakesink state")
			}
			if ret := pad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link video src pad: %v", ret)
			}
		}
	})

	if srcPad := ioManager.GetStaticPad(srcPadName); srcPad != nil {
		if !sink.SyncStateWithParent() {
			t.Logf("warning: failed to sync fakesink state")
		}
		if ret := srcPad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
			t.Logf("warning: failed to link video src pad: %v", ret)
		}
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(4 * time.Second)

	dumpDot(t, pipeline, "video_flow_h264")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d video buffers (H264 → VP8)", count)
	if count <= 0 {
		t.Fatal("no video buffers received")
	}
}
