package camera_pipeline

import (
	"fmt"
	"strings"
	"sync"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/media-sdk/h264"
	v2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

const VP8CAPS = "application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96,rtcp-fb-nack-pli=true"

var webrtcCaps = map[uint]string{
	96: VP8CAPS,
}

type WebrtcToSip struct {
	log          logger.Logger
	mu           sync.Mutex
	WebrtcTracks map[uint32]*WebrtcTrack

	RtpBin *gst.Element

	RtpFunnel *gst.Element
	//rtpbin
	InputSelector *gst.Element
	// Vp8Depay      *gst.Element
	Vp8Dec      *gst.Element
	VideoRate   *gst.Element
	RateFilter  *gst.Element
	VideoScale  *gst.Element
	ScaleFilter *gst.Element
	Queue       *gst.Element
	X264Enc     *gst.Element
	RtpH264Pay  *gst.Element
	OutQueue    *gst.Element
	SipRtpSink  *gst.Element

	RtcpFunnel *gst.Element
	//rtpbin
	WebrtcRtcpSink *gst.Element

	SipRtpAppSink     *app.Sink
	WebrtcRtcpAppSink *app.Sink

	// Signal handlers for cleanup
	rtpBinPtMapHandle    glib.SignalHandle
	rtpBinPadAddedHandle glib.SignalHandle

	// Request pads for cleanup
	recvRtpSinkPad  *gst.Pad
	recvRtcpSinkPad *gst.Pad
	sendRtcpSrcPad  *gst.Pad
}

func (wts *WebrtcToSip) Configure(media *v2.SDPMedia) error {
	if media.Codec.Codec.Info().SDPName != h264.SDPName {
		return fmt.Errorf("unsupported codec %s for SIP video", media.Codec.Codec.Info().SDPName)
	}
	wts.SipRtpSink.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
		media.Codec.PayloadType,
	)))
	return nil
}

var _ pipeline.GstChain = (*WebrtcToSip)(nil)

func buildSelectorToSipChain(log logger.Logger) (*WebrtcToSip, error) {
	rtpbin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"autoremove":         true,
		"do-lost":            true,
		"do-sync-event":      true,
		"drop-on-latency":    true,
		"latency":            uint64(0),
		"rtcp-sync-interval": uint64(1000000000), // 1s
		"rtp-profile":        int(3),             // RTP_PROFILE_AVPF
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtpbin: %w", err)
	}

	rtpfunnel, err := gst.NewElementWithProperties("rtpfunnel", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp funnel: %w", err)
	}

	inputSelector, err := gst.NewElementWithProperties("input-selector", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc input selector: %w", err)
	}

	vp8dec, err := gst.NewElementWithProperties("vp8dec", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc vp8 decoder: %w", err)
	}

	videorate, err := gst.NewElement("videorate")
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videorate: %w", err)
	}

	ratefilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rate capsfilter: %w", err)
	}

	videoscale, err := gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videoscale: %w", err)
	}

	scalefilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc scale capsfilter: %w", err)
	}

	queue, err := gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc queue: %w", err)
	}

	x264enc, err := gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"bitrate":      int(2000),
		"key-int-max":  int(48), // Matches framerate for 1 keyframe/sec
		"speed-preset": int(1),  // ultrafast
		"tune":         int(4),  // zerolatency
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc x264 encoder: %w", err)
	}

	rtph264pay, err := gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"mtu":             int(1200),
		"config-interval": int(1),
		"aggregate-mode":  int(1), // zero-latency
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp h264 payloader: %w", err)
	}

	sipRtpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "sip_rtp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
		"async":        false, // Don't wait for preroll - live pipeline
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc appsink: %w", err)
	}

	rtcpFunnel, err := gst.NewElementWithProperties("funnel", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp funnel: %w", err)
	}

	outQueue, err := gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp out queue: %w", err)
	}

	webrtcRtcpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "webrtc_rtcp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
		"async":        false, // Don't wait for preroll - live pipeline
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp appsink: %w", err)
	}

	return &WebrtcToSip{
		log:          log,
		WebrtcTracks: make(map[uint32]*WebrtcTrack),

		RtpBin: rtpbin,

		RtpFunnel: rtpfunnel,
		// rtpbin
		InputSelector: inputSelector,
		// Vp8Depay:      vp8depay,
		Vp8Dec:      vp8dec,
		VideoRate:   videorate,
		RateFilter:  ratefilter,
		VideoScale:  videoscale,
		ScaleFilter: scalefilter,
		Queue:       queue,
		X264Enc:     x264enc,
		RtpH264Pay:  rtph264pay,
		OutQueue:    outQueue,
		SipRtpSink:  sipRtpSink,

		RtcpFunnel: rtcpFunnel,
		// rtpbin
		WebrtcRtcpSink: webrtcRtcpSink,

		SipRtpAppSink:     app.SinkFromElement(sipRtpSink),
		WebrtcRtcpAppSink: app.SinkFromElement(webrtcRtcpSink),
	}, nil
}

