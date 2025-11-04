package sip

import (
	"fmt"
	"io"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

const pipelineStr = `
  rtpsession name=sip_session
  rtpsession name=webrtc_session

  appsrc name=sip_rtp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=true
      caps="application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000,packetization-mode=1" !
      sip_session.recv_rtp_sink

  appsrc name=sip_rtcp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
      caps="application/x-rtcp" !
      sip_session.recv_rtcp_sink

  sip_session.recv_rtp_src !
      rtpjitterbuffer latency=10 do-lost=true do-retransmission=false drop-on-latency=true !
      rtph264depay !
      h264parse config-interval=-1 !
      avdec_h264 max-threads=4 !
      videoconvert !
      vp8enc deadline=1 target-bitrate=3000000 cpu-used=5 keyframe-max-dist=60 lag-in-frames=0 threads=4 buffer-initial-size=100 buffer-optimal-size=120 buffer-size=150 !
      rtpvp8pay pt=96 mtu=1200 !
      webrtc_session.send_rtp_sink

  webrtc_session.send_rtp_src !
      appsink name=webrtc_rtp_out emit-signals=false drop=false max-buffers=30 sync=false

  webrtc_session.send_rtcp_src !
      appsink name=webrtc_rtcp_out emit-signals=false drop=false max-buffers=10 sync=false

  appsrc name=webrtc_rtp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
      caps="application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96" !
      webrtc_session.recv_rtp_sink

  appsrc name=webrtc_rtcp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
      caps="application/x-rtcp" !
      webrtc_session.recv_rtcp_sink

  webrtc_session.recv_rtp_src !
      rtpjitterbuffer latency=10 do-lost=true do-retransmission=false drop-on-latency=true !
      rtpvp8depay !
      vp8dec !
      videoconvert !
      video/x-raw,format=I420 !
      x264enc bitrate=2000 key-int-max=60 bframes=0 rc-lookahead=0 sliced-threads=true sync-lookahead=0 !
      h264parse config-interval=-1 !
      rtph264pay pt=%d mtu=1200 config-interval=-1 aggregate-mode=zero-latency !
      sip_session.send_rtp_sink

  sip_session.send_rtp_src !
      appsink name=sip_rtp_out emit-signals=false drop=false max-buffers=30 sync=false

  sip_session.send_rtcp_src !
      appsink name=sip_rtcp_out emit-signals=false drop=false max-buffers=10 sync=false
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

func (v *VideoManager) SetupGstPipeline() error {
	pstr := fmt.Sprintf(pipelineStr, v.media.Codec.PayloadType, v.media.Codec.PayloadType)

	pipeline, err := gst.NewPipelineFromString(pstr)
	if err != nil {
		return fmt.Errorf("failed to create GStreamer pipeline: %w\n%s", err, pstr)
	}

	sipRtpIn, err := writerFromPipeline(pipeline, "sip_rtp_in")
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP reader: %w", err)
	}
	go io.Copy(sipRtpIn, v.sipRtpIn)

	sipRtpOut, err := readerFromPipeline(pipeline, "sip_rtp_out")
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP writer: %w", err)
	}
	go io.Copy(v.sipRtpOut, sipRtpOut)

	sipRtcpIn, err := writerFromPipeline(pipeline, "sip_rtcp_in")
	if err != nil {
		return fmt.Errorf("failed to create SIP RTCP reader: %w", err)
	}
	go io.Copy(sipRtcpIn, v.sipRtcpIn)

	sipRtcpOut, err := readerFromPipeline(pipeline, "sip_rtcp_out")
	if err != nil {
		return fmt.Errorf("failed to create SIP RTCP writer: %w", err)
	}
	go io.Copy(v.sipRtcpOut, sipRtcpOut)

	webrtcRtpIn, err := writerFromPipeline(pipeline, "webrtc_rtp_in")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP reader: %w", err)
	}
	go io.Copy(webrtcRtpIn, v.webrtcRtpIn)

	webrtcRtpOut, err := readerFromPipeline(pipeline, "webrtc_rtp_out")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP writer: %w", err)
	}
	go io.Copy(v.webrtcRtpOut, webrtcRtpOut)

	webrtcRtcpIn, err := writerFromPipeline(pipeline, "webrtc_rtcp_in")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTCP reader: %w", err)
	}
	go io.Copy(webrtcRtcpIn, v.webrtcRtcpIn)

	webrtcRtcpOut, err := readerFromPipeline(pipeline, "webrtc_rtcp_out")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTCP writer: %w", err)
	}
	go io.Copy(v.webrtcRtcpOut, webrtcRtcpOut)

	v.pipeline = pipeline

	return nil
}
