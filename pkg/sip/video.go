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

var mainLoop *glib.MainLoop

func init() {
	gst.Init(nil)

	mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)
	_ = mainLoop
}

type VideoStatus int

const (
	VideoStatusClosed VideoStatus = iota
	VideoStatusStopped
	VideoStatusReady
	VideoStatusStarted
)

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
		status:   VideoStatusStopped,
	}

	v.log.Infow("video manager created")

	return v, nil
}

type VideoManager struct {
	*VideoIO
	mu       sync.Mutex
	log      logger.Logger
	remote   netip.Addr
	opts     *MediaOptions
	room     *Room
	rtpConn  *udpConn
	rtcpConn *udpConn
	pipeline *gst.Pipeline
	recv     bool
	send     bool
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

func (v *VideoManager) OnWebrtcTrack(ti *TrackInput) {
	v.log.Infow("WebRTC video track subscribed - connecting WebRTC→SIP pipeline",
		"hasRtpIn", ti.RtpIn != nil,
		"hasRtcpIn", ti.RtcpIn != nil)

	if r := v.webrtcRtpIn.Swap(ti.RtpIn); r != nil {
		_ = r.Close()
	}
	if w := v.webrtcRtcpIn.Swap(ti.RtcpIn); w != nil {
		_ = w.Close()
	}
}

func (v *VideoManager) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.status = VideoStatusClosed
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

// func (v *VideoIO) SetWebrtcRtpIn(r io.ReadCloser) {
// 	v.webrtcRtpIn.Swap(r)
// }

func (v *VideoIO) SetWebrtcRtpOut(w io.WriteCloser) {
	v.webrtcRtpOut.Swap(w)
}

// func (v *VideoIO) SetWebrtcRtcpIn(r io.ReadCloser) {
// 	v.webrtcRtcpIn.Swap(r)
// }

func (v *VideoIO) SetWebrtcRtcpOut(w io.WriteCloser) {
	v.webrtcRtcpOut.Swap(w)
}

func (v *VideoManager) Status() VideoStatus {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.status
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

func (v *VideoManager) setupOutput(media *sdpv2.SDPMedia, send bool) error {
	if !v.send {
		if w := v.sipRtpOut.Swap(nil); w != nil {
			_ = w.Close()
		}
		if w := v.sipRtcpOut.Swap(nil); w != nil {
			_ = w.Close()
		}

		v.rtpConn.SetDst(netip.AddrPort{})
		v.rtcpConn.SetDst(netip.AddrPort{})
		return nil
	}
	rtpAddr := netip.AddrPortFrom(v.remote, media.Port)
	v.rtpConn.SetDst(rtpAddr)

	rtcpAddr := netip.AddrPortFrom(v.remote, media.RTCPPort)
	v.rtcpConn.SetDst(rtcpAddr)

	v.log.Infow("setting up video send to SIP", "remote", v.remote.String(), "port", media.Port, "rtcp_port", media.RTCPPort)
	if w := v.sipRtpOut.Swap(v.rtpConn); w != nil {
		_ = w.Close()
	}
	if w := v.sipRtcpOut.Swap(v.rtcpConn); w != nil {
		_ = w.Close()
	}
	return nil
}

func (v *VideoManager) setupInput(media *sdpv2.SDPMedia, recv bool) error {
	if !recv {
		if r := v.sipRtpIn.Swap(nil); r != nil {
			_ = r.Close()
		}
		if r := v.sipRtcpIn.Swap(nil); r != nil {
			_ = r.Close()
		}
		return nil
	}

	v.log.Infow("setting up video receive from SIP", "remote", v.remote.String(), "port", media.Port, "rtcp_port", media.RTCPPort)
	if r := v.sipRtpIn.Swap(v.rtpConn); r != nil {
		_ = r.Close()
	}
	if r := v.sipRtcpIn.Swap(v.rtcpConn); r != nil {
		_ = r.Close()
	}
	return nil
}

func (v *VideoManager) Setup(media *sdpv2.SDPMedia) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status != VideoStatusStopped {
		return fmt.Errorf("video manager must be stopped to setup, current status: %d", v.status)
	}

	v.log.Debugw("setting up video manager", "media", media)

	if err := v.SetupGstPipeline(media); err != nil {
		return fmt.Errorf("failed to setup GStreamer pipeline: %w", err)
	}

	v.recv = media.Direction.IsRecv()
	v.send = media.Direction.IsSend()

	v.log.Infow("video setup", "send", v.send, "recv", v.recv, "remote", v.remote.String(), "rtp_port", v.RtpPort(), "rtcp_port", v.RtcpPort())

	if err := v.setupOutput(media, v.recv); err != nil {
		return fmt.Errorf("failed to setup video input: %w", err)
	}

	if err := v.setupInput(media, v.send); err != nil {
		return fmt.Errorf("failed to setup video output: %w", err)
	}

	v.status = VideoStatusReady

	return nil
}

func (v *VideoManager) Start() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status != VideoStatusReady {
		return fmt.Errorf("video manager must be ready to start, current status: %d", v.status)
	}

	v.log.Debugw("starting video manager")

	if err := v.pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to playing: %w", err)
	}

	// CRITICAL FIX: Wait for pipeline to actually reach PLAYING state
	// SetState() is asynchronous - it returns immediately but the state transition takes time (~100-150ms)
	// If we send 200 OK before pipeline is ready, early RTP packets cause corruption (green artifacts)
	v.log.Debugw("waiting for GStreamer pipeline to reach PLAYING state...")
	changeReturn, currentState := v.pipeline.GetState(gst.StatePlaying, gst.ClockTime(5*time.Second))
	if changeReturn == gst.StateChangeFailure {
		return fmt.Errorf("GStreamer pipeline failed to reach PLAYING state")
	}

	v.log.Infow("GStreamer pipeline is PLAYING and ready for RTP packets",
		"current_state", currentState.String(),
		"change_result", changeReturn.String())

	v.status = VideoStatusStarted
	return nil
}

func (v *VideoManager) Stop() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status == VideoStatusStopped {
		return nil
	}

	if v.status != VideoStatusStarted {
		return fmt.Errorf("video manager must be started to stop, current status: %d", v.status)
	}

	v.log.Debugw("stopping video manager")

	if err := v.pipeline.SetState(gst.StateNull); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
	}

	// CRITICAL FIX: Wait for pipeline to actually reach NULL state
	// SetState() is asynchronous - it returns immediately but the state transition takes time (~100-150ms)
	// If we send 200 OK before pipeline is ready, early RTP packets cause corruption (green artifacts)
	v.log.Debugw("waiting for GStreamer pipeline to reach NULL state...")
	changeReturn, currentState := v.pipeline.GetState(gst.StateNull, gst.ClockTime(5*time.Second))
	if changeReturn == gst.StateChangeFailure {
		return fmt.Errorf("GStreamer pipeline failed to reach NULL state")
	}
	v.log.Infow("GStreamer pipeline is NULL",
		"current_state", currentState.String(),
		"change_result", changeReturn.String())

	v.recv = false
	v.send = false

	v.status = VideoStatusStopped
	return nil
}
