package pipeline

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

type SelectorToSip struct {
	InputSelector *gst.Element
	Vp8Dec        *gst.Element
	VideoConvert  *gst.Element
	I420Filter    *gst.Element
	X264Enc       *gst.Element
	Parse         *gst.Element
	RtpH264Pay    *gst.Element
	Sink          *gst.Element

	AppSink *app.Sink
}

func buildWebRTCToSipChain(sipOutPayloadType int) (*SelectorToSip, []*gst.Element, error) {

	inputSelector, err := gst.NewElementWithProperties("input-selector", map[string]interface{}{
		"name":         "webrtc_rtp_sel",
		"sync-streams": true,
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
		inputSelector, vp8dec, vconv, i420Filter, x264enc, parse, rtpPay, sink,
	}

	return &SelectorToSip{
		InputSelector: inputSelector,
		Vp8Dec:        vp8dec,
		VideoConvert:  vconv,
		I420Filter:    i420Filter,
		X264Enc:       x264enc,
		Parse:         parse,
		RtpH264Pay:    rtpPay,
		Sink:          sink,
		AppSink:       app.SinkFromElement(sink),
	}, chainElems, nil
}
