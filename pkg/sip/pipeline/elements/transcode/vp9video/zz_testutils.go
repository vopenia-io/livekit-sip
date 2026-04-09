package vp9video

// Exported test helpers for vp9-video. Must not import "testing" or
// introduce globals/init — otherwise DCE can't strip these symbols
// from production binaries that import vp9video.

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

type TestElement struct{}

func Test() TestElement { return TestElement{} }

func (TestElement) Name() string { return "vp9-video" }

// BuildSource: videotestsrc -> capsfilter(WxH,fps,I420) -> vp9enc -> rtpvp9pay.
// Live source so buffer cadence matches wall-clock frame interval.
func (TestElement) BuildSource(p *gst.Pipeline, width, height, fps, numBuffers int) (*gst.Pad, error) {
	src, err := gst.NewElementWithName("videotestsrc", "src")
	if err != nil {
		return nil, fmt.Errorf("videotestsrc: %w", err)
	}
	src.SetProperty("num-buffers", numBuffers)
	src.SetProperty("is-live", true)

	caps, err := gst.NewElementWithName("capsfilter", "src_caps")
	if err != nil {
		return nil, fmt.Errorf("src capsfilter: %w", err)
	}
	caps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,width=%d,height=%d,framerate=%d/1,format=I420", width, height, fps),
	))

	enc, err := gst.NewElementWithName("vp9enc", "encoder")
	if err != nil {
		return nil, fmt.Errorf("vp9enc: %w", err)
	}
	// Source-side encoder tuning to stop vp9enc from back-pressuring
	// the live source — the default preset stalls at 720p.
	enc.SetProperty("deadline", 1)
	enc.SetProperty("cpu-used", 8)

	pay, err := gst.NewElementWithName("rtpvp9pay", "payloader")
	if err != nil {
		return nil, fmt.Errorf("rtpvp9pay: %w", err)
	}
	pay.SetProperty("mtu", 1200)

	if err := p.AddMany(src, caps, enc, pay); err != nil {
		return nil, fmt.Errorf("add source chain: %w", err)
	}
	if err := gst.ElementLinkMany(src, caps, enc, pay); err != nil {
		return nil, fmt.Errorf("link source chain: %w", err)
	}
	return pay.GetStaticPad("src"), nil
}

func (TestElement) BuildElement(p *gst.Pipeline, targetWidth, targetHeight int) (*gst.Element, error) {
	e, err := gst.NewElementWithProperties("vp9-video", map[string]any{
		"video-width":  uint(targetWidth),
		"video-height": uint(targetHeight),
	})
	if err != nil {
		return nil, fmt.Errorf("vp9-video: %w", err)
	}
	if err := p.Add(e); err != nil {
		return nil, fmt.Errorf("add vp9-video: %w", err)
	}
	return e, nil
}

func (TestElement) BuildSink(p *gst.Pipeline) (*gst.Pad, error) {
	sink, err := gst.NewElementWithName("fakesink", "sink")
	if err != nil {
		return nil, fmt.Errorf("fakesink: %w", err)
	}
	sink.SetProperty("sync", false)
	if err := p.Add(sink); err != nil {
		return nil, fmt.Errorf("add sink: %w", err)
	}
	return sink.GetStaticPad("sink"), nil
}
