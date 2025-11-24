package sip

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/media-sdk/h264"
	mrtp "github.com/livekit/media-sdk/rtp"
	sdpv1 "github.com/livekit/media-sdk/sdp"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/pion/webrtc/v4"
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

func NewVideoManager(log logger.Logger, room *Room, opts *MediaOptions) (*VideoManager, error) {
	// Allocate RTP/RTCP port pair according to RFC 3550 (RTCP on RTP+1)
	rtpConn, rtcpConn, err := mrtp.ListenUDPPortPair(opts.Ports.Start, opts.Ports.End, opts.IP)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP port pair for RTP/RTCP: %w", err)
	}

	v := &VideoManager{
		VideoIO:      NewVideoIO(),
		log:          log,
		room:         room,
		opts:         opts,
		rtpConn:      newUDPConn(log.WithComponent("video-rtp"), rtpConn),
		rtcpConn:     newUDPConn(log.WithComponent("video-rtcp"), rtcpConn),
		status:       VideoStatusStopped,
		participants: make(map[string]*lksdk.RemoteParticipant),
	}

	v.log.Infow("video manager created")

	return v, nil
}

type VideoManager struct {
	*VideoIO
	mu                 sync.Mutex
	log                logger.Logger
	opts               *MediaOptions
	room               *Room
	rtpConn            *udpConn
	rtcpConn           *udpConn
	pipeline           *pipeline.GstPipeline
	codec              *sdpv2.Codec
	recv               bool
	send               bool
	status             VideoStatus
	participants       map[string]*lksdk.RemoteParticipant // sid -> participant mapping
	lastActiveSpeakers []string                             // circular buffer of last N active speakers (max 6)
}

type VideoIO struct {
	sipRtpIn   *SwitchReader
	sipRtpOut  *SwitchWriter
	sipRtcpIn  *SwitchReader
	sipRtcpOut *SwitchWriter

	webrtcRtpOut  *SwitchWriter
	webrtcRtcpOut *SwitchWriter
}

func NewVideoIO() *VideoIO {
	return &VideoIO{
		sipRtpIn:      NewSwitchReader(),
		sipRtpOut:     NewSwitchWriter(),
		sipRtcpIn:     NewSwitchReader(),
		sipRtcpOut:    NewSwitchWriter(),
		webrtcRtpOut:  NewSwitchWriter(),
		webrtcRtcpOut: NewSwitchWriter(),
	}
}

func (v *VideoIO) Close() error {
	return errors.Join(
		v.sipRtpIn.Close(),
		v.sipRtpOut.Close(),
		v.sipRtcpIn.Close(),
		v.sipRtcpOut.Close(),
		v.webrtcRtpOut.Close(),
		v.webrtcRtcpOut.Close(),
	)
}

func (v *VideoManager) RtpPort() int {
	return v.rtpConn.LocalAddr().(*net.UDPAddr).Port
}

func (v *VideoManager) RtcpPort() int {
	return v.rtcpConn.LocalAddr().(*net.UDPAddr).Port
}

// updateActiveSpeakerCache maintains a circular buffer of the last N active speakers
// This helps us prioritize keyframe requests to the most likely candidates for switching
func (v *VideoManager) updateActiveSpeakerCache(sid string) {
	const maxActiveSpeakers = 6

	// Check if already in cache
	for i, s := range v.lastActiveSpeakers {
		if s == sid {
			// Move to end (most recent)
			v.lastActiveSpeakers = append(v.lastActiveSpeakers[:i], v.lastActiveSpeakers[i+1:]...)
			v.lastActiveSpeakers = append(v.lastActiveSpeakers, sid)
			return
		}
	}

	// Add new speaker
	v.lastActiveSpeakers = append(v.lastActiveSpeakers, sid)

	// Trim to max size
	if len(v.lastActiveSpeakers) > maxActiveSpeakers {
		v.lastActiveSpeakers = v.lastActiveSpeakers[len(v.lastActiveSpeakers)-maxActiveSpeakers:]
	}

	fmt.Printf("[%s] 📊 Active speakers cache updated: %v\n", time.Now().Format("15:04:05.000"), v.lastActiveSpeakers)
}

