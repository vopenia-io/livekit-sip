package sip

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
	"github.com/pion/webrtc/v4"
)

var mainLoop *glib.MainLoop

func init() {
	gst.Init(nil)
	mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)
	_ = mainLoop
}

func NewVideoManager(log logger.Logger, room *Room, sdp *sdpv2.Session, media *sdpv2.MediaSection, opts *MediaOptions) (*VideoManager, error) {
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

	return v, nil
}

type VideoManager struct {
	log           logger.Logger
	opts          *MediaOptions
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

func (v *VideoManager) SetSipRtpIn(r io.ReadCloser) {
	v.sipRtpIn.Swap(r)
}

func (v *VideoManager) SetSipRtpOut(w io.WriteCloser) {
	v.sipRtpOut.Swap(w)
}

func (v *VideoManager) SetWebrtcRtpIn(r io.ReadCloser) {
	v.webrtcRtpIn.Swap(r)
}

func (v *VideoManager) SetWebrtcRtpOut(w io.WriteCloser) {
	v.webrtcRtpOut.Swap(w)
}

type TrackAdapter struct{ *webrtc.TrackRemote }

func (t *TrackAdapter) Read(p []byte) (n int, err error) {
	n, _, err = t.TrackRemote.Read(p)
	return n, err
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

	v.webrtcRtpOut.Swap(&NopWriteCloser{(io.Discard)})

	v.room.AddVideoTrackCallback("*",
		func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) {
			v.log.Infow("adding video track from participant", "participant", rp.Identity(), "trackID", track.ID())

			v.webrtcRtpIn.Swap(io.NopCloser(
				&TrackAdapter{TrackRemote: track},
			))
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
