package sip

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"

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
	remote   netip.Addr
	opts     *MediaOptions
	room     *Room
	rtpConn  *udpConn
	rtcpConn *udpConn
	pipeline *gst.Pipeline
	recv     bool
	send     bool
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
	if !v.recv {
		return v.room.StopVideo()
	}
	to, err := v.room.StartVideo()
	if err != nil {
		return err
	}
	v.SetWebrtcRtpOut(to.RtpOut)
	v.SetWebrtcRtcpOut(to.RtcpOut)
	return nil
}

func (v *VideoManager) Setup(media *sdpv2.SDPMedia) error {
	v.log.Debugw("starting video manager")

	if v.pipeline != nil {
		v.pipeline.SetState(gst.StateNull)
		v.pipeline = nil
		if err := v.VideoIO.Close(); err != nil {
			v.log.Errorw("failed to close previous video IO", err)
		}
		v.VideoIO = NewVideoIO()
	}

	if err := v.SetupGstPipeline(media); err != nil {
		return fmt.Errorf("failed to setup GStreamer pipeline: %w", err)
	}

	v.recv = media.Direction.IsRecv()
	v.send = media.Direction.IsSend()

	v.log.Infow("video setup", "send", v.send, "recv", v.recv, "remote", v.remote.String(), "rtp_port", v.RtpPort(), "rtcp_port", v.RtcpPort())

	if v.send {
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
		if !v.recv {
			if r := v.sipRtpIn.Swap(nil); r != nil {
				_ = r.Close()
			}
			if w := v.sipRtcpIn.Swap(nil); w != nil {
				_ = w.Close()
			}
			v.room.SetTrackCallback(nil)
		}
	}

	if v.recv {
		if r := v.sipRtpIn.Swap(v.rtpConn); r != nil {
			_ = r.Close()
		}
		if w := v.sipRtcpIn.Swap(v.rtcpConn); w != nil {
			_ = w.Close()
		}

		v.room.SetTrackCallback(func(ti *TrackInput) {
			fmt.Printf("\n\n\n\n\n\n\n\n\nCALLBACK\n\n\n\n\n")


			if r := v.webrtcRtpIn.Swap(ti.RtpIn); r != nil {
				_ = r.Close()
			}
			if w := v.webrtcRtcpIn.Swap(ti.RtcpIn); w != nil {
				_ = w.Close()
			}
		})

		v.room.UpdateActiveParticipant("")

		if !v.send {
			if w := v.sipRtpOut.Swap(nil); w != nil {
				_ = w.Close()
			}
			if w := v.sipRtcpOut.Swap(nil); w != nil {
				_ = w.Close()
			}
		}
	}

	return nil
}

func (v *VideoManager) Start() error {
	v.log.Debugw("starting video manager")

	if err := v.pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to playing: %w", err)
	}
	return nil
}
