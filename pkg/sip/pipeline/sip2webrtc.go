package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

type SipToWebRTC struct {
	// raw elements
	Src          *gst.Element
	JitterBuffer *gst.Element
	Depay        *gst.Element
	Parse        *gst.Element
	Decoder      *gst.Element
	VideoConvert *gst.Element
	VideoScale   *gst.Element
	VideoRate    *gst.Element
	Vp8Enc       *gst.Element
	RtpVp8Pay    *gst.Element
	Sink         *gst.Element

	// convenience typed wrappers
	AppSrc  *app.Source
	AppSink *app.Sink
}

func buildSipToWebRTCChain(sipPayloadType int) (*SipToWebRTC, []*gst.Element, error) {
	// appsrc with most settings done as properties
	capsStr := fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
		sipPayloadType,
	)

	src, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         "sip_rtp_in",
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(3), // GST_FORMAT_TIME; using the same numeric value as your launch string
		"max-bytes":    uint64(5_000_000),
		"block":        false,
		"caps":         gst.NewCapsFromString(capsStr),
	})
	if err != nil {
		return nil, nil, err
	}

	jb, err := gst.NewElementWithProperties("rtpjitterbuffer", map[string]interface{}{
		"name":              "sip_jitterbuffer",
		"latency":           uint(100),
		"do-lost":           true,
		"do-retransmission": false,
		"drop-on-latency":   false,
	})
	if err != nil {
		return nil, nil, err
	}

	depay, err := gst.NewElementWithProperties("rtph264depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, nil, err
	}

	parse, err := gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		return nil, nil, err
	}

	dec, err := gst.NewElementWithProperties("avdec_h264", map[string]interface{}{
		"max-threads": int(4),
	})
	if err != nil {
		return nil, nil, err
	}

	vconv, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, nil, err
	}

	vscale, err := gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": false,
	})
	if err != nil {
		return nil, nil, err
	}

	vrate, err := gst.NewElement("videorate")
	if err != nil {
		return nil, nil, err
	}

	vp8enc, err := gst.NewElementWithProperties("vp8enc", map[string]interface{}{
		"deadline":            int(1),
		"target-bitrate":      int(3_000_000),
		"cpu-used":            int(2),
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
		return nil, nil, err
	}

	rtpVp8Pay, err := gst.NewElementWithProperties("rtpvp8pay", map[string]interface{}{
		"pt":              int(96),
		"mtu":             int(1200),
		"picture-id-mode": int(2), // 15-bit in your launch string
	})
	if err != nil {
		return nil, nil, err
	}

	sink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "webrtc_rtp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
	})
	if err != nil {
		return nil, nil, err
	}

	chainElems := []*gst.Element{
		src, jb, depay, parse, dec, vconv, vscale, vrate, vp8enc, rtpVp8Pay, sink,
	}

	return &SipToWebRTC{
		Src:          src,
		JitterBuffer: jb,
		Depay:        depay,
		Parse:        parse,
		Decoder:      dec,
		VideoConvert: vconv,
		VideoScale:   vscale,
		VideoRate:    vrate,
		Vp8Enc:       vp8enc,
		RtpVp8Pay:    rtpVp8Pay,
		Sink:         sink,
		AppSrc:       app.SrcFromElement(src),
		AppSink:      app.SinkFromElement(sink),
	}, chainElems, nil
}
