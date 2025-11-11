package sip

import (
	"fmt"
	"io"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/pion/rtcp"
)

// rtcpMonitor wraps an io.Reader to monitor and log RTCP packets
type rtcpMonitor struct {
	reader         io.Reader
	writer         io.Writer
	log            logger.Logger
	name           string
	pliForwarder   io.Writer // Forward PLI/FIR to the opposite direction
	lastPLIForward int64     // Unix timestamp of last PLI forward (rate limiting)
}

func (r *rtcpMonitor) Read(p []byte) (n int, err error) {
	n, err = r.reader.Read(p)
	if err != nil || n == 0 {
		return n, err
	}

	// Try to parse RTCP packets
	packets, parseErr := rtcp.Unmarshal(p[:n])
	if parseErr == nil && len(packets) > 0 {
		var packetsToForward []rtcp.Packet
		needsPLIForward := false

		for _, pkt := range packets {
			switch pkt := pkt.(type) {
			case *rtcp.SenderReport:
				packetsToForward = append(packetsToForward, pkt)
			case *rtcp.ReceiverReport:
				packetsToForward = append(packetsToForward, pkt)
			case *rtcp.PictureLossIndication:
				// Forward PLI to opposite direction to request keyframe
				needsPLIForward = true
			case *rtcp.FullIntraRequest:
				// Forward FIR to opposite direction to request keyframe
				needsPLIForward = true
			case *rtcp.TransportLayerNack:
				// Don't forward NACKs as retransmission is disabled
			case *rtcp.ReceiverEstimatedMaximumBitrate:
				packetsToForward = append(packetsToForward, pkt)
			default:
				packetsToForward = append(packetsToForward, pkt)
			}
		}

		// Forward PLI to opposite direction if needed
		if needsPLIForward && r.pliForwarder != nil {
			// Create a PLI and send it to the opposite side
			pli := &rtcp.PictureLossIndication{
				SenderSSRC: 0,
				MediaSSRC:  0,
			}
			pliBytes, err := pli.Marshal()
			if err == nil {
				r.pliForwarder.Write(pliBytes)
			}
		}

		// Write filtered packets to destination
		if r.writer != nil && len(packetsToForward) > 0 {
			forwardBytes, err := rtcp.Marshal(packetsToForward)
			if err == nil {
				r.writer.Write(forwardBytes)
			}
		}
	} else if r.writer != nil {
		// If we can't parse, just forward as-is
		r.writer.Write(p[:n])
	}

	return n, err
}

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
	CopyLabeled(dst, src, "")
}

func CopyLabeled(dst io.WriteCloser, src io.ReadCloser, label string) {
	prefix := ""
	if label != "" {
		prefix = fmt.Sprintf("[%s] ", label)
	}
	fmt.Printf("📋 %sCopy started: src=%T dst=%T\n", prefix, src, dst)
	buf := make([]byte, 32768) // 32KB buffer for efficiency
	totalBytes := 0
	totalWrites := 0

	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			totalBytes += n
			totalWrites++
			if totalWrites%500 == 0 {
				fmt.Printf("📋 %sCopy progress: %d writes, %d total bytes (src=%T, dst=%T)\n", prefix, totalWrites, totalBytes, src, dst)
			}

			_, writeErr := dst.Write(buf[:n])
			if writeErr != nil {
				fmt.Printf("❌ %sCopy write error after %d bytes: %v (src=%T, dst=%T)\n", prefix, totalBytes, writeErr, src, dst)
				break
			}
		}

		if readErr != nil {
			if readErr != io.EOF {
				fmt.Printf("❌ %sCopy read error after %d bytes: %v (src=%T, dst=%T)\n", prefix, totalBytes, readErr, src, dst)
			} else {
				fmt.Printf("✅ %sCopy completed: %d bytes in %d writes (src=%T, dst=%T)\n", prefix, totalBytes, totalWrites, src, dst)
			}
			break
		}
	}

	src.Close()
	dst.Close()
}

