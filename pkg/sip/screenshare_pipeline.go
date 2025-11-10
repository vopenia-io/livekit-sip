package sip

import (
	"fmt"
	"io"
	"time"

	"github.com/go-gst/go-gst/gst"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/pion/rtcp"
)

const screensharePipelineStr = `
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

func (s *ScreenShareManager) SetupGstPipeline(media *sdpv2.SDPMedia) error {
	// Note: packetization-mode and profile-level-id are SDP/FMTP parameters,
	// not RTP caps parameters. GStreamer's rtph264depay will handle them automatically
	// from the H.264 stream itself. We only need to specify payload type in the caps.

	pstr := fmt.Sprintf(screensharePipelineStr, media.Codec.PayloadType, media.Codec.PayloadType)

	s.log.Infow("Creating GStreamer pipeline", "payloadType", media.Codec.PayloadType, "codecName", media.Codec.Name)
	s.log.Infow("🔧 Pipeline string:", "pipeline", pstr)

	pipeline, err := gst.NewPipelineFromString(pstr)
	if err != nil {
		return fmt.Errorf("failed to create GStreamer pipeline: %w\n%s", err, pstr)
	}

	// Add bus message handler for detailed debugging
	bus := pipeline.GetPipelineBus()
	bus.AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageError:
			gerr := msg.ParseError()
			s.log.Errorw("❌ GStreamer ERROR", gerr,
				"debug", gerr.DebugString(),
				"source", msg.Source())
		case gst.MessageWarning:
			gerr := msg.ParseWarning()
			s.log.Warnw("⚠️ GStreamer WARNING", gerr,
				"debug", gerr.DebugString(),
				"source", msg.Source())
		case gst.MessageStateChanged:
			old, new := msg.ParseStateChanged()
			if msg.Source() == pipeline.GetName() {
				s.log.Infow("🔄 Pipeline state changed",
					"old", old,
					"new", new)
			}
		case gst.MessageStreamStatus:
			status, owner := msg.ParseStreamStatus()
			s.log.Debugw("📊 Stream status",
				"status", status,
				"owner", owner)
		case gst.MessageAsyncDone:
			s.log.Infow("✅ Pipeline ASYNC-DONE (state change complete)")
		case gst.MessageLatency:
			s.log.Debugw("⏱️ Latency message received")
		case gst.MessageQoS:
			qos := msg.ParseQoS()
			s.log.Debugw("📉 QOS event",
				"live", qos.Live,
				"running-time", qos.RunningTime,
				"stream-time", qos.StreamTime,
				"timestamp", qos.Timestamp,
				"duration", qos.Duration)
		}
		return true
	})
	s.log.Infow("✅ GStreamer bus watch added for message monitoring")

	sipRtpIn, err := writerFromPipeline(pipeline, "sip_rtp_in")
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP reader: %w", err)
	}
	go Copy(sipRtpIn, s.sipRtpIn)

	sipRtpOut, err := readerFromPipeline(pipeline, "sip_rtp_out")
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP writer: %w", err)
	}
	go Copy(NewDebugWriteCloser(s.sipRtpOut, "SIP RTP OUT", 5*time.Second), NewDebugReadCloser(sipRtpOut, "SIP RTP OUT", 5*time.Second))

	webrtcRtpIn, err := writerFromPipeline(pipeline, "webrtc_rtp_in")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP reader: %w", err)
	}
	go Copy(NewDebugWriteCloser(webrtcRtpIn, "WebRTC RTP IN", 5*time.Second), NewDebugReadCloser(s.webrtcRtpIn, "WebRTC RTP IN", 5*time.Second))

	// Monitor WebRTC->SIP path elements for debugging
	s.monitorElement(pipeline, "webrtc_rtp_in", "WebRTC appsrc")
	s.monitorElement(pipeline, "webrtc_jitterbuffer", "WebRTC jitterbuffer")
	s.monitorElement(pipeline, "rtpvp8depay", "VP8 depayloader")
	s.monitorElement(pipeline, "vp8dec", "VP8 decoder")
	s.monitorElement(pipeline, "x264enc", "H.264 encoder")
	s.monitorElement(pipeline, "rtph264pay", "H.264 payloader")
	s.monitorElement(pipeline, "sip_rtp_out", "SIP appsink")

	webrtcRtpOut, err := readerFromPipeline(pipeline, "webrtc_rtp_out")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP writer: %w", err)
	}
	go Copy(s.webrtcRtpOut, webrtcRtpOut)

	// RTCP monitoring with cross-direction PLI forwarding
	// WebRTC RTCP monitor - forward PLI to SIP side
	webrtcRtcpMonitor := &rtcpMonitor{
		reader:       s.webrtcRtcpIn,
		writer:       s.webrtcRtcpOut,
		pliForwarder: s.sipRtcpOut,
		log:          s.log,
		name:         "WebRTC-IN",
	}
	go Copy(&NopWriteCloser{io.Discard}, io.NopCloser(webrtcRtcpMonitor))

	// SIP RTCP monitor - forward PLI to WebRTC side
	sipRtcpMonitor := &rtcpMonitor{
		reader:       s.sipRtcpIn,
		writer:       s.sipRtcpOut,
		pliForwarder: s.webrtcRtcpOut,
		log:          s.log,
		name:         "SIP-IN",
	}
	go Copy(&NopWriteCloser{io.Discard}, io.NopCloser(sipRtcpMonitor))

	// Monitor jitter buffer for packet loss on WebRTC->SIP path and send PLI to WebRTC
	webrtcJitterBuffer, err := pipeline.GetElementByName("webrtc_jitterbuffer")
	if err == nil {
		webrtcJitterBuffer.Connect("on-npt-stop", func() {
			s.log.Infow("🔴 WebRTC jitter buffer NPT stop - packet loss detected")
			s.sendPLI(s.webrtcRtcpOut, "WebRTC (auto-recovery)")
		})
		s.log.Debugw("Connected WebRTC jitter buffer signals for packet loss detection")
	}

	// Monitor jitter buffer for packet loss on SIP->WebRTC path and send PLI to SIP
	sipJitterBuffer, err := pipeline.GetElementByName("sip_jitterbuffer")
	if err == nil {
		sipJitterBuffer.Connect("on-npt-stop", func() {
			s.log.Infow("🔴 SIP jitter buffer NPT stop - packet loss detected")
			s.sendPLI(s.sipRtcpOut, "SIP (auto-recovery)")
		})
		s.log.Debugw("Connected SIP jitter buffer signals for packet loss detection")
	}

	// Proactive PLI sender - send PLI every 1 second as fallback recovery mechanism
	// This ensures we get fresh keyframes even if automatic detection fails
	// Reduced from 3s to 1s to recover faster from progressive corruption
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.sendPLI(s.webrtcRtcpOut, "WebRTC (periodic)")
			s.sendPLI(s.sipRtcpOut, "SIP (periodic)")
		}
	}()

	s.pipeline = pipeline

	return nil
}

// sendPLI sends a PLI (Picture Loss Indication) packet to request a keyframe
func (s *ScreenShareManager) sendPLI(writer io.Writer, direction string) {
	if writer == nil {
		return
	}

	pli := &rtcp.PictureLossIndication{
		SenderSSRC: 0,
		MediaSSRC:  0,
	}
	pliBytes, err := pli.Marshal()
	if err != nil {
		return
	}
	writer.Write(pliBytes)
}

// monitorElement adds state change monitoring to a pipeline element
func (s *ScreenShareManager) monitorElement(pipeline *gst.Pipeline, elementName, displayName string) {
	element, err := pipeline.GetElementByName(elementName)
	if err != nil {
		s.log.Warnw("⚠️ Could not get element for monitoring", nil,
			"element", elementName,
			"error", err)
		return
	}

	// Monitor state changes
	element.Connect("notify::state", func() {
		state, _ := element.GetState(gst.StateNull, 0)
		s.log.Debugw("🔧 Element state",
			"element", displayName,
			"state", state)
	})

	// Add pad probes to track data flow
	srcPad := element.GetStaticPad("src")
	if srcPad != nil {
		srcPad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			buffer := info.GetBuffer()
			s.log.Debugw("📦 Buffer flowing",
				"element", displayName,
				"pad", "src",
				"size", buffer.GetSize(),
				"pts", buffer.PresentationTimestamp(),
				"dts", buffer.DecodingTimestamp())
			return gst.PadProbeOK
		})
		s.log.Debugw("✅ Added src pad probe", "element", displayName)
	}

	s.log.Debugw("✅ Monitoring element", "element", displayName)
}
