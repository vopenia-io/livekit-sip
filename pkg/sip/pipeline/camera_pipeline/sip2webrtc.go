package camera_pipeline

import (
	"fmt"
	"io"
	"net"
	"runtime/cgo"
	"strings"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/media-sdk/h264"
	v2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type SipToWebrtc struct {
	log logger.Logger

	RtpBin *gst.Element

	SipRtpIn *gst.Element
	// rptbin
	Depay         *gst.Element
	Parse         *gst.Element
	Decoder       *gst.Element
	VideoConvert  *gst.Element
	VideoScale    *gst.Element
	ResFilter     *gst.Element
	VideoConvert2 *gst.Element
	VideoRate     *gst.Element
	FpsFilter     *gst.Element
	Vp8Enc        *gst.Element
	RtpVp8Pay     *gst.Element
	WebrtcRtpOut  *gst.Element

	SipRtcpIn *gst.Element
	// rptbin
	SipRtcpOut *gst.Element
}

func (stw *SipToWebrtc) Configure(media *v2.SDPMedia) error {
	if media.Codec.Codec.Info().SDPName != h264.SDPName {
		return fmt.Errorf("unsupported codec %s for SIP video", media.Codec.Codec.Info().SDPName)
	}
	stw.SipRtpIn.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
		media.Codec.PayloadType,
	)))
	return nil
}

var _ pipeline.GstChain = (*SipToWebrtc)(nil)

