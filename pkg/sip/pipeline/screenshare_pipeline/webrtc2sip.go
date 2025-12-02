package screenshare_pipeline

import (
	"fmt"
	"sync/atomic"
	"time"

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

	// Debug stats
	inputPackets  atomic.Uint64
	inputBytes    atomic.Uint64
	outputPackets atomic.Uint64
	outputBytes   atomic.Uint64
	stopMonitor   chan struct{}
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
	// Stop the monitor goroutine
	if stw.stopMonitor != nil {
		close(stw.stopMonitor)
	}

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

// StartDataFlowMonitor starts logging data flow statistics for debugging.
// It adds probes to track input/output and logs total stats periodically.
// The optional getDst function returns the current RTP destination address.
func (stw *WebrtcToSip) StartDataFlowMonitor(getDst func() string) {
	stw.stopMonitor = make(chan struct{})

	// Add probe on RtpSrc output (data entering the pipeline from WebRTC)
	srcPad := stw.RtpSrc.GetStaticPad("src")
	if srcPad != nil {
		srcPad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			buffer := info.GetBuffer()
			if buffer != nil {
				stw.inputPackets.Add(1)
				stw.inputBytes.Add(uint64(buffer.GetSize()))
			}
			return gst.PadProbeOK
		})
		stw.log.Infow("added INPUT probe on RtpSrc")
	} else {
		stw.log.Warnw("failed to get src pad from RtpSrc for probe", nil)
	}

	// Add probe on RtpSink input (data leaving the pipeline to SIP)
	sinkPad := stw.RtpSink.GetStaticPad("sink")
	if sinkPad != nil {
		sinkPad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			buffer := info.GetBuffer()
			if buffer != nil {
				stw.outputPackets.Add(1)
				stw.outputBytes.Add(uint64(buffer.GetSize()))
			}
			return gst.PadProbeOK
		})
		stw.log.Infow("added OUTPUT probe on RtpSink")
	} else {
		stw.log.Warnw("failed to get sink pad from RtpSink for probe", nil)
	}

	// Start periodic stats logging
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		var lastInputPackets, lastOutputPackets uint64
		var lastInputBytes, lastOutputBytes uint64

		for {
			select {
			case <-stw.stopMonitor:
				stw.log.Infow("screenshare data flow monitor stopped",
					"totalInputPackets", stw.inputPackets.Load(),
					"totalInputBytes", stw.inputBytes.Load(),
					"totalOutputPackets", stw.outputPackets.Load(),
					"totalOutputBytes", stw.outputBytes.Load(),
				)
				return
			case <-ticker.C:
				currentInputPackets := stw.inputPackets.Load()
				currentOutputPackets := stw.outputPackets.Load()
				currentInputBytes := stw.inputBytes.Load()
				currentOutputBytes := stw.outputBytes.Load()

				deltaInputPackets := currentInputPackets - lastInputPackets
				deltaOutputPackets := currentOutputPackets - lastOutputPackets
				deltaInputBytes := currentInputBytes - lastInputBytes
				deltaOutputBytes := currentOutputBytes - lastOutputBytes

				dst := ""
				if getDst != nil {
					dst = getDst()
				}
				stw.log.Infow("screenshare pipeline data flow stats",
					"inputPackets", currentInputPackets,
					"outputPackets", currentOutputPackets,
					"inputBytes", currentInputBytes,
					"outputBytes", currentOutputBytes,
					"deltaInputPackets", deltaInputPackets,
					"deltaOutputPackets", deltaOutputPackets,
					"deltaInputKB", float64(deltaInputBytes)/1024,
					"deltaOutputKB", float64(deltaOutputBytes)/1024,
					"sendingToDestination", currentOutputPackets > 0,
					"rtpDestination", dst,
				)

				// Warn if no output data
				if currentInputPackets > 0 && deltaOutputPackets == 0 {
					stw.log.Warnw("screenshare pipeline receiving input but NOT sending output!", nil,
						"inputPackets", currentInputPackets,
						"outputPackets", currentOutputPackets,
					)
				}

				lastInputPackets = currentInputPackets
				lastOutputPackets = currentOutputPackets
				lastInputBytes = currentInputBytes
				lastOutputBytes = currentOutputBytes
			}
		}
	}()

	stw.log.Infow("screenshare data flow monitor started")
}