// Add implements GstChain.
func (wts *WebrtcToSip) Add(pipeline *gst.Pipeline) error {
	if err := pipeline.AddMany(
		wts.RtpBin,

		wts.RtpFunnel,
		// rtpbin
		wts.InputSelector,
		// wts.Vp8Depay,
		wts.Vp8Dec,
		wts.VideoRate,
		wts.RateFilter,
		wts.VideoScale,
		wts.ScaleFilter,
		wts.Queue,
		wts.X264Enc,
		wts.RtpH264Pay,
		wts.OutQueue,
		wts.SipRtpSink,

		wts.RtcpFunnel,
		// rtpbin
		wts.WebrtcRtcpSink,
	); err != nil {
		return fmt.Errorf("failed to add SelectorToSip elements to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (wts *WebrtcToSip) Link(p *gst.Pipeline) error {
	var err error

	// Store signal handle for cleanup
	wts.rtpBinPtMapHandle, err = wts.RtpBin.Connect("request-pt-map", func(self *gst.Element, session uint, pt uint) *gst.Caps {
		caps, ok := webrtcCaps[pt]
		if !ok {
			return nil
		}
		wts.log.Debugw("RTPBIN requested PT map", "pt", pt, "caps", caps)
		return gst.NewCapsFromString(caps)
	})
	if err != nil {
		return fmt.Errorf("failed to connect to rtpbin request-pt-map signal: %w", err)
	}

	// Store request pad for cleanup
	wts.recvRtpSinkPad = wts.RtpBin.GetRequestPad("recv_rtp_sink_0")
	if err := pipeline.LinkPad(
		wts.RtpFunnel.GetStaticPad("src"),
		wts.recvRtpSinkPad,
	); err != nil {
		return fmt.Errorf("failed to link WebrtcToSip rtp funnel to rtpbin: %w", err)
	}

	// Store signal handle for cleanup
	wts.rtpBinPadAddedHandle, err = wts.RtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		wts.log.Debugw("RTPBIN PAD ADDED", "pad", pad.GetName())
		padName := pad.GetName()
		if !strings.HasPrefix(padName, "recv_rtp_src_") {
			return
		}
		var sessionID, ssrc, payloadType uint32
		if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &sessionID, &ssrc, &payloadType); err != nil {
			wts.log.Warnw("Invalid RTP pad format", err, "pad", padName)
			return
		}
		wts.log.Infow("RTP pad added", "pad", padName, "sessionID", sessionID, "ssrc", ssrc, "payloadType", payloadType)

		wts.mu.Lock()
		defer wts.mu.Unlock()

		track, ok := wts.WebrtcTracks[ssrc]
		if !ok {
			wts.log.Warnw("No WebRTC track found for SSRC", nil, "ssrc", ssrc)
			return
		}

		if err := track.LinkParent(wts, pad); err != nil {
			wts.log.Errorw("Failed to link RTP pad to WebRTC track", err, "pad", padName, "ssrc", ssrc)
			return
		}
		pad.Connect("unlinked", func(_ interface{}) {
			wts.log.Infow("RTP pad unlinked", "pad", padName, "ssrc", ssrc)
			// wts.mu.Lock()
			// defer wts.mu.Unlock()
			if err := track.Close(p); err != nil {
				wts.log.Errorw("Failed to close WebRTC track on pad unlink", err, "ssrc", ssrc)
			}
		})
		wts.log.Infow("Linked RTP pad", "pad", padName)
	})
	if err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if err := gst.ElementLinkMany(
		wts.InputSelector,
		wts.Vp8Dec,
		wts.VideoRate,
		wts.RateFilter,
		wts.VideoScale,
		wts.ScaleFilter,
		wts.Queue,
		wts.X264Enc,
		wts.RtpH264Pay,
		wts.OutQueue,
		wts.SipRtpSink,
	); err != nil {
		return fmt.Errorf("failed to link SelectorToSip video elements: %w", err)
	}

	// Store request pads for cleanup
	wts.recvRtcpSinkPad = wts.RtpBin.GetRequestPad("recv_rtcp_sink_0")
	if err := pipeline.LinkPad(
		wts.RtcpFunnel.GetStaticPad("src"),
		wts.recvRtcpSinkPad,
	); err != nil {
		return fmt.Errorf("failed to link SelectorToSip rtcp funnel to rtpbin: %w", err)
	}

	wts.sendRtcpSrcPad = wts.RtpBin.GetRequestPad("send_rtcp_src_0")
	if err := pipeline.LinkPad(
		wts.sendRtcpSrcPad,
		wts.WebrtcRtcpSink.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link SelectorToSip rtcp rtpbin to webrtc rtcp sink: %w", err)
	}

	return nil
}

