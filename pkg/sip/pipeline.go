package sip

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/google/uuid"
)

type PipelineBuilder struct {
	err      error
	pipeline *gst.Pipeline
	appsrc   *app.Source
	appsink  *app.Sink

	// Elements
	jitterBuffer *gst.Element
	vp8depay     *gst.Element
	vp8dec       *gst.Element
	vp8enc       *gst.Element
	vp8pay       *gst.Element
}

func NewPipelineBuilder() *PipelineBuilder {
	return &PipelineBuilder{}
}

func (b *PipelineBuilder) withPipeline() *PipelineBuilder {
	if b.err != nil || b.pipeline != nil {
		return b
	}

	pipeline, err := gst.NewPipeline("vp8-transcoder" + uuid.New().String())
	if err != nil {
		b.err = fmt.Errorf("failed to create pipeline: %w", err)
		return b
	}
	b.pipeline = pipeline
	return b
}

func (b *PipelineBuilder) createElements() *PipelineBuilder {
	if b.err != nil {
		return b
	}

	var errs error
	var err error

	helper := func(name string) *gst.Element {
		e, err := gst.NewElement(name)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to create element %s: %w", name, err))
		}
		return e
	}

	_ = helper

	b.appsrc, err = app.NewAppSrc()
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to create appsrc: %w", err))
	}

	b.jitterBuffer = helper("rtpjitterbuffer")
	b.vp8pay = helper("rtpvp8pay")
	b.vp8dec = helper("vp8dec")
	b.vp8enc = helper("vp8enc")
	b.vp8depay = helper("rtpvp8depay")

	b.appsink, err = app.NewAppSink()
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to create appsink: %w", err))
	}

	if errs != nil {
		b.err = fmt.Errorf("failed to create elements: %w", errs)
	}
	return b
}

func (b *PipelineBuilder) configureElements() *PipelineBuilder {
	if b.err != nil {
		return b
	}

	var errs error

	type propertySettable interface {
		SetProperty(string, any) error
		GetFactory() *gst.ElementFactory
		GetName() string
	}

	helper := func(element propertySettable, property string, value any) {
		if err := element.SetProperty(property, value); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to set %s property on %s:%s: %w", property, element.GetFactory().GetName(), element.GetName(), err))
		}
	}
	_ = helper

	// Configure appsrc
	// helper(b.appsrc.Element, "is-live", true)
	// helper(b.appsrc.Element, "format", uint(3)) // --- IGNORE ---
	// helper(b.appsrc.Element, "do-timestamp", true)
	// helper(b.appsrc.Element, "block", true)
	// helper(b.appsrc.Element, "max-bytes", uint(2_000_000)) // 2 MB buffer

	// Set caps for incoming RTP vp8 video
	caps := gst.NewCapsFromString(
		"application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96",
	)
	b.appsrc.SetCaps(caps)

	// Configure jitter buffer for low latency
	// helper(b.jitterBuffer, "latency", uint(50))        // 50ms latency
	// helper(b.jitterBuffer, "do-lost", true)            // Handle lost packets
	// helper(b.jitterBuffer, "do-retransmission", false) // Disable for lower latency

	// Configure VP8 encoder
	helper(b.vp8enc, "deadline", int64(1))           // Realtime encoding
	helper(b.vp8enc, "target-bitrate", int(2000000)) // 1 Mbps
	// helper(b.vp8enc, "end-usage", 1)                  // CBR mode
	// helper(b.vp8enc, "error-resilient", uint8(1))     // Error resilience for packet loss
	// helper(b.vp8enc, "keyframe-max-dist", uint(30))   // Keyframe every 30 frames
	// helper(b.vp8enc, "min-quantizer", uint(4))
	// helper(b.vp8enc, "max-quantizer", uint(20))
	// helper(b.vp8enc, "lag-in-frames", uint(5)) // No frame lag for low latency

	// Configure VP8 payloader
	helper(b.vp8pay, "pt", uint(96))
	helper(b.vp8pay, "mtu", uint(1200)) // Smaller MTU for WebRTC
	// helper(b.rtpvp8pay, "picture-id-mode", 2) // Extended picture ID

	// Configure appsink
	// helper(b.appsink, "emit-signals", true)
	// helper(b.appsink, "drop", true)           // Drop old buffers if full
	// helper(b.appsink, "max-buffers", uint(1)) // Minimal buffering

	if errs != nil {
		b.err = fmt.Errorf("failed to configure elements: %w", errs)
	}
	return b
}

func (b *PipelineBuilder) addElements() *PipelineBuilder {
	if b.err != nil {
		return b
	}

	elements := []*gst.Element{
		b.appsrc.Element,
		b.jitterBuffer,
		b.vp8depay,
		b.vp8dec,
		b.vp8enc,
		b.vp8pay,
		b.appsink.Element,
	}

	var errs error
	for _, elem := range elements {
		if err := b.pipeline.Add(elem); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to add element %s:%s to pipeline: %w", elem.GetFactory().GetName(), elem.GetName(), err))
		}
	}
	if errs != nil {
		b.err = fmt.Errorf("failed to add elements to pipeline: %w", errs)
		return b
	}
	return b
}

func (b *PipelineBuilder) linkElements() *PipelineBuilder {
	if b.err != nil {
		return b
	}

	var errs error
	helper := func(src, dst *gst.Element) {
		if err := src.Link(dst); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to link %s:%s to %s:%s: %w", src.GetFactory().GetName(), src.GetName(), dst.GetFactory().GetName(), dst.GetName(), err))
		}
	}

	helper(b.appsrc.Element, b.jitterBuffer)
	helper(b.jitterBuffer, b.vp8depay)
	helper(b.vp8depay, b.vp8dec)
	helper(b.vp8dec, b.vp8enc)
	helper(b.vp8enc, b.vp8pay)
	helper(b.vp8pay, b.appsink.Element)

	// helper(b.appsrc.Element, b.appsink.Element)

	if errs != nil {
		b.err = fmt.Errorf("failed to link elements: %w", errs)
	}
	return b
}

// func (b *PipelineBuilder) strPipeline() *PipelineBuilder {
// 	if b.err != nil {
// 		return b
// 	}

// 	pipelineStr := fmt.Sprintf(
// 		"appsrc name=src format=3 is-live=true do-timestamp=true max-buffers=%d ! "+
// 			"application/x-rtp,media=video,clock-rate=90000,encoding-name=VP8 ! "+
// 			"rtpvp8depay ! "+
// 			"vp8dec ! "+
// 			"videoconvert ! "+
// 			"x264enc tune=zerolatency bitrate=%d speed-preset=superfast "+
// 			"key-int-max=%d bframes=0 byte-stream=true threads=%d "+
// 			"vbv-buf-capacity=1000 ! "+
// 			"video/x-h264,profile=baseline ! "+
// 			"h264parse config-interval=1 ! "+
// 			"rtph264pay aggregate-mode=none config-interval=1 ! "+
// 			"appsink name=sink sync=false max-buffers=%d drop=true",
// 		t.config.InputBufferSize,
// 		t.config.BitrateKbps,
// 		t.config.KeyFrameInterval,
// 		t.config.ThreadCount,
// 		t.config.OutputBufferSize,
// 	)
// }

func (b *PipelineBuilder) Build() (*gst.Pipeline, error) {
	if b.err != nil {
		return nil, b.err
	}

	b.withPipeline().
		createElements().
		configureElements().
		addElements().
		linkElements()

	if b.err != nil {
		slog.Error("failed to build pipeline", "error", b.err)
		return nil, b.err
	}

	return b.pipeline, nil
}
