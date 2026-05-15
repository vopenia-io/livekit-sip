package trackfallback

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

type Fallback interface {
	Create(e *TrackFallback, self *gst.Bin) (*gst.Pad, error)
	Sync() error
	Remove(e *TrackFallback, self *gst.Bin) error
}

type AudioFallback struct {
	initialized   bool
	AudioTestSrc  *gst.Element
	SilenceFilter *gst.Element
}

func (f *AudioFallback) Create(e *TrackFallback, self *gst.Bin) (*gst.Pad, error) {
	if f.initialized {
		return nil, fmt.Errorf("audio fallback already initialized")
	}

	var err error
	f.AudioTestSrc, err = gst.NewElementWithProperties("audiotestsrc", map[string]interface{}{
		"is-live": true,
		"wave":    int(4), // silence
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create audiotestsrc: %w", err)
	}

	f.SilenceFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("audio/x-raw,format=S16LE,rate=16000,channels=1,layout=interleaved"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create silence filter: %w", err)
	}

	if err := self.AddMany(f.AudioTestSrc, f.SilenceFilter); err != nil {
		return nil, fmt.Errorf("failed to add elements to bin: %w", err)
	}

	if err := gst.ElementLinkMany(f.AudioTestSrc, f.SilenceFilter); err != nil {
		return nil, fmt.Errorf("failed to link elements: %w", err)
	}

	f.initialized = true
	return f.SilenceFilter.GetStaticPad("src"), nil
}

func (f *AudioFallback) Sync() error {
	var errs []error
	if !f.AudioTestSrc.SyncStateWithParent() {
		errs = append(errs, fmt.Errorf("failed to sync audio test src state with parent"))
	}
	if !f.SilenceFilter.SyncStateWithParent() {
		errs = append(errs, fmt.Errorf("failed to sync silence filter state with parent"))
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered errors during fallback sync: %v", errs)
	}

	return nil
}

func (f *AudioFallback) Remove(e *TrackFallback, self *gst.Bin) error {
	if !f.initialized {
		return fmt.Errorf("audio fallback not initialized")
	}

	var errs []error
	if err := f.AudioTestSrc.SetState(gst.StateNull); err != nil {
		errs = append(errs, fmt.Errorf("failed to set audio test src state to null: %w", err))
	}
	if err := f.SilenceFilter.SetState(gst.StateNull); err != nil {
		errs = append(errs, fmt.Errorf("failed to set silence filter state to null: %w", err))
	}

	if err := self.RemoveMany(f.AudioTestSrc, f.SilenceFilter); err != nil {
		errs = append(errs, fmt.Errorf("failed to remove elements from bin: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered errors during fallback removal: %v", errs)
	}

	return nil
}

type VideoFallback struct {
	Fallback
}

type VideoFallbackCPU struct {
	initialized    bool
	FakeVideoSrc   *gst.Element
	FallbackFilter *gst.Element
}

func (f *VideoFallbackCPU) Create(e *TrackFallback, self *gst.Bin) (*gst.Pad, error) {
	if f.initialized {
		return nil, fmt.Errorf("video fallback already initialized")
	}

	var err error
	f.FakeVideoSrc, err = gst.NewElementWithProperties("videotestsrc", map[string]interface{}{
		"pattern": int(2), // black
		"is-live": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create fake video src: %w", err)
	}
	f.FallbackFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw,format=I420,width=[1,%d],height=[1,%d]", e.videoWidth, e.videoHeight)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create fallback filter: %w", err)
	}

	if err := self.AddMany(
		f.FakeVideoSrc,
		f.FallbackFilter,
	); err != nil {
		return nil, fmt.Errorf("failed to add elements to bin: %w", err)
	}

	if err := gst.ElementLinkMany(f.FakeVideoSrc, f.FallbackFilter); err != nil {
		return nil, fmt.Errorf("failed to link elements: %w", err)
	}

	f.initialized = true

	return f.FallbackFilter.GetStaticPad("src"), nil
}

func (f *VideoFallbackCPU) Sync() error {
	var errs []error
	if !f.FakeVideoSrc.SyncStateWithParent() {
		errs = append(errs, fmt.Errorf("failed to sync state of fake video source with parent"))
	}
	if !f.FallbackFilter.SyncStateWithParent() {
		errs = append(errs, fmt.Errorf("failed to sync state of fallback filter with parent"))
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered errors during fallback sync: %v", errs)
	}
	return nil
}

func (f *VideoFallbackCPU) Remove(e *TrackFallback, self *gst.Bin) error {
	if !f.initialized {
		return fmt.Errorf("video fallback not initialized")
	}

	var errs []error
	if err := f.FakeVideoSrc.SetState(gst.StateNull); err != nil {
		errs = append(errs, fmt.Errorf("failed to set fake video source state to null: %w", err))
	}
	if err := f.FallbackFilter.SetState(gst.StateNull); err != nil {
		errs = append(errs, fmt.Errorf("failed to set fallback filter state to null: %w", err))
	}

	if err := self.RemoveMany(f.FakeVideoSrc, f.FallbackFilter); err != nil {
		errs = append(errs, fmt.Errorf("failed to remove elements from bin: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered errors during fallback removal: %v", errs)
	}

	return nil
}

type VideoFallbackNVidia struct {
	initialized        bool
	FakeVideoSrc       *gst.Element
	FallbackCudaUpload *gst.Element
	FallbackFilter     *gst.Element
}

func (f *VideoFallbackNVidia) Create(e *TrackFallback, self *gst.Bin) (*gst.Pad, error) {
	if f.initialized {
		return nil, fmt.Errorf("video fallback already initialized")
	}

	var err error
	f.FakeVideoSrc, err = gst.NewElementWithProperties("videotestsrc", map[string]interface{}{
		"pattern": int(2), // black
		"is-live": true,
	})
	if err != nil {
		return nil, err
	}

	f.FallbackCudaUpload, err = gst.NewElementWithProperties("cudaupload", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create CUDA upload element: %w", err)
	}

	f.FallbackFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw(memory:CUDAMemory),width=[1,%d],height=[1,%d],format=NV12", e.videoWidth, e.videoHeight)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create fallback filter: %w", err)
	}

	if err := self.AddMany(
		f.FakeVideoSrc,
		f.FallbackCudaUpload,
		f.FallbackFilter,
	); err != nil {
		return nil, fmt.Errorf("failed to add elements to bin: %w", err)
	}

	if err := gst.ElementLinkMany(f.FakeVideoSrc, f.FallbackCudaUpload, f.FallbackFilter); err != nil {
		return nil, fmt.Errorf("failed to link elements: %w", err)
	}

	f.initialized = true

	return f.FallbackFilter.GetStaticPad("src"), nil
}

