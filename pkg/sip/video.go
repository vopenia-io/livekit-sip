package sip

/*
#cgo pkg-config: gstreamer-1.0
#include <glib.h>
#include <string.h>

static void customLogHandler(const gchar *log_domain,
                             GLogLevelFlags log_level,
                             const gchar *message,
                             gpointer user_data) {
    // Suppress "loop detected in the graph" warnings
    if (log_level == G_LOG_LEVEL_WARNING && strstr(message, "loop detected in the graph") != NULL) {
        return;
    }
    // For other messages, use the default handler
    g_log_default_handler(log_domain, log_level, message, user_data);
}

static void installLogHandler() {
    g_log_set_handler("GStreamer", G_LOG_LEVEL_WARNING | G_LOG_LEVEL_CRITICAL | G_LOG_LEVEL_ERROR, customLogHandler, NULL);
}
*/
import "C"

import (
	"fmt"
	"io"
	"net"
	"net/netip"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	mrtp "github.com/livekit/media-sdk/rtp"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/config"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

var mainLoop *glib.MainLoop

func init() {
	gst.Init(nil)

	// Install a custom GLib log handler to filter out "loop detected" warnings
	// These warnings come from g_warning() in GStreamer core, not the debug log system
	C.installLogHandler()

	mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)
	_ = mainLoop
}

func NewVideoManager(log logger.Logger, room *Room, sdp *sdpv2.SDP, media *sdpv2.SDPMedia, opts *MediaOptions) (*VideoManager, error) {
	rtpConn, err := mrtp.ListenUDPPortRange(opts.Ports.Start, opts.Ports.End, opts.IP)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP port range for RTP: %w", err)
	}
	rtcpConn, err := mrtp.ListenUDPPortRange(opts.Ports.Start, opts.Ports.End, opts.IP)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP port range for RTCP: %w", err)
	}

	v := &VideoManager{
		log:           log,
		room:          room,
		opts:          opts,
		media:         media,
		rtpConn:       newUDPConn(log.WithComponent("video-rtp"), rtpConn),
		rtcpConn:      newUDPConn(log.WithComponent("video-rtcp"), rtcpConn),
		sipRtpIn:      NewSwitchReader(),
		sipRtpOut:     NewSwitchWriter(),
		sipRtcpIn:     NewSwitchReader(),
		sipRtcpOut:    NewSwitchWriter(),
		webrtcRtpIn:   NewSwitchReader(),
		webrtcRtpOut:  NewSwitchWriter(),
		webrtcRtcpIn:  NewSwitchReader(),
		webrtcRtcpOut: NewSwitchWriter(),
	}

	rtpAddr := netip.AddrPortFrom(sdp.Addr, media.Port)
	v.rtpConn.SetDst(rtpAddr)

	rtcpAddr := netip.AddrPortFrom(sdp.Addr, media.RTCPPort)
	v.rtcpConn.SetDst(rtcpAddr)

	v.log.Infow("video manager created", "rtpLocal", v.rtpConn.LocalAddr(), "rtpRemote", rtpAddr, "rtcpLocal", v.rtcpConn.LocalAddr(), "rtcpRemote", rtcpAddr)

	return v, nil
}

type VideoManager struct {
	log           logger.Logger
	opts          *MediaOptions
	media         *sdpv2.SDPMedia
	room          *Room
	rtpConn       *udpConn
	rtcpConn      *udpConn
	pipeline      *gst.Pipeline
	sipRtpIn      *SwitchReader
	sipRtpOut     *SwitchWriter
	sipRtcpIn     *SwitchReader
	sipRtcpOut    *SwitchWriter
	webrtcRtpIn   *SwitchReader
	webrtcRtpOut  *SwitchWriter
	webrtcRtcpIn  *SwitchReader
	webrtcRtcpOut *SwitchWriter
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
	if err := v.rtpConn.Close(); err != nil {
		return fmt.Errorf("failed to close UDP connection: %w", err)
	}
	return nil
}

// func (v *VideoManager) SetSipRtpIn(r io.ReadCloser) {
// 	v.sipRtpIn.Swap(r)
// }

// func (v *VideoManager) SetSipRtpOut(w io.WriteCloser) {
// 	v.sipRtpOut.Swap(w)
// }

func (v *VideoManager) SetWebrtcRtpIn(r io.ReadCloser) {
	v.webrtcRtpIn.Swap(r)
}

func (v *VideoManager) SetWebrtcRtpOut(w io.WriteCloser) {
	v.webrtcRtpOut.Swap(w)
}

func (v *VideoManager) SetWebrtcRtcpIn(r io.ReadCloser) {
	v.webrtcRtcpIn.Swap(r)
}

func (v *VideoManager) SetWebrtcRtcpOut(w io.WriteCloser) {
	v.webrtcRtcpOut.Swap(w)
}

func (v *VideoManager) Setup() error {
	v.log.Debugw("starting video manager")

	if err := v.SetupGstPipeline(); err != nil {
		return fmt.Errorf("failed to setup GStreamer pipeline: %w", err)
	}

	// track, err := v.room.NewParticipantVideoTrack()
	// if err != nil {
	// 	return fmt.Errorf("failed to create video track: %w", err)
	// }

	v.sipRtpIn.Swap(v.rtpConn)
	v.sipRtpOut.Swap(v.rtpConn)
	v.sipRtcpIn.Swap(v.rtcpConn)
	v.sipRtcpOut.Swap(v.rtcpConn)

	// v.webrtcRtpOut.Swap(&NopWriteCloser{(io.Discard)})

	webrtcRtcpInPipeIn, webrtcRtcpInPipeOut := io.Pipe()
	v.webrtcRtcpIn.Swap(webrtcRtcpInPipeIn)

	v.room.AddVideoTrackCallback("*",
		func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) {
			v.log.Infow("adding video track from participant", "participant", rp.Identity(), "trackID", track.ID())

			v.webrtcRtpIn.Swap(io.NopCloser(
				&TrackAdapter{TrackRemote: track},
			))
			pub.OnRTCP(func(pkt rtcp.Packet) {
				var buf []byte
				b, err := pkt.Marshal()
				if err != nil {
					v.log.Errorw("failed to marshal rtcp packet", err)
					return
				}
				buf = append(buf, b...)
				// fmt.Printf("Writing %d bytes to webrtcRtcpIn pipe\n", len(buf))
				_, err = webrtcRtcpInPipeOut.Write(buf)
				if err != nil {
					v.log.Errorw("failed to write rtcp packet to pipe", err)
				}
			})
		})

	return nil
}

func (v *VideoManager) Start() error {
	v.log.Debugw("starting video manager")

	if err := v.pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to playing: %w", err)
	}
	return nil
}
