package sip

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	mrtp "github.com/livekit/media-sdk/rtp"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
)

// VideoStatus represents the current state of the VideoManager
type VideoStatus int

const (
	VideoStatusStopped VideoStatus = iota
	VideoStatusStarted
)

var mainLoop *glib.MainLoop

func init() {
	gst.Init(nil)

	mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)
	_ = mainLoop
}

func NewVideoManager(log logger.Logger, room *Room, sdp *sdpv2.SDP, opts *MediaOptions) (*VideoManager, error) {
	// Allocate RTP/RTCP port pair according to RFC 3550 (RTCP on RTP+1)
	rtpConn, rtcpConn, err := mrtp.ListenUDPPortPair(opts.Ports.Start, opts.Ports.End, opts.IP)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP port pair for RTP/RTCP: %w", err)
	}

	v := &VideoManager{
		VideoIO:  NewVideoIO(),
		log:      log,
		room:     room,
		opts:     opts,
		remote:   sdp.Addr,
		rtpConn:  newUDPConn(log.WithComponent("video-rtp"), rtpConn),
		rtcpConn: newUDPConn(log.WithComponent("video-rtcp"), rtcpConn),
	}

	v.log.Infow("video manager created")

	return v, nil
}

type VideoManager struct {
	*VideoIO
	log      logger.Logger
	label    string // "camera" or "screenshare" for debugging
	remote   netip.Addr
	opts     *MediaOptions
	room     *Room
	rtpConn  *udpConn
	rtcpConn *udpConn
	pipeline *gst.Pipeline
	recv     bool
	send     bool

	mu       sync.Mutex
	status   VideoStatus
}

type VideoIO struct {
	sipRtpIn   *SwitchReader
	sipRtpOut  *SwitchWriter
	sipRtcpIn  *SwitchReader
	sipRtcpOut *SwitchWriter

	webrtcRtpIn   *SwitchReader
	webrtcRtpOut  *SwitchWriter
	webrtcRtcpIn  *SwitchReader
	webrtcRtcpOut *SwitchWriter
}

func NewVideoIO() *VideoIO {
	return &VideoIO{
		sipRtpIn:      NewSwitchReader(),
		sipRtpOut:     NewSwitchWriter(),
		sipRtcpIn:     NewSwitchReader(),
		sipRtcpOut:    NewSwitchWriter(),
		webrtcRtpIn:   NewSwitchReader(),
		webrtcRtpOut:  NewSwitchWriter(),
		webrtcRtcpIn:  NewSwitchReader(),
		webrtcRtcpOut: NewSwitchWriter(),
	}
}

func (v *VideoIO) Close() error {
	return errors.Join(
		v.sipRtpIn.Close(),
		v.sipRtpOut.Close(),
		v.sipRtcpIn.Close(),
		v.sipRtcpOut.Close(),
		v.webrtcRtpIn.Close(),
		v.webrtcRtpOut.Close(),
		v.webrtcRtcpIn.Close(),
		v.webrtcRtcpOut.Close(),
	)
}

func (v *VideoManager) RtpPort() int {
	return v.rtpConn.LocalAddr().(*net.UDPAddr).Port
}

func (v *VideoManager) RtcpPort() int {
	return v.rtcpConn.LocalAddr().(*net.UDPAddr).Port
}

func (v *VideoManager) Close() error {
	v.log.Debugw("closing video manager")
	if v.pipeline != nil {
		if err := v.pipeline.SetState(gst.StateNull); err != nil {
			return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
		}
	}
	if err := v.VideoIO.Close(); err != nil {
		return fmt.Errorf("failed to close video IO: %w", err)
	}
	if err := v.rtpConn.Close(); err != nil {
		return fmt.Errorf("failed to close RTP connection: %w", err)
	}
	if err := v.rtcpConn.Close(); err != nil {
		return fmt.Errorf("failed to close RTCP connection: %w", err)
	}
	return nil
}

// func (v *VideoManager) SetSipRtpIn(r io.ReadCloser) {
// 	v.sipRtpIn.Swap(r)
// }

// func (v *VideoManager) SetSipRtpOut(w io.WriteCloser) {
// 	v.sipRtpOut.Swap(w)
// }

