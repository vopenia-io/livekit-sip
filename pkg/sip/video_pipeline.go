package sip

import (
	"fmt"
	"io"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

// const pipelineStr = `
// ( appsrc name=sip_rtp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//       caps="application/x-rtp,media=video,encoding-name=H264,payload=96,clock-rate=90000" !
//       rtpjitterbuffer latency=100 do-lost=true mode=slave !
//       rtph264depay request-keyframe=false wait-for-keyframe=false !
//       h264parse !
//       avdec_h264 max-threads=0 skip-frame=default !
//       videoconvert !
//       vp8enc deadline=1 target-bitrate=2000000 cpu-used=16 keyframe-max-dist=60 noise-sensitivity=2 buffer-size=60 static-threshold=100 !
//       rtpvp8pay pt=96 mtu=1200 !
//       appsink name=webrtc_rtp_out emit-signals=false drop=false max-buffers=0 sync=false
//   )
//   ( appsrc name=webrtc_rtp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//       caps="application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96" !
//       rtpjitterbuffer latency=30 do-lost=true do-retransmission=false drop-on-latency=true !
//       rtpvp8depay !
//       vp8dec !
//       videoconvert !
//       video/x-raw,format=I420 !
//       x264enc bitrate=2000 key-int-max=60 bframes=0 rc-lookahead=0 sliced-threads=true sync-lookahead=0 !
//       h264parse config-interval=-1 !
//       rtph264pay pt=96 mtu=1200 config-interval=-1 aggregate-mode=zero-latency !
//       appsink name=sip_rtp_out emit-signals=false drop=false max-buffers=2 sync=false
//   )
// `

// Optimized pipeline: MINIMUM LATENCY with BEST QUALITY for video conferencing
// Target: 80-120ms glass-to-glass latency
// Path 1: SIP (H.264) -> WebRTC (VP8)
// Path 2: WebRTC (VP8) -> SIP (H.264)
// const pipelineStr = `
//   appsrc name=sip_rtp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//       caps="application/x-rtp,media=video,encoding-name=H264,payload=96,clock-rate=90000,packetization-mode=1" !
//       rtpjitterbuffer name=sip_jitter latency=40 do-lost=true do-retransmission=false drop-on-latency=false mode=1 !
//       rtph264depay request-keyframe=false wait-for-keyframe=false !
//       h264parse config-interval=-1 !
//       avdec_h264 max-threads=4 output-corrupt=false !
//       videoconvert n-threads=4 !
//       video/x-raw,format=I420 !
//       vp8enc
//           deadline=1
//           target-bitrate=2500000
//           end-usage=cbr
//           cpu-used=5
//           keyframe-max-dist=60
//           keyframe-mode=auto
//           lag-in-frames=0
//           threads=4
//           error-resilient=1
//           token-partitions=2
//           static-threshold=0
//           buffer-initial-size=100
//           buffer-optimal-size=120
//           buffer-size=150
//           max-intra-bitrate=500
//           undershoot=95
//           overshoot=105
//           resize-allowed=false
//           sharpness=1
//           noise-sensitivity=0
//           min-quantizer=4
//           max-quantizer=40 !
//       rtpvp8pay pt=%d mtu=1200 picture-id-mode=1 !
//       appsink name=webrtc_rtp_out emit-signals=false drop=false max-buffers=3 sync=false

//   appsrc name=webrtc_rtp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//       caps="application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96" !
//       rtpjitterbuffer name=webrtc_jitter latency=40 do-lost=true do-retransmission=false drop-on-latency=false mode=1 !
//       rtpvp8depay request-keyframe=true wait-for-keyframe=false !
//       vp8dec threads=4 post-processing=false post-processing-flags=0 !
//       videoconvert n-threads=4 !
//       video/x-raw,format=I420 !
//       x264enc
//           bitrate=2500
//           vbv-buf-capacity=2500
//           speed-preset=faster
//           tune=zerolatency
//           key-int-max=60
//           bframes=0
//           b-adapt=false
//           rc-lookahead=0
//           sliced-threads=true
//           sync-lookahead=0
//           aud=true
//           byte-stream=true
//           cabac=true
//           dct8x8=true
//           ref=1
//           me=hex
//           subme=2
//           trellis=0
//           weightb=false
//           qp-min=10
//           qp-max=40
//           qp-step=4
//           pass=cbr
//           option-string="scenecut=0:bframes=0:force-cfr=1:nal-hrd=cbr:filler=1:intra-refresh=1:min-keyint=30" !
//       h264parse config-interval=-1 !
//       rtph264pay pt=%d mtu=1200 config-interval=-1 aggregate-mode=zero-latency !
//       appsink name=sip_rtp_out emit-signals=false drop=false max-buffers=3 sync=false

//   appsrc name=sip_rtcp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//       caps="application/x-rtcp" !
//       appsink name=sip_rtcp_out emit-signals=false drop=false max-buffers=2 sync=false

//   appsrc name=webrtc_rtcp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//       caps="application/x-rtcp" !
//       appsink name=webrtc_rtcp_out emit-signals=false drop=false max-buffers=2 sync=false
// `

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
      vp8enc deadline=1 target-bitrate=2000000 cpu-used=8 keyframe-max-dist=60 lag-in-frames=0 threads=4 buffer-initial-size=100 buffer-optimal-size=120 buffer-size=150 !
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
	v.log.Debugw("setting up GStreamer pipeline")

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
