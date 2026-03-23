package iosip_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/iosip"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipcompositor"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audioopus"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/dtmfaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/g711audio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/h264video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp8video"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",io_manager_sip:5,sip_compositor:5", true)
	gst.Init(nil)
	sipcompositor.Register()
	audioopus.Register()
	g711audio.Register()
	dtmfaudio.Register()
	h264video.Register()
	vp8video.Register()
	videovp8.Register()
	iosip.Register()
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
