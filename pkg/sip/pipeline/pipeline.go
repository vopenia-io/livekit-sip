package pipeline

import (
	"fmt"
	"strings"
	"sync"

	"github.com/frostbyte73/core"
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

type WebRTCToSip struct {
	Src          *gst.Element
	JitterBuffer *gst.Element
	Depay        *gst.Element
	Vp8Dec       *gst.Element
	VideoConvert *gst.Element
	I420Filter   *gst.Element
	X264Enc      *gst.Element
	Parse        *gst.Element
	RtpH264Pay   *gst.Element
	Sink         *gst.Element

	AppSrc  *app.Source
	AppSink *app.Sink
}

type GstPipeline struct {
	mu          sync.Mutex
	Pipeline    *gst.Pipeline
	SipToWebRTC *SipToWebRTC
	WebRTCToSip *WebRTCToSip
	closed      core.Fuse
}

func (gp *GstPipeline) Close() error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	if gp.closed.IsBroken() {
		return nil
	}
	gp.closed.Break()

	return gp.Pipeline.SetState(gst.StateNull)
}

func (gp *GstPipeline) SetState(state gst.State) error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	return gp.Pipeline.SetState(state)
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

func buildWebRTCToSipChain(sipOutPayloadType int) (*WebRTCToSip, []*gst.Element, error) {
	src, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         "webrtc_rtp_in",
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(3),
		"max-bytes":    uint64(2_000_000),
		"block":        false,
		"caps": gst.NewCapsFromString(
			"application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96",
		),
	})
	if err != nil {
		return nil, nil, err
	}

	jb, err := gst.NewElementWithProperties("rtpjitterbuffer", map[string]interface{}{
		"name":              "webrtc_jitterbuffer",
		"latency":           uint(100),
		"do-lost":           true,
		"do-retransmission": false,
		"drop-on-latency":   false,
	})
	if err != nil {
		return nil, nil, err
	}

	depay, err := gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, nil, err
	}

	vp8dec, err := gst.NewElement("vp8dec")
	if err != nil {
		return nil, nil, err
	}

	vconv, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, nil, err
	}

	// caps filter: video/x-raw,format=I420
	i420Filter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,format=I420"),
	})
	if err != nil {
		return nil, nil, err
	}

	x264enc, err := gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"bitrate":        int(2000),
		"key-int-max":    int(30),
		"bframes":        int(0),
		"rc-lookahead":   int(0),
		"sliced-threads": true,
		"sync-lookahead": int(0),
		"tune":           "zerolatency",
		"speed-preset":   "ultrafast",
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

	rtpPay, err := gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"pt":              int(sipOutPayloadType),
		"mtu":             int(1200),
		"config-interval": int(1),
		"aggregate-mode":  int(0), // zero-latency
	})
	if err != nil {
		return nil, nil, err
	}

	sink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "sip_rtp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
	})
	if err != nil {
		return nil, nil, err
	}

	chainElems := []*gst.Element{
		src, jb, depay, vp8dec, vconv, i420Filter, x264enc, parse, rtpPay, sink,
	}

	return &WebRTCToSip{
		Src:          src,
		JitterBuffer: jb,
		Depay:        depay,
		Vp8Dec:       vp8dec,
		VideoConvert: vconv,
		I420Filter:   i420Filter,
		X264Enc:      x264enc,
		Parse:        parse,
		RtpH264Pay:   rtpPay,
		Sink:         sink,
		AppSrc:       app.SrcFromElement(src),
		AppSink:      app.SinkFromElement(sink),
	}, chainElems, nil
}

func NewSipWebRTCPipeline(sipInPayload, sipOutPayload int) (*GstPipeline, error) {
	// gst.Init(nil) must be called once in your process before this.

	pipeline, err := gst.NewPipeline("")
	if err != nil {
		return nil, err
	}

	sipToWebRTC, sipElems, err := buildSipToWebRTCChain(sipInPayload)
	if err != nil {
		return nil, err
	}

	webrtcToSip, webrtcElems, err := buildWebRTCToSipChain(sipOutPayload)
	if err != nil {
		return nil, err
	}

	allElems := append([]*gst.Element{}, sipElems...)
	allElems = append(allElems, webrtcElems...)

	if err := pipeline.AddMany(allElems...); err != nil {
		return nil, err
	}

	if err := gst.ElementLinkMany(sipElems...); err != nil {
		return nil, err
	}
	if err := gst.ElementLinkMany(webrtcElems...); err != nil {
		return nil, err
	}

	return &GstPipeline{
		Pipeline:    pipeline,
		SipToWebRTC: sipToWebRTC,
		WebRTCToSip: webrtcToSip,
	}, nil
}

func PipelineAsString(p *gst.Pipeline) string {
	elems, err := p.GetElementsRecursive()
	if err != nil {
		return fmt.Sprintf("<error: %v>", err)
	}

	var parts []string
	for i := len(elems) - 1; i >= 0; i-- {
		e := elems[i]
		factory := e.GetFactory()
		fname := "<unknown>"
		if factory != nil {
			fname = factory.GetName()
		}
		parts = append(parts, fname+" name="+e.GetName())
	}
	return strings.Join(parts, " !\n")
}