func (v *VideoManager) WebrtcTrackInput(ti *TrackInput, sid string, rp *lksdk.RemoteParticipant) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status != VideoStatusStarted {
		v.log.Warnw("video manager not started, cannot add WebRTC track input", nil, "status", v.status)
		return
	}

	v.log.Infow("WebRTC video track subscribed - connecting WebRTC→SIP pipeline",
		"participant", sid,
		"hasRtpIn", ti.RtpIn != nil,
		"hasRtcpIn", ti.RtcpIn != nil)

	// Store participant for keyframe requests
	v.participants[sid] = rp

	s, err := v.pipeline.AddWebRTCSourceToSelector(sid)
	if err != nil {
		v.log.Errorw("failed to add WebRTC source to selector", err)
		return
	}

	// Request a keyframe immediately when participant joins to prime the pipeline
	// This ensures we have a keyframe available if they become the active source
	v.log.Infow("requesting initial keyframe from new participant", "participant", sid)
	cameraPub := rp.GetTrackPublication(livekit.TrackSource_CAMERA)
	if cameraPub != nil {
		if track := cameraPub.Track(); track != nil {
			if remoteTrack, ok := track.(*webrtc.TrackRemote); ok {
				ssrc := remoteTrack.SSRC()
				v.log.Infow("sending initial PLI request", "participant", sid, "ssrc", ssrc)
				rp.WritePLI(webrtc.SSRC(ssrc))
			}
		}
	}

	webrtcRtpIn, err := NewGstWriter(s.WebrtcRtpAppSrc)
	if err != nil {
		v.log.Errorw("failed to create WebRTC RTP reader", err)
		return
	}
	go func() {
		Copy(webrtcRtpIn, ti.RtpIn)

		v.log.Infow("participant track ended, cleaning up", "participant", sid)
		fmt.Printf("[%s] 👋 Participant %s track ended\n", time.Now().Format("15:04:05.000"), sid)

		// Request keyframe from recent active speakers BEFORE removing from map
		// This scales better (6 requests vs potentially 40+) and targets likely-to-be-selected participants
		v.mu.Lock()

		// Build list of recent active speakers to request keyframes from (excluding the one leaving)
		var speakersToRequest []string
		for _, cachedSid := range v.lastActiveSpeakers {
			if cachedSid != sid {
				if _, exists := v.participants[cachedSid]; exists {
					speakersToRequest = append(speakersToRequest, cachedSid)
				}
			}
		}

		// If no cached speakers (e.g., first participant leaving), fall back to all remaining participants
		if len(speakersToRequest) == 0 && len(v.participants) > 1 {
			for remainingSid := range v.participants {
				if remainingSid != sid {
					speakersToRequest = append(speakersToRequest, remainingSid)
				}
			}
		}

		v.log.Infow("participant left, requesting keyframes from recent active speakers", "removed", sid, "requesting_from", len(speakersToRequest))
		fmt.Printf("[%s] 🎯 Participant %s left, requesting keyframes from %d recent active speakers: %v\n",
			time.Now().Format("15:04:05.000"), sid, len(speakersToRequest), speakersToRequest)

		for _, targetSid := range speakersToRequest {
			participant, ok := v.participants[targetSid]
			if !ok {
				continue
			}
			cameraPub := participant.GetTrackPublication(livekit.TrackSource_CAMERA)
			if cameraPub != nil {
				if track := cameraPub.Track(); track != nil {
					if remoteTrack, ok := track.(*webrtc.TrackRemote); ok {
						ssrc := remoteTrack.SSRC()
						v.log.Infow("requesting keyframe after participant departure", "participant", targetSid, "ssrc", ssrc)
						fmt.Printf("[%s] 🔑 Sending PLI request to participant %s (SSRC: %d)\n", time.Now().Format("15:04:05.000"), targetSid, ssrc)
						participant.WritePLI(webrtc.SSRC(ssrc))
					}
				}
			}
		}

		// Now remove participant from tracking
		delete(v.participants, sid)
		v.mu.Unlock()

		// Note: PLI requests sent above work well (keyframes arrive in ~50-100ms)
		// The auto-selection mechanism in ensureActiveSource() will wait for keyframe using a probe
		// just like manual active speaker switching does (see SwitchWebRTCSelectorSource)

		// Remove source from pipeline (this may trigger auto-selection which waits for keyframe)
		if err := v.pipeline.RemoveWebRTCSourceFromSelector(sid); err != nil {
			v.log.Errorw("failed to remove WebRTC source from selector", err)
			return
		}
	}()

	webrtcRtcpIn, err := NewGstWriter(s.WebrtcRtcpAppSrc)
	if err != nil {
		v.log.Errorw("failed to create WebRTC RTCP reader", err)
		return
	}
	go Copy(webrtcRtcpIn, ti.RtcpIn)
}

func (v *VideoManager) SwitchActiveWebrtcTrack(sid string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.log.Infow("switching active speaker", "participant", sid)

	// Update active speaker cache to track recent speakers
	v.updateActiveSpeakerCache(sid)

	// Request keyframe from LiveKit before switching
	participant, ok := v.participants[sid]
	if !ok {
		v.log.Warnw("participant not found for keyframe request", nil, "participant", sid)
	} else {
		// Get the camera track publication to find SSRC
		cameraPub := participant.GetTrackPublication(livekit.TrackSource_CAMERA)
		if cameraPub != nil {
			if track := cameraPub.Track(); track != nil {
				// Cast to webrtc.TrackRemote to get SSRC
				if remoteTrack, ok := track.(*webrtc.TrackRemote); ok {
					ssrc := remoteTrack.SSRC()
					v.log.Infow("requesting keyframe via PLI", "participant", sid, "ssrc", ssrc)
					participant.WritePLI(webrtc.SSRC(ssrc))
				} else {
					v.log.Warnw("track is not a remote track", nil, "participant", sid)
				}
			} else {
				v.log.Warnw("camera track not available for keyframe request", nil, "participant", sid)
			}
		} else {
			v.log.Warnw("camera track publication not found for keyframe request", nil, "participant", sid)
		}
	}

	return v.pipeline.SwitchWebRTCSelectorSource(sid)
}