func (f *VideoFallbackNVidia) Sync() error {
	var errs []error
	if !f.FakeVideoSrc.SyncStateWithParent() {
		errs = append(errs, fmt.Errorf("failed to sync state of fake video source with parent"))
	}
	if !f.FallbackCudaUpload.SyncStateWithParent() {
		errs = append(errs, fmt.Errorf("failed to sync state of fallback CUDA upload with parent"))
	}
	if !f.FallbackFilter.SyncStateWithParent() {
		errs = append(errs, fmt.Errorf("failed to sync state of fallback filter with parent"))
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered errors during fallback sync: %v", errs)
	}
	return nil
}

func (f *VideoFallbackNVidia) Remove(e *TrackFallback, self *gst.Bin) error {
	if !f.initialized {
		return fmt.Errorf("video fallback not initialized")
	}

	var errs []error
	if err := f.FakeVideoSrc.SetState(gst.StateNull); err != nil {
		errs = append(errs, fmt.Errorf("failed to set fake video source state to null: %w", err))
	}
	if err := f.FallbackCudaUpload.SetState(gst.StateNull); err != nil {
		errs = append(errs, fmt.Errorf("failed to set fallback CUDA upload state to null: %w", err))
	}
	if err := f.FallbackFilter.SetState(gst.StateNull); err != nil {
		errs = append(errs, fmt.Errorf("failed to set fallback filter state to null: %w", err))
	}

	if err := self.RemoveMany(f.FakeVideoSrc, f.FallbackCudaUpload, f.FallbackFilter); err != nil {
		errs = append(errs, fmt.Errorf("failed to remove elements from bin: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered errors during fallback removal: %v", errs)
	}

	return nil
}

func (f *VideoFallback) Create(e *TrackFallback, self *gst.Bin) (*gst.Pad, error) {
	if f.Fallback != nil {
		return f.Fallback.Create(e, self)
	}

	if e.nvidia {
		f.Fallback = &VideoFallbackNVidia{}
	} else {
		f.Fallback = &VideoFallbackCPU{}
	}
	return f.Fallback.Create(e, self)
}

func (f *VideoFallback) Sync() error {
	if f.Fallback != nil {
		return f.Fallback.Sync()
	}
	return nil
}

func (f *VideoFallback) Remove(e *TrackFallback, self *gst.Bin) error {
	if f.Fallback != nil {
		return f.Fallback.Remove(e, self)
	}
	return nil
}
