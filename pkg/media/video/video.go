package video

import (
	"fmt"
	"io"
	"net"

	"github.com/go-gst/go-gst/gst"
	mrtp "github.com/livekit/media-sdk/rtp"
	sdp "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/config"
	"github.com/livekit/sip/pkg/sip"
	"github.com/pion/webrtc/v4"
)

func NewVideoManager(log logger.Logger, room *sip.Room, sdp sdp.Session, opts *sip.MediaOptions) (*VideoManager, error) {
	conn, err := mrtp.ListenUDPPortRange(opts.Ports.Start, opts.Ports.End, opts.IP)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP port range: %w", err)
	}

	return &VideoManager{
		room: room,
		sdp:  sdp,
		opts: opts,
		conn: conn,
	}, nil
}

type VideoManager struct {
	log  logger.Logger
	opts *sip.MediaOptions
	room *sip.Room
	sdp  sdp.Session
	conn *net.UDPConn

	// runner *GstPipelineRunner
	pipeline *gst.Pipeline

	sipRtpIn     SwitchReader
	sipRtpOut    SwitchWriter
	webrtcRtpIn  SwitchReader
	webrtcRtpOut SwitchWriter
}

func (v *VideoManager) Close() error {
	v.log.Debugw("closing video manager")
	if v.pipeline != nil {
		if err := v.pipeline.SetState(gst.StateNull); err != nil {
			return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
		}
	}
	if err := v.conn.Close(); err != nil {
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

	track, err := v.room.NewParticipantVideoTrack()
	if err != nil {
		return fmt.Errorf("failed to create video track: %w", err)
	}

	trackWriter := &NopWriteCloser{Writer: track}
	v.webrtcRtpOut.Swap(trackWriter)
	v.sipRtpIn.Swap(v.conn)
	v.sipRtpOut.Swap(v.conn)

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
