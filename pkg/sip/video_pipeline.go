package sip

import (
	"fmt"
	"io"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
)

const pipelineStr = `
  appsrc name=sip_rtp_in format=3 is-live=true do-timestamp=true max-bytes=5000000 block=false
      caps="application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000" !
      rtpjitterbuffer name=sip_jitterbuffer latency=100 do-lost=true do-retransmission=false drop-on-latency=false !
      rtph264depay request-keyframe=true !
      h264parse config-interval=1 !
      avdec_h264 max-threads=4 !
      videoconvert !
      videoscale add-borders=false !
      videorate !
      vp8enc deadline=1 target-bitrate=3000000 cpu-used=2 keyframe-max-dist=30 lag-in-frames=0 threads=4 buffer-initial-size=100 buffer-optimal-size=120 buffer-size=150 min-quantizer=4 max-quantizer=40 cq-level=13 error-resilient=1 !
      rtpvp8pay pt=96 mtu=1200 picture-id-mode=15-bit !
      appsink name=webrtc_rtp_out emit-signals=false drop=false max-buffers=100 sync=false

  appsrc name=webrtc_rtp_in format=3 is-live=true do-timestamp=true max-bytes=2000000 block=false
      caps="application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96" !
      rtpjitterbuffer name=webrtc_jitterbuffer latency=100 do-lost=true do-retransmission=false drop-on-latency=false !
      rtpvp8depay request-keyframe=true !
      vp8dec !
      videoconvert !
      video/x-raw,format=I420 !
      x264enc bitrate=2000 key-int-max=30 bframes=0 rc-lookahead=0 sliced-threads=true sync-lookahead=0 tune=zerolatency speed-preset=ultrafast !
      h264parse config-interval=1 !
      rtph264pay pt=%d mtu=1200 config-interval=1 aggregate-mode=zero-latency !
      appsink name=sip_rtp_out emit-signals=false drop=false max-buffers=100 sync=false
`

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

func Copy(dst io.WriteCloser, src io.ReadCloser) {
	n, err := io.Copy(dst, src)
	fmt.Printf("Copied %d bytes with err=%v\n", n, err)
	src.Close()
	dst.Close()
}

func (v *VideoManager) SetupGstPipeline(media *sdpv2.SDPMedia) error {
	pstr := fmt.Sprintf(pipelineStr, media.Codec.PayloadType, media.Codec.PayloadType)

	v.log.Infow("Creating GStreamer pipeline", "payloadType", media.Codec.PayloadType, "codecName", media.Codec.Name)

	pipeline, err := gst.NewPipelineFromString(pstr)
	if err != nil {
		return fmt.Errorf("failed to create GStreamer pipeline: %w\n%s", err, pstr)
	}

	sipRtpIn, err := writerFromPipeline(pipeline, "sip_rtp_in")
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP reader: %w", err)
	}
	go Copy(sipRtpIn, v.sipRtpIn)

	sipRtpOut, err := readerFromPipeline(pipeline, "sip_rtp_out")
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP writer: %w", err)
	}
	go Copy(v.sipRtpOut, sipRtpOut)

	webrtcRtpIn, err := writerFromPipeline(pipeline, "webrtc_rtp_in")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP reader: %w", err)
	}
	go Copy(webrtcRtpIn, v.webrtcRtpIn)

	webrtcRtpOut, err := readerFromPipeline(pipeline, "webrtc_rtp_out")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP writer: %w", err)
	}
	go Copy(v.webrtcRtpOut, webrtcRtpOut)

	v.pipeline = pipeline

	return nil
}