func (v *VideoManager) WebrtcTrackOutput(to *TrackOutput) {
	v.log.Infow("WebRTC video track published - connecting SIP→WebRTC pipeline",
		"hasRtpOut", to.RtpOut != nil,
		"hasRtcpOut", to.RtcpOut != nil)

	if w := v.webrtcRtpOut.Swap(to.RtpOut); w != nil {
		_ = w.Close()
	}
	if w := v.webrtcRtcpOut.Swap(to.RtcpOut); w != nil {
		_ = w.Close()
	}
}

func (v *VideoManager) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.status = VideoStatusClosed
	v.log.Debugw("closing video manager")
	if v.pipeline != nil {
		if err := v.pipeline.Close(); err != nil {
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

func (v *VideoManager) Status() VideoStatus {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.status
}

func (v *VideoManager) Direction() sdpv2.Direction {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.send && v.recv {
		return sdpv2.DirectionSendRecv
	}
	if v.send {
		return sdpv2.DirectionSendOnly
	}
	if v.recv {
		return sdpv2.DirectionRecvOnly
	}
	return sdpv2.DirectionInactive
}

func (v *VideoManager) Codec() *sdpv2.Codec {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.codec
}

func (v *VideoManager) SupportedCodecs() []*sdpv2.Codec {
	//TODO: make is dynamic.
	c := sdpv1.CodecByName(h264.SDPName)
	if c == nil {
		return []*sdpv2.Codec{}
	}
	codec, err := (&sdpv2.Codec{}).Builder().SetCodec(c).Build()
	if err != nil {
		return []*sdpv2.Codec{}
	}
	return []*sdpv2.Codec{
		codec,
	}
}

func (v *VideoManager) setupOutput(remote netip.Addr, media *sdpv2.SDPMedia, send bool) error {
	if !send {
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
	rtpAddr := netip.AddrPortFrom(remote, media.Port)
	v.rtpConn.SetDst(rtpAddr)

	rtcpAddr := netip.AddrPortFrom(remote, media.RTCPPort)
	v.rtcpConn.SetDst(rtcpAddr)

	v.log.Infow("setting up video send to SIP", "remote", remote.String(), "port", media.Port, "rtcp_port", media.RTCPPort)
	if w := v.sipRtpOut.Swap(v.rtpConn); w != nil {
		_ = w.Close()
	}
	if w := v.sipRtcpOut.Swap(v.rtcpConn); w != nil {
		_ = w.Close()
	}
	return nil
}

func (v *VideoManager) setupInput(remote netip.Addr, media *sdpv2.SDPMedia, recv bool) error {
	if !recv {
		if r := v.sipRtpIn.Swap(nil); r != nil {
			_ = r.Close()
		}
		if r := v.sipRtcpIn.Swap(nil); r != nil {
			_ = r.Close()
		}
		return nil
	}

	v.log.Infow("setting up video receive from SIP", "remote", remote.String(), "port", media.Port, "rtcp_port", media.RTCPPort)
	if r := v.sipRtpIn.Swap(v.rtpConn); r != nil {
		_ = r.Close()
	}
	if r := v.sipRtcpIn.Swap(v.rtcpConn); r != nil {
		_ = r.Close()
	}
	return nil
}

func (v *VideoManager) Setup(remote netip.Addr, media *sdpv2.SDPMedia) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status != VideoStatusStopped {
		return fmt.Errorf("video manager must be stopped to setup, current status: %d", v.status)
	}

	v.log.Debugw("setting up video manager", "media", media)

	if media.Codec == nil {
		return fmt.Errorf("no codec selected for video media")
	}
	v.codec = media.Codec

	if err := v.SetupGstPipeline(media); err != nil {
		return fmt.Errorf("failed to setup GStreamer pipeline: %w", err)
	}

	v.recv = media.Direction.IsRecv()
	v.send = media.Direction.IsSend()

	v.log.Infow("video setup", "send", v.send, "recv", v.recv, "remote", remote.String(), "rtp_port", v.RtpPort(), "rtcp_port", v.RtcpPort())

	if err := v.setupOutput(remote, media, v.recv); err != nil {
		return fmt.Errorf("failed to setup video input: %w", err)
	}

	if err := v.setupInput(remote, media, v.send); err != nil {
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

	// Set status to Started BEFORE setting pipeline state to allow WebRTC tracks to be added
	// The pipeline state change to PLAYING may be ASYNC and wait for data (preroll)
	// But we need to accept WebRTC tracks for data to flow
	v.status = VideoStatusStarted

	if err := v.pipeline.SetState(gst.StatePlaying); err != nil {
		v.status = VideoStatusReady // Rollback on error
		return fmt.Errorf("failed to set GStreamer pipeline to playing: %w", err)
	}

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

	v.codec = nil

	if err := v.pipeline.Close(); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
	}

	v.recv = false
	v.send = false

	v.status = VideoStatusStopped
	return nil
}
