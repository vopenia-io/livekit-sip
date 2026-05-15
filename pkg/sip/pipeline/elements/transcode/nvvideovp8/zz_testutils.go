package nvvideovp8

// Exported test helpers for nv-video-vp8. Must not import "testing"
// or introduce globals/init — otherwise DCE can't strip these symbols
// from production binaries that import nvvideovp8.

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

type TestElement struct{}

func Test() TestElement { return TestElement{} }

func (TestElement) Name() string { return "nv-video-vp8" }

// BuildSource: videotestsrc -> capsfilter(I420 sys) -> cudaupload ->
// cudaconvertscale -> capsfilter(NV12 CUDAMemory). Emits CUDA NV12
// video (memType 1) so cudaipcsink can do a zero-copy handoff to the
// child. See nvvideoh264 for the rationale on the capsfilter chain.
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

	up, err := gst.NewElementWithName("cudaupload", "src_upload")
	if err != nil {
		return nil, 0, fmt.Errorf("cudaupload: %w", err)
	}

	conv, err := gst.NewElementWithName("cudaconvertscale", "src_cuda_convert")
	if err != nil {
		return nil, 0, fmt.Errorf("cudaconvertscale: %w", err)
	}

	cudaCaps, err := gst.NewElementWithName("capsfilter", "src_cuda_caps")
	if err != nil {
		return nil, 0, fmt.Errorf("src cuda capsfilter: %w", err)
	}
	cudaCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw(memory:CUDAMemory),width=%d,height=%d,framerate=%d/1,format=NV12", width, height, fps),
	))

	if err := p.AddMany(src, caps, up, conv, cudaCaps); err != nil {
		return nil, 0, fmt.Errorf("add source chain: %w", err)
	}
	if err := gst.ElementLinkMany(src, caps, up, conv, cudaCaps); err != nil {
		return nil, 0, fmt.Errorf("link source chain: %w", err)
	}
	return cudaCaps.GetStaticPad("src"), 1, nil
}

func (TestElement) BuildElement(p *gst.Pipeline, targetWidth, targetHeight int) (*gst.Element, error) {
	e, err := gst.NewElementWithProperties("nv-video-vp8", map[string]any{
		"video-width":  uint(targetWidth),
		"video-height": uint(targetHeight),
	})
	if err != nil {
		return nil, fmt.Errorf("nv-video-vp8: %w", err)
	}
	if err := p.Add(e); err != nil {
		return nil, fmt.Errorf("add nv-video-vp8: %w", err)
	}
	return e, nil
}

// BuildSink: fakesink. The element outputs RTP already (cudadownload
// is internal, NVENC has no VP8).
func (TestElement) BuildSink(p *gst.Pipeline) (*gst.Pad, int, error) {
	sink, err := gst.NewElementWithName("fakesink", "sink")
	if err != nil {
		return nil, 0, fmt.Errorf("fakesink: %w", err)
	}
	sink.SetProperty("sync", false)
	sink.SetProperty("async", false)
	if err := p.Add(sink); err != nil {
		return nil, 0, fmt.Errorf("add sink: %w", err)
	}
	return sink.GetStaticPad("sink"), 0, nil
}