func (v *VideoManager) SetupGstPipeline(media *sdpv2.SDPMedia) error {
	// Note: packetization-mode and profile-level-id are SDP/FMTP parameters,
	// not RTP caps parameters. GStreamer's rtph264depay will handle them automatically
	// from the H.264 stream itself. We only need to specify payload type in the caps.

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
	go CopyLabeled(sipRtpIn, v.sipRtpIn, v.label)

	sipRtpOut, err := readerFromPipeline(pipeline, "sip_rtp_out")
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP writer: %w", err)
	}
	go CopyLabeled(v.sipRtpOut, sipRtpOut, v.label)

	webrtcRtpIn, err := writerFromPipeline(pipeline, "webrtc_rtp_in")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP reader: %w", err)
	}
	go CopyLabeled(webrtcRtpIn, v.webrtcRtpIn, v.label)

	webrtcRtpOut, err := readerFromPipeline(pipeline, "webrtc_rtp_out")
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP writer: %w", err)
	}
	go CopyLabeled(v.webrtcRtpOut, webrtcRtpOut, v.label)

	// RTCP monitoring with cross-direction PLI forwarding
	// WebRTC RTCP monitor - forward PLI to SIP side
	webrtcRtcpMonitor := &rtcpMonitor{
		reader:       v.webrtcRtcpIn,
		writer:       v.webrtcRtcpOut,
		pliForwarder: v.sipRtcpOut,
		log:          v.log,
		name:         "WebRTC-IN",
	}
	go Copy(&NopWriteCloser{io.Discard}, io.NopCloser(webrtcRtcpMonitor))

	// SIP RTCP monitor - forward PLI to WebRTC side
	sipRtcpMonitor := &rtcpMonitor{
		reader:       v.sipRtcpIn,
		writer:       v.sipRtcpOut,
		pliForwarder: v.webrtcRtcpOut,
		log:          v.log,
		name:         "SIP-IN",
	}
	go Copy(&NopWriteCloser{io.Discard}, io.NopCloser(sipRtcpMonitor))

	// Monitor jitter buffer for packet loss on WebRTC->SIP path and send PLI to WebRTC
	webrtcJitterBuffer, err := pipeline.GetElementByName("webrtc_jitterbuffer")
	if err == nil {
		webrtcJitterBuffer.Connect("on-npt-stop", func() {
			v.log.Infow("🔴 WebRTC jitter buffer NPT stop - packet loss detected")
			v.sendPLI(v.webrtcRtcpOut, "WebRTC (auto-recovery)")
		})
		v.log.Debugw("Connected WebRTC jitter buffer signals for packet loss detection")
	}

	// Monitor jitter buffer for packet loss on SIP->WebRTC path and send PLI to SIP
	sipJitterBuffer, err := pipeline.GetElementByName("sip_jitterbuffer")
	if err == nil {
		sipJitterBuffer.Connect("on-npt-stop", func() {
			v.log.Infow("🔴 SIP jitter buffer NPT stop - packet loss detected")
			v.sendPLI(v.sipRtcpOut, "SIP (auto-recovery)")
		})
		v.log.Debugw("Connected SIP jitter buffer signals for packet loss detection")
	}

	// Proactive PLI sender - send PLI every 1 second as fallback recovery mechanism
	// This ensures we get fresh keyframes even if automatic detection fails
	// Reduced from 3s to 1s to recover faster from progressive corruption
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			v.sendPLI(v.webrtcRtcpOut, "WebRTC (periodic)")
			v.sendPLI(v.sipRtcpOut, "SIP (periodic)")
		}
	}()

	v.pipeline = pipeline

	return nil
}

// sendPLI sends a PLI (Picture Loss Indication) packet to request a keyframe
func (v *VideoManager) sendPLI(writer io.Writer, direction string) {
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
