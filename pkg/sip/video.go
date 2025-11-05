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
)

var mainLoop *glib.MainLoop

func init() {
	gst.Init(nil)

	mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)
	_ = mainLoop
}

func NewVideoManager(log logger.Logger, room *Room, sdp *sdpv2.SDP, media *sdpv2.SDPMedia, opts *MediaOptions) (*VideoManager, error) {
	// Allocate RTP/RTCP port pair according to RFC 3550 (RTCP on RTP+1)
	rtpConn, rtcpConn, err := mrtp.ListenUDPPortPair(opts.Ports.Start, opts.Ports.End, opts.IP)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP port pair for RTP/RTCP: %w", err)
	}

	v := &VideoManager{
		VideoIO: VideoIO{
			sipRtpIn:      NewSwitchReader(),
			sipRtpOut:     NewSwitchWriter(),
			sipRtcpIn:     NewSwitchReader(),
			sipRtcpOut:    NewSwitchWriter(),
			webrtcRtpIn:   NewSwitchReader(),
			webrtcRtpOut:  NewSwitchWriter(),
			webrtcRtcpIn:  NewSwitchReader(),
			webrtcRtcpOut: NewSwitchWriter(),
		},
		log:      log,
		room:     room,
		opts:     opts,
		media:    media,
		rtpConn:  newUDPConn(log.WithComponent("video-rtp"), rtpConn),
		rtcpConn: newUDPConn(log.WithComponent("video-rtcp"), rtcpConn),
	}

	rtpAddr := netip.AddrPortFrom(sdp.Addr, media.Port)
	v.rtpConn.SetDst(rtpAddr)

	rtcpAddr := netip.AddrPortFrom(sdp.Addr, media.RTCPPort)
	v.rtcpConn.SetDst(rtcpAddr)

	v.log.Infow("video manager created", "rtpLocal", v.rtpConn.LocalAddr(), "rtpRemote", rtpAddr, "rtcpLocal", v.rtcpConn.LocalAddr(), "rtcpRemote", rtcpAddr)

	return v, nil
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

type VideoManager struct {
	VideoIO
	log      logger.Logger
	opts     *MediaOptions
	media    *sdpv2.SDPMedia
	room     *Room
	rtpConn  *udpConn
	rtcpConn *udpConn
	pipeline *gst.Pipeline
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

	// // v.webrtcRtpOut.Swap(&NopWriteCloser{(io.Discard)})

	// webrtcRtcpInPipeIn, webrtcRtcpInPipeOut := io.Pipe()
	// v.webrtcRtcpIn.Swap(webrtcRtcpInPipeIn)

	// // Create RTCP output pipe for WebRTC
	// webrtcRtcpOutPipeIn, webrtcRtcpOutPipeOut := io.Pipe()
	// v.webrtcRtcpOut.Swap(webrtcRtcpOutPipeOut)

	v.room.SetTrackCallback(func(ti *TrackInput) {
		if r := v.webrtcRtpIn.Swap(ti.RtpIn); r != nil {
			_ = r.Close()
		}
		if w := v.webrtcRtcpIn.Swap(ti.RtcpIn); w != nil {
			_ = w.Close()
		}
	})

	v.room.UpdateActiveParticipant("")

	// v.room.AddVideoTrackCallback("*",
	// 	func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) {
	// 		v.log.Infow("adding video track from participant", "participant", rp.Identity(), "trackID", track.ID())

	// 		v.webrtcRtpIn.Swap(io.NopCloser(
	// 			&TrackAdapter{TrackRemote: track},
	// 		))

	// 		// Read RTCP from WebRTC incoming
	// 		pub.OnRTCP(func(pkt rtcp.Packet) {
	// 			var buf []byte
	// 			b, err := pkt.Marshal()
	// 			if err != nil {
	// 				return
	// 			}
	// 			buf = append(buf, b...)
	// 			_, err = webrtcRtcpInPipeOut.Write(buf)
	// 			if err != nil {
	// 			}
	// 		})

	// 		// Send RTCP to WebRTC (from our monitor/forwarder)
	// 		// Note: For now just log what we would send - actual sending requires PeerConnection access
	// 		go func() {
	// 			buf := make([]byte, 1500)
	// 			for {
	// 				_, err := webrtcRtcpOutPipeIn.Read(buf)
	// 				if err != nil {
	// 					return
	// 				}
	// 				// Discard RTCP packets - no way to send them to WebRTC track
	// 			}
	// 		}()
	// 	})

	return nil
}

func (v *VideoManager) Start() error {
	v.log.Debugw("starting video manager")

	if err := v.pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to playing: %w", err)
	}
	return nil
}
