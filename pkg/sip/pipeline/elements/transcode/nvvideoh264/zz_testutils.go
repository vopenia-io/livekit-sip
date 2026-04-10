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

// BuildSource: videotestsrc -> capsfilter(I420 sys) -> cudaupload ->
// cudaconvertscale -> capsfilter(NV12 CUDAMemory).
//
// The format conversion is load-bearing. nvh264enc (inside the EUT
// bin) only accepts NV12/Y444/VUYA/RGBA/etc — NOT I420. The EUT bin
// has an internal cudaconvertscale that would normally handle this,
// but across a cudaipc hop we want to pin the wire format so caps
// negotiation can't drop to system memory or pick a format that
// breaks the handoff.
//
// The second capsfilter is also load-bearing: cudaipcsink's pad
// template advertises both system memory and CUDAMemory, so
// cudaconvertscale could otherwise negotiate to system memory and
// ruin the zero-copy GPU handoff. Pinning both memory type AND
// format makes the whole chain unambiguous. Emits CUDA NV12 video
// (memType 1).
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
