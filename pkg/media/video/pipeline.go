package video

import (
	"fmt"
	"io"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

const pipelineStr = ``

func writerFromPipeline(pipeline *gst.Pipeline, name string) (*GstWriter, error) {
	element, err := pipeline.GetElementByName(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get %s element: %w", name, err)
	}
	src := app.SrcFromElement(element)
	writer, err := NewGstWriter(src)
	if err != nil {
		return nil, fmt.Errorf("failed to create GST write RTP for %s: %w", name, err)
	}
	return writer, nil
}

func readerFromPipeline(pipeline *gst.Pipeline, name string) (*GstReader, error) {
	element, err := pipeline.GetElementByName(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get %s element: %w", name, err)
	}
	sink := app.SinkFromElement(element)
	reader, err := NewGstReader(sink)
	if err != nil {
		return nil, fmt.Errorf("failed to create GST read RTP for %s: %w", name, err)
	}
	return reader, nil
}

func (v *VideoManager) SetupGstPipeline() error {
	v.log.Debugw("setting up GStreamer pipeline")

	pstr := pipelineStr

	pipeline, err := gst.NewPipelineFromString(pstr)
	if err != nil {
		return fmt.Errorf("failed to create GStreamer pipeline: %w\n%s", err, pstr)
	}

	sipRtpIn, err := writerFromPipeline(pipeline, "sip-rtp-in")
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP reader: %w", err)
	}
	go io.Copy(sipRtpIn, &v.sipRtpIn)

	sipRtpOut, err := readerFromPipeline(pipeline, "sip-rtp-out")
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP writer: %w", err)
	}
	go io.Copy(&v.sipRtpOut, sipRtpOut)

	webrtcRtpIn, err := writerFromPipeline(pipeline, "webrtc-rtp-in")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP reader: %w", err)
	}
	go io.Copy(webrtcRtpIn, &v.webrtcRtpIn)

	webrtcRtpOut, err := readerFromPipeline(pipeline, "webrtc-rtp-out")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP writer: %w", err)
	}
	go io.Copy(&v.webrtcRtpOut, webrtcRtpOut)

	v.pipeline = pipeline

	return nil
}
