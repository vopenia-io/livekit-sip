package nvvideoh264

// Exported test helpers for nv-video-h264. Must not import "testing"
// or introduce globals/init — otherwise DCE can't strip these symbols
// from production binaries that import nvvideoh264.

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

type TestElement struct{}

func Test() TestElement { return TestElement{} }

func (TestElement) Name() string { return "nv-video-h264" }

// BuildSource: videotestsrc -> capsfilter(raw I420 WxH@fps) ->
// cudaupload. Raw frames are generated on the CPU and only cross to
// the GPU at the element-under-test boundary, so the source chain
// doesn't share any GPU compute with the element we're measuring.
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

	up, err := gst.NewElementWithName("cudaupload", "src_upload")
	if err != nil {
		return nil, fmt.Errorf("cudaupload: %w", err)
	}

	if err := p.AddMany(src, caps, up); err != nil {
		return nil, fmt.Errorf("add source chain: %w", err)
	}
	if err := gst.ElementLinkMany(src, caps, up); err != nil {
		return nil, fmt.Errorf("link source chain: %w", err)
	}
	return up.GetStaticPad("src"), nil
}

func (TestElement) BuildElement(p *gst.Pipeline, targetWidth, targetHeight int) (*gst.Element, error) {
	e, err := gst.NewElementWithProperties("nv-video-h264", map[string]any{
		"video-width":  uint(targetWidth),
		"video-height": uint(targetHeight),
	})
	if err != nil {
		return nil, fmt.Errorf("nv-video-h264: %w", err)
	}
	if err := p.Add(e); err != nil {
		return nil, fmt.Errorf("add nv-video-h264: %w", err)
	}
	return e, nil
}

// BuildSink: fakesink. The element outputs RTP already.
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
