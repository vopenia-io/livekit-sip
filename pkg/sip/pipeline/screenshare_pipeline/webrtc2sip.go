package screenshare_pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type WebrtcToSip struct {
	log logger.Logger

	RtpSrc        *gst.Element
	Vp8Depay      *gst.Element
	Vp8Dec        *gst.Element
	VideoConvert  *gst.Element
	VideoScale    *gst.Element
	ResFilter     *gst.Element
	VideoConvert2 *gst.Element
	VideoRate     *gst.Element
	FpsFilter     *gst.Element
	I420Filter    *gst.Element
	X264Enc       *gst.Element
	H264Parse     *gst.Element
	H264Pay       *gst.Element
	RtpSink       *gst.Element

	RtpAppSrc  *app.Source
	RtpAppSink *app.Sink
}

var _ pipeline.GstChain = (*WebrtcToSip)(nil)

func buildWebrtcToSipChain(log logger.Logger, sipPayloadType int) (*WebrtcToSip, error) {
	rtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(3), // GST_FORMAT_TIME; using the same numeric value as your launch string
		"max-bytes":    uint64(5_000_000),
		"block":        false,
		"caps":         gst.NewCapsFromString("application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96,rtcp-fb-nack-pli=true"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP appsrc: %w", err)
	}

	vp8depay, err := gst.NewElement("rtpvp8depay")
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc vp8 depayloader: %w", err)
	}

	vp8dec, err := gst.NewElement("vp8dec")
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc vp8 decoder: %w", err)
	}

	vconv, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videoconvert: %w", err)
	}

	// Scale to 720p - videoscale will letterbox automatically when add-borders=true
	vscale, err := gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videoscale: %w", err)
	}

	// Force 1280x720 resolution with PAR 1:1 - this forces letterboxing for non-16:9 content
	resFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc resolution capsfilter: %w", err)
	}

	// videoconvert after scaling to ensure proper format
	vconv2, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videoconvert2: %w", err)
	}

	// Force 24fps output
	vrate, err := gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true, // Only drop frames, don't duplicate
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videorate: %w", err)
	}

	// Force 24fps in caps
	fpsFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc fps capsfilter: %w", err)
	}

	// caps filter: video/x-raw,format=I420
	i420Filter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,format=I420"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc i420 capsfilter: %w", err)
	}

	x264enc, err := gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"bitrate":        int(1500),
		"key-int-max":    int(30),
		"bframes":        int(0),
		"rc-lookahead":   int(0),
		"sliced-threads": true,
		"sync-lookahead": int(0),
		"tune":           0x00000004, // GST_X264_ENC_TUNE_ZERO_LATENCY
		"speed-preset":   7,          // GST_X264_ENC_PRESET_SUPERFAST
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc x264 encoder: %w", err)
	}

	h264parse, err := gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc h264 parser: %w", err)
	}

	h264Pay, err := gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"pt":              int(sipPayloadType),
		"mtu":             int(1200),
		"config-interval": int(1),
		"aggregate-mode":  int(0), // zero-latency
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp h264 payloader: %w", err)
	}

	rtpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
		"async":        false, // Don't wait for preroll - live pipeline
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc appsink: %w", err)
	}

	return &WebrtcToSip{
		log: log,

		RtpSrc:        rtpSrc,
		Vp8Depay:      vp8depay,
		Vp8Dec:        vp8dec,
		VideoConvert:  vconv,
		VideoScale:    vscale,
		ResFilter:     resFilter,
		VideoConvert2: vconv2,
		VideoRate:     vrate,
		FpsFilter:     fpsFilter,
		I420Filter:    i420Filter,
		X264Enc:       x264enc,
		H264Parse:     h264parse,
		H264Pay:       h264Pay,
		RtpSink:       rtpSink,

		RtpAppSrc:  app.SrcFromElement(rtpSrc),
		RtpAppSink: app.SinkFromElement(rtpSink),
	}, nil
}

// Add implements GstChain.
func (stw *WebrtcToSip) Add(p *gst.Pipeline) error {
	if err := p.AddMany(
		stw.RtpSrc,
		stw.Vp8Depay,
		stw.Vp8Dec,
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
		stw.I420Filter,
		stw.X264Enc,
		stw.H264Parse,
		stw.H264Pay,
		stw.RtpSink,
	); err != nil {
		return fmt.Errorf("failed to add sip to webrtc chain to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (stw *WebrtcToSip) Link(p *gst.Pipeline) error {
	if err := gst.ElementLinkMany(
		stw.RtpSrc,
		stw.Vp8Depay,
		stw.Vp8Dec,
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
		stw.I420Filter,
		stw.X264Enc,
		stw.H264Parse,
		stw.H264Pay,
		stw.RtpSink,
	); err != nil {
		return fmt.Errorf("failed to link webrtc to sip elements: %w", err)
	}

	return nil
}

// Close implements GstChain.
func (stw *WebrtcToSip) Close(pipeline *gst.Pipeline) error {
	if err := pipeline.RemoveMany(
		stw.RtpSrc,
		stw.Vp8Depay,
		stw.Vp8Dec,
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
		stw.I420Filter,
		stw.X264Enc,
		stw.H264Parse,
		stw.H264Pay,
		stw.RtpSink,
	); err != nil {
		return fmt.Errorf("failed to remove sip to webrtc chain from pipeline: %w", err)
	}
	return nil
}