func (v *VideoIO) SetWebrtcRtpIn(r io.ReadCloser) {
	v.webrtcRtpIn.Swap(r)
}

func (v *VideoIO) SetWebrtcRtpOut(w io.WriteCloser) {
	v.webrtcRtpOut.Swap(w)
}

func (v *VideoIO) SetWebrtcRtcpIn(r io.ReadCloser) {
	v.webrtcRtcpIn.Swap(r)
}

func (v *VideoIO) SetWebrtcRtcpOut(w io.WriteCloser) {
	v.webrtcRtcpOut.Swap(w)
}

func (v *VideoManager) PublishVideoTrack() error {
	// Only start video output if we're receiving from SIP
	if !v.recv {
		return v.room.StopVideo()
	}

	// Start the video track for SIP→WebRTC
	to, err := v.room.StartVideo()
	if err != nil {
		return err
	}
	v.SetWebrtcRtpOut(to.RtpOut)
	v.SetWebrtcRtcpOut(to.RtcpOut)

	v.log.Infow("video track published for SIP→WebRTC", "recv", v.recv, "send", v.send)
	return nil
}

func (v *VideoManager) Setup(media *sdpv2.SDPMedia) error {
	v.log.Debugw("starting video manager")

	if v.pipeline != nil {
		v.log.Infow("stopping existing pipeline for re-setup (re-INVITE)")
		if err := v.pipeline.SetState(gst.StateNull); err != nil {
			v.log.Errorw("failed to stop pipeline", err)
		}
		v.pipeline = nil
		// DON'T close/recreate VideoIO - the Copy() goroutines need the switches
		// to gracefully terminate. New pipeline will reuse existing switches.
	}

	if err := v.SetupGstPipeline(media); err != nil {
		return fmt.Errorf("failed to setup GStreamer pipeline: %w", err)
	}

	v.recv = media.Direction.IsRecv()
	v.send = media.Direction.IsSend()

	v.log.Infow("video setup", "send", v.send, "recv", v.recv, "remote", v.remote.String(), "rtp_port", v.RtpPort(), "rtcp_port", v.RtcpPort())

	if v.send {
		// Set up SIP RTP/RTCP output (our pipeline → SIP device)
		rtpAddr := netip.AddrPortFrom(v.remote, media.Port)
		v.rtpConn.SetDst(rtpAddr)

		rtcpAddr := netip.AddrPortFrom(v.remote, media.RTCPPort)
		v.rtcpConn.SetDst(rtcpAddr)
		if w := v.sipRtpOut.Swap(v.rtpConn); w != nil {
			_ = w.Close()
		}
		if w := v.sipRtcpOut.Swap(v.rtcpConn); w != nil {
			_ = w.Close()
		}

		v.log.Infow("setting up video send to SIP", "remote", v.remote.String(), "port", media.Port)

		// Set up callback for when WebRTC tracks are subscribed (WebRTC → SIP direction)
		// This is needed to get video from WebRTC participants and send to SIP device
		v.room.SetTrackCallback(func(ti *TrackInput) {
			v.log.Infow("WebRTC video track subscribed - connecting WebRTC→SIP pipeline",
				"hasRtpIn", ti.RtpIn != nil,
				"hasRtcpIn", ti.RtcpIn != nil)

			// Wrap RTP input with debug logging to track data flow
			debugRtpIn := NewDebugReadCloser(ti.RtpIn, fmt.Sprintf("[%s] WebRTC→SIP RTP", v.label), 2*time.Second)

			if r := v.webrtcRtpIn.Swap(debugRtpIn); r != nil {
				_ = r.Close()
			}
			if w := v.webrtcRtcpIn.Swap(ti.RtcpIn); w != nil {
				_ = w.Close()
			}
		})

		// Trigger active participant update to invoke callback if tracks already exist
		v.room.UpdateActiveParticipant("")
	} else {
		// If not sending to SIP, disable SIP output
		if w := v.sipRtpOut.Swap(nil); w != nil {
			_ = w.Close()
		}
		if w := v.sipRtcpOut.Swap(nil); w != nil {
			_ = w.Close()
		}
	}

	if v.recv {
		// Set up SIP RTP/RTCP input (SIP device → our pipeline)
		if r := v.sipRtpIn.Swap(v.rtpConn); r != nil {
			_ = r.Close()
		}
		if w := v.sipRtcpIn.Swap(v.rtcpConn); w != nil {
			_ = w.Close()
		}

		v.log.Infow("setting up video receive from SIP", "remote", v.remote.String(), "port", media.Port)
	} else {
		// If not receiving from SIP, disable SIP input
		if r := v.sipRtpIn.Swap(nil); r != nil {
			_ = r.Close()
		}
		if w := v.sipRtcpIn.Swap(nil); w != nil {
			_ = w.Close()
		}
	}

	return nil
}

