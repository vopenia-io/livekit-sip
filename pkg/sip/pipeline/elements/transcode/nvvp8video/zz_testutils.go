package nvvp8video

// Exported test helpers for nv-vp8-video. Must not import "testing"
// or introduce globals/init — otherwise DCE can't strip these symbols
// from production binaries that import nvvp8video.

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

type TestElement struct{}

func Test() TestElement { return TestElement{} }

func (TestElement) Name() string { return "nv-vp8-video" }

// BuildSource: videotestsrc -> capsfilter -> vp8enc -> rtpvp8pay.
// Identical to the CPU vp8-video source chain — the GPU element's
// sink pad consumes RTP so no cudaupload is needed here. Keeping the
// source on the CPU ensures we measure only the GPU element's cost,
// not shared GPU compute from the sample generator.
func (TestElement) BuildSource(p *gst.Pipeline, width, height, fps, numBuffers int) (*gst.Pad, int, error) {
	src, err := gst.NewElementWithName("videotestsrc", "src")
	if err != nil {
		return nil, 0, fmt.Errorf("videotestsrc: %w", err)
	}
	src.SetProperty("num-buffers", numBuffers)
	src.SetProperty("is-live", true)

	caps, err := gst.NewElementWithName("capsfilter", "src_caps")
	if err != nil {
		return nil, 0, fmt.Errorf("src capsfilter: %w", err)
	}
	caps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,width=%d,height=%d,framerate=%d/1,format=I420", width, height, fps),
	))

	enc, err := gst.NewElementWithName("vp8enc", "encoder")
	if err != nil {
		return nil, 0, fmt.Errorf("vp8enc: %w", err)
	}
	// Realtime preset on the source-side encoder to stop it from
	// back-pressuring the live source.
	enc.SetProperty("deadline", 1)
	enc.SetProperty("cpu-used", 8)
	enc.SetProperty("lag-in-frames", 0)

	pay, err := gst.NewElementWithName("rtpvp8pay", "payloader")
	if err != nil {
		return nil, 0, fmt.Errorf("rtpvp8pay: %w", err)
	}
	pay.SetProperty("mtu", 1200)

	if err := p.AddMany(src, caps, enc, pay); err != nil {
		return nil, 0, fmt.Errorf("add source chain: %w", err)
	}
	if err := gst.ElementLinkMany(src, caps, enc, pay); err != nil {
		return nil, 0, fmt.Errorf("link source chain: %w", err)
	}
	return pay.GetStaticPad("src"), 0, nil
}

func (TestElement) BuildElement(p *gst.Pipeline, targetWidth, targetHeight int) (*gst.Element, error) {
	e, err := gst.NewElementWithProperties("nv-vp8-video", map[string]any{
		"video-width":  uint(targetWidth),
		"video-height": uint(targetHeight),
	})
	if err != nil {
		return nil, fmt.Errorf("nv-vp8-video: %w", err)
	}
	if err := p.Add(e); err != nil {
		return nil, fmt.Errorf("add nv-vp8-video: %w", err)
	}
	return e, nil
}

// BuildSink: cudadownload -> fakesink. The element outputs raw video
// in CUDA memory; we pull it back to system memory so the residency
// measurement reflects the GPU element reaching its final output.
func (TestElement) BuildSink(p *gst.Pipeline) (*gst.Pad, int, error) {
	dl, err := gst.NewElementWithName("cudadownload", "sink_download")
	if err != nil {
		return nil, 0, fmt.Errorf("cudadownload: %w", err)
	}
	sink, err := gst.NewElementWithName("fakesink", "sink")
	if err != nil {
		return nil, 0, fmt.Errorf("fakesink: %w", err)
	}
	sink.SetProperty("sync", false)
	sink.SetProperty("async", false)
	if err := p.AddMany(dl, sink); err != nil {
		return nil, 0, fmt.Errorf("add sink chain: %w", err)
	}
	if err := gst.ElementLinkMany(dl, sink); err != nil {
		return nil, 0, fmt.Errorf("link sink chain: %w", err)
	}
	return dl.GetStaticPad("sink"), 1, nil
}
