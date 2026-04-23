package sipcompositor

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

type SipCompositorVideoFallbackCpu struct {
	VideoTestSrc *gst.Element
}

func (f *SipCompositorVideoFallbackCpu) Create(self *gst.Bin) (*gst.Pad, error) {
	var err error
	f.VideoTestSrc, err = gst.NewElementWithProperties("videotestsrc", map[string]interface{}{
		"pattern": 2, // black
		"is-live": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create videotestsrc element for video fallback: %w", err)
	}
	if err := self.Add(f.VideoTestSrc); err != nil {
		return nil, fmt.Errorf("failed to add videotestsrc element for video fallback to bin: %w", err)
	}
	if !f.VideoTestSrc.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of videotestsrc element for video fallback with parent")
	}
	return f.VideoTestSrc.GetStaticPad("src"), nil
}

func (f *SipCompositorVideoFallbackCpu) Cleanup(self *gst.Bin) error {
	if err := f.VideoTestSrc.SetState(gst.StateNull); err != nil {
		return fmt.Errorf("failed to set videotestsrc element for video fallback to null state: %w", err)
	}
	if err := self.Remove(f.VideoTestSrc); err != nil {
		return fmt.Errorf("failed to remove videotestsrc element for video fallback from bin: %w", err)
	}
	return nil
}

type SipCompositorVideoFallbackNVidia struct {
	VideoTestSrc *gst.Element
	CudaUpload   *gst.Element
}

func (f *SipCompositorVideoFallbackNVidia) Create(self *gst.Bin) (*gst.Pad, error) {
	var err error
	f.VideoTestSrc, err = gst.NewElementWithProperties("videotestsrc", map[string]interface{}{
		"pattern": 2, // black
		"is-live": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create videotestsrc element for video fallback: %w", err)
	}
	f.CudaUpload, err = gst.NewElementWithProperties("cudaupload", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create cudaupload element for video fallback: %w", err)
	}
	if err := self.AddMany(f.VideoTestSrc, f.CudaUpload); err != nil {
		return nil, fmt.Errorf("failed to add elements for video fallback to bin: %w", err)
	}
	if err := f.VideoTestSrc.Link(f.CudaUpload); err != nil {
		return nil, fmt.Errorf("failed to link elements for video fallback: %w", err)
	}
	if !f.VideoTestSrc.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of videotestsrc element for video fallback with parent")
	}
	if !f.CudaUpload.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of cudaupload element for video fallback with parent")
	}
	return f.CudaUpload.GetStaticPad("src"), nil
}

func (f *SipCompositorVideoFallbackNVidia) Cleanup(self *gst.Bin) error {
	if err := f.VideoTestSrc.SetState(gst.StateNull); err != nil {
		return fmt.Errorf("failed to set videotestsrc element for video fallback to null state: %w", err)
	}
	if err := f.CudaUpload.SetState(gst.StateNull); err != nil {
		return fmt.Errorf("failed to set cudaupload element for video fallback to null state: %w", err)
	}
	if err := self.RemoveMany(f.VideoTestSrc, f.CudaUpload); err != nil {
		return fmt.Errorf("failed to remove elements for video fallback from bin: %w", err)
	}
	return nil
}

type SipCompositorVideoFallback interface {
	Create(self *gst.Bin) (*gst.Pad, error)
	Cleanup(self *gst.Bin) error
}