func (v *VideoManager) Start() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status == VideoStatusStarted {
		v.log.Debugw("video manager already started, skipping")
		return nil
	}

	v.log.Debugw("starting video manager")

	if v.pipeline == nil {
		return fmt.Errorf("cannot start: pipeline not set up")
	}

	// Send EOS to unused pipeline branches for unidirectional streams
	// This allows the pipeline to transition to PLAYING without waiting for data that will never come
	if !v.recv {
		// If we're not receiving from SIP (sendonly), close the SIP→WebRTC branch
		v.log.Infow("Sending EOS to sip_rtp_in (unused for sendonly direction)")
		if sipRtpIn, err := v.pipeline.GetElementByName("sip_rtp_in"); err == nil {
			sipRtpIn.SendEvent(gst.NewEOSEvent())
		}
	}
	if !v.send {
		// If we're not sending to SIP (recvonly), close the WebRTC→SIP branch
		v.log.Infow("Sending EOS to webrtc_rtp_in (unused for recvonly direction)")
		if webrtcRtpIn, err := v.pipeline.GetElementByName("webrtc_rtp_in"); err == nil {
			webrtcRtpIn.SendEvent(gst.NewEOSEvent())
		}
	}

	v.log.Infow("Setting pipeline to PLAYING state", "label", v.label, "send", v.send, "recv", v.recv)

	if err := v.pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to playing: %w", err)
	}

	// CRITICAL FIX: Wait for pipeline to actually reach PLAYING state
	// SetState() is asynchronous - it returns immediately but the state transition takes time
	// For bidirectional pipelines, this can take 100-5000ms depending on data flow
	// If we send 200 OK before pipeline is ready, early RTP packets cause corruption (green artifacts)
	v.log.Infow("Waiting for GStreamer pipeline to reach PLAYING state...", "label", v.label)
	changeReturn, currentState := v.pipeline.GetState(gst.StatePlaying, gst.ClockTime(5*time.Second))

	if changeReturn == gst.StateChangeFailure {
		return fmt.Errorf("GStreamer pipeline failed to reach PLAYING state")
	}

	if changeReturn == gst.StateChangeAsync {
		v.log.Warnw("Pipeline still transitioning to PLAYING asynchronously", nil,
			"current_state", currentState.String(),
			"change_result", changeReturn.String(),
			"note", "Pipeline will reach PLAYING once data flows - this is normal for unidirectional streams")
	} else {
		v.log.Infow("GStreamer pipeline is PLAYING and ready for RTP packets",
			"current_state", currentState.String(),
			"change_result", changeReturn.String())
	}

	v.status = VideoStatusStarted
	return nil
}

// Stop sets the pipeline to NULL state without closing resources
// This allows the pipeline to be restarted later with Start()
func (v *VideoManager) Stop() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status == VideoStatusStopped {
		v.log.Debugw("video manager already stopped, skipping")
		return nil
	}

	v.log.Debugw("stopping video manager")

	if v.pipeline != nil {
		if err := v.pipeline.SetState(gst.StateNull); err != nil {
			return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
		}

		// Wait for pipeline to reach NULL state
		v.log.Debugw("waiting for GStreamer pipeline to reach NULL state...")
		changeReturn, currentState := v.pipeline.GetState(gst.StateNull, gst.ClockTime(100*time.Millisecond))
		if changeReturn == gst.StateChangeFailure {
			return fmt.Errorf("GStreamer pipeline failed to reach NULL state")
		}

		v.log.Infow("GStreamer pipeline is NULL",
			"current_state", currentState.String(),
			"change_result", changeReturn.String())
	}

	v.status = VideoStatusStopped
	return nil
}