// Close implements GstChain.
func (wts *WebrtcToSip) Close(p *gst.Pipeline) error {
	wts.mu.Lock()
	defer wts.mu.Unlock()

	// 1. Close all WebrtcTracks first (releases their funnel/selector request pads)
	for ssrc, track := range wts.WebrtcTracks {
		if err := track.Close(p); err != nil {
			wts.log.Warnw("failed to close webrtc track", err, "ssrc", ssrc)
		}
	}
	wts.WebrtcTracks = nil

	// 2. Disconnect signal handlers (prevents callbacks during cleanup)
	pipeline.DisconnectSignal(wts.RtpBin, wts.rtpBinPtMapHandle)
	pipeline.DisconnectSignal(wts.RtpBin, wts.rtpBinPadAddedHandle)
	wts.rtpBinPtMapHandle = 0
	wts.rtpBinPadAddedHandle = 0

	// 3. Release ALL request pads from rtpbin, funnel, and selector
	// This must happen while elements are still in a valid state
	pipeline.ReleaseAllRequestPads(wts.RtpBin)
	pipeline.ReleaseAllRequestPads(wts.RtpFunnel)
	pipeline.ReleaseAllRequestPads(wts.RtcpFunnel)
	pipeline.ReleaseAllRequestPads(wts.InputSelector)
	wts.recvRtpSinkPad = nil
	wts.recvRtcpSinkPad = nil
	wts.sendRtcpSrcPad = nil

	// 5. Define elements to clean up
	elements := []*gst.Element{
		wts.RtpBin,
		wts.RtpFunnel,
		wts.InputSelector,
		wts.Vp8Dec,
		wts.VideoRate,
		wts.RateFilter,
		wts.VideoScale,
		wts.ScaleFilter,
		wts.Queue,
		wts.X264Enc,
		wts.RtpH264Pay,
		wts.OutQueue,
		wts.SipRtpSink,
		wts.RtcpFunnel,
		wts.WebrtcRtcpSink,
	}

	// 6. Set elements to NULL state before removal
	for _, elem := range elements {
		if elem != nil {
			elem.SetState(gst.StateNull)
		}
	}

	// 7. Remove elements from pipeline
	if p != nil {
		if err := p.RemoveMany(elements...); err != nil {
			wts.log.Warnw("failed to remove webrtc to sip chain from pipeline", err)
		}
	}

	// 8. Unref each element
	pipeline.UnrefElements(elements...)

	// 9. Nil out struct fields
	wts.RtpBin = nil
	wts.RtpFunnel = nil
	wts.InputSelector = nil
	wts.Vp8Dec = nil
	wts.VideoRate = nil
	wts.RateFilter = nil
	wts.VideoScale = nil
	wts.ScaleFilter = nil
	wts.Queue = nil
	wts.X264Enc = nil
	wts.RtpH264Pay = nil
	wts.OutQueue = nil
	wts.SipRtpSink = nil
	wts.RtcpFunnel = nil
	wts.WebrtcRtcpSink = nil
	wts.SipRtpAppSink = nil
	wts.WebrtcRtcpAppSink = nil

	return nil
}