func buildSipToWebRTCChain(log logger.Logger, rtpConn, rtcpConn net.Conn) (*SipToWebrtc, error) {
	rtpBin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name": "sip_rtp_bin",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtpbin: %w", err)
	}

	// sipRtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
	// 	"name":         "sip_rtp_in",
	// 	"is-live":      true,
	// 	"do-timestamp": true,
	// 	"format":       int(3), // GST_FORMAT_TIME; using the same numeric value as your launch string
	// 	"max-bytes":    uint64(5_000_000),
	// 	"block":        false,
	// })
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create SIP appsrc: %w", err)
	// }

	rtpHandle := cgo.NewHandle(rtpConn)
	defer rtpHandle.Delete()
	rtcpHandle := cgo.NewHandle(rtcpConn)
	defer rtcpHandle.Delete()

	sipRtpIn, err := gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":   "sip_rtp_in",
		"handle": uint64(rtpHandle),
		// "is-live":      true,
		"do-timestamp": true,
		// "format":       int(gst.FormatTime),
		// "max-bytes":    uint64(5_000_000),
		// "block":        false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtp sourcereader: %w", err)
	}

	depay, err := gst.NewElementWithProperties("rtph264depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtp depayloader: %w", err)
	}

	parse, err := gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP h264 parser: %w", err)
	}

	dec, err := gst.NewElementWithProperties("avdec_h264", map[string]interface{}{
		"max-threads": int(4),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP h264 decoder: %w", err)
	}

	vconv, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videoconvert: %w", err)
	}

	vscale, err := gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videoscale: %w", err)
	}

	// Force 1280x720 resolution with PAR 1:1 - this forces letterboxing for non-16:9 content
	resFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP resolution capsfilter: %w", err)
	}

	// videoconvert after scaling to ensure proper format
	vconv2, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videoconvert2: %w", err)
	}

	// Force 24fps output
	vrate, err := gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true, // Only drop frames, don't duplicate
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videorate: %w", err)
	}

	// Force 24fps in caps
	fpsFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP fps capsfilter: %w", err)
	}

	vp8enc, err := gst.NewElementWithProperties("vp8enc", map[string]interface{}{
		"deadline":            int(1),
		"target-bitrate":      int(1_500_000),
		"cpu-used":            int(4),
		"keyframe-max-dist":   int(30),
		"lag-in-frames":       int(0),
		"threads":             int(4),
		"buffer-initial-size": int(100),
		"buffer-optimal-size": int(120),
		"buffer-size":         int(150),
		"min-quantizer":       int(4),
		"max-quantizer":       int(40),
		"cq-level":            int(13),
		"error-resilient":     int(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP vp8 encoder: %w", err)
	}

	rtpVp8Pay, err := gst.NewElementWithProperties("rtpvp8pay", map[string]interface{}{
		"pt":              int(96),
		"mtu":             int(1200),
		"picture-id-mode": int(2), // 15-bit in your launch string
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtp vp8 payloader: %w", err)
	}

	// webrtcRtpWriter, err := gst.NewElementWithProperties("sinkwriter", map[string]interface{}{
	// 	"name":         "webrtc_rtp_out",
	// 	"emit-signals": false,
	// 	"drop":         false,
	// 	"max-buffers":  uint(100),
	// 	"sync":         false,
	// 	"async":        false,
	// })
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create SIP appsink: %w", err)
	// }

	// sipRtcpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
	// 	"name":         "sip_rtcp_in",
	// 	"is-live":      true,
	// 	"do-timestamp": true,
	// 	"format":       int(3),
	// 	"max-bytes":    uint64(200_000),
	// 	"block":        false,
	// 	"caps":         gst.NewCapsFromString("application/x-rtcp"),
	// })
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create SIP rtcp appsrc: %w", err)
	// }

	sipRtcpIn, err := gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":   "sip_rtcp_in",
		"handle": uint64(rtcpHandle),
		// "is-live":      true,
		"do-timestamp": true,
		// "format":       int(gst.FormatTime),
		// "max-bytes":    uint64(200_000),
		// "block":        false,
		"caps": gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtcp sourcereader: %w", err)
	}

	// sipRtcpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
	// 	"name":         "sip_rtcp_out",
	// 	"emit-signals": false,
	// 	"drop":         false,
	// 	"max-buffers":  uint(100),
	// 	"sync":         false,
	// 	"async":        false,
	// })
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create SIP rtcp appsink: %w", err)
	// }

	sipRtcpOut, err := gst.NewElementWithProperties("sinkwriter", map[string]interface{}{
		"name":   "sip_rtcp_out",
		"handle": uint64(uintptr(rtcpHandle)),
		"caps":   gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtcp sinkwriter: %w", err)
	}

	return &SipToWebrtc{
		log:    log,
		RtpBin: rtpBin,

		SipRtpIn: sipRtpIn,
		// rtpbin
		Depay:         depay,
		Parse:         parse,
		Decoder:       dec,
		VideoConvert:  vconv,
		VideoScale:    vscale,
		ResFilter:     resFilter,
		VideoConvert2: vconv2,
		VideoRate:     vrate,
		FpsFilter:     fpsFilter,
		Vp8Enc:        vp8enc,
		RtpVp8Pay:     rtpVp8Pay,

		SipRtcpIn: sipRtcpIn,
		// rtpbin
		SipRtcpOut: sipRtcpOut,
	}, nil
}

// Add implements GstChain.
func (stw *SipToWebrtc) Add(p *gst.Pipeline) error {
	if err := p.AddMany(
		stw.RtpBin,

		stw.SipRtpIn,
		// rptbin
		stw.Depay,
		stw.Parse,
		stw.Decoder,
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
		stw.Vp8Enc,
		stw.RtpVp8Pay,
		// stw.WebrtcRtpSink,

		stw.SipRtcpIn,
		// rtpbin
		stw.SipRtcpOut,
	); err != nil {
		return fmt.Errorf("failed to add sip to webrtc chain to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (stw *SipToWebrtc) Link(p *gst.Pipeline) error {
	// link rtp path
	if err := pipeline.LinkPad(
		stw.SipRtpIn.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtp src to rtpbin: %w", err)
	}

	if _, err := stw.RtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		stw.log.Debugw("RTPBIN PAD ADDED", "pad", pad.GetName())
		padName := pad.GetName()
		if !strings.HasPrefix(padName, "recv_rtp_src_") {
			return
		}
		var ssrc, payloadType uint32
		if _, err := fmt.Sscanf(padName, "recv_rtp_src_0_%d_%d", &ssrc, &payloadType); err != nil {
			stw.log.Warnw("Invalid RTP pad format", err, "pad", padName)
			return
		}
		stw.log.Infow("RTP pad added", "pad", padName, "ssrc", ssrc, "payloadType", payloadType)
		if err := pipeline.LinkPad(
			pad,
			stw.Depay.GetStaticPad("sink"),
		); err != nil {
			stw.log.Errorw("Failed to link rtpbin pad to depayloader", err)
			return
		}
		stw.log.Infow("Linked RTP pad", "pad", padName)
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if err := gst.ElementLinkMany(
		stw.Depay,
		stw.Parse,
		stw.Decoder,
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
		stw.Vp8Enc,
		stw.RtpVp8Pay,
		// stw.WebrtcRtpSink,
	); err != nil {
		return fmt.Errorf("failed to link sip to webrtc rtp path: %w", err)
	}

	// link rtcp path
	if err := pipeline.LinkPad(
		stw.SipRtcpIn.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtcp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtcp src to rtpbin: %w", err)
	}

	if err := pipeline.LinkPad(
		stw.RtpBin.GetRequestPad("send_rtcp_src_0"),
		stw.SipRtcpOut.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link rtpbin rtcp src to sip rtcp sink: %w", err)
	}

	return nil
}

func (stw *SipToWebrtc) WriteRtpTo(p *gst.Pipeline, w io.Writer) error {
	if stw.WebrtcRtpOut != nil {
		return fmt.Errorf("webrtc rtp writer already set")
	}

	handle := cgo.NewHandle(w)
	defer handle.Delete()

	rtpWriter, err := gst.NewElementWithProperties("sinkwriter", map[string]interface{}{
		"handle": uint64(uintptr(handle)),
		"caps": gst.NewCapsFromString(
			VP8CAPS,
		),
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc rtp writer: %w", err)
	}

	stw.WebrtcRtpOut = rtpWriter

	if err := p.Add(stw.WebrtcRtpOut); err != nil {
		return fmt.Errorf("failed to add webrtc rtp writer to pipeline: %w", err)
	}

	if err := stw.RtpVp8Pay.Link(rtpWriter); err != nil {
		return fmt.Errorf("failed to link rtp vp8 payloader to webrtc rtp writer: %w", err)
	}

	if !stw.WebrtcRtpOut.SyncStateWithParent() {
		return fmt.Errorf("failed to sync webrtc rtp writer state with parent")
	}

	return nil
}

// Close implements GstChain.
func (stw *SipToWebrtc) Close(pipeline *gst.Pipeline) error {
	if err := pipeline.RemoveMany(
		stw.RtpBin,

		stw.SipRtpIn,
		// rptbin
		stw.Depay,
		stw.Parse,
		stw.Decoder,
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
		stw.Vp8Enc,
		stw.RtpVp8Pay,

		stw.SipRtcpIn,
		// rtpbin
		stw.SipRtcpOut,
	); err != nil {
		return fmt.Errorf("failed to remove sip to webrtc chain from pipeline: %w", err)
	}
	if stw.WebrtcRtpOut != nil {
		if err := pipeline.Remove(stw.WebrtcRtpOut); err != nil {
			return fmt.Errorf("failed to remove webrtc rtp writer from pipeline: %w", err)
		}
	}
	return nil
}
