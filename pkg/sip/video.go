package sip

import (
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
	pionrtp "github.com/pion/rtp"
)

var mainLoop *glib.MainLoop

func init() {
	gst.Init(nil)

	mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)
	_ = mainLoop
}

// generateSSRCForParticipant generates a stable SSRC for a participant based on their ID.
// This ensures the same participant always gets the same SSRC, which is crucial for
// VideoSwitcher to properly route packets by SSRC.
func generateSSRCForParticipant(participantID string) uint32 {
	// Use a hash of the participant ID to generate a stable SSRC
	h := uint32(0)
	for i := 0; i < len(participantID); i++ {
		h = h*31 + uint32(participantID[i])
	}
	// Ensure SSRC is non-zero and in a reasonable range (0x50000000 to 0x5FFFFFFF)
	return 0x50000000 + (h % 0x0FFFFFFF)
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
		log:                  log,
		room:                 room,
		opts:                 opts,
		media:                media,
		rtpConn:              newUDPConn(log.WithComponent("video-rtp"), rtpConn),
		rtcpConn:             newUDPConn(log.WithComponent("video-rtcp"), rtcpConn),
		participantRewriters: make(map[string]*RTPRewriter),
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
	log                  logger.Logger
	opts                 *MediaOptions
	media                *sdpv2.SDPMedia
	room                 *Room
	rtpConn              *udpConn
	rtcpConn             *udpConn
	pipeline             *VideoPipeline
	videoSwitcher        *VideoSwitcher            // Intelligent switcher for smooth transitions
	participantRewriters map[string]*RTPRewriter   // Per-participant RTP rewriters
	rewriterMu           sync.Mutex                // Protects participantRewriters map
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
		if err := v.pipeline.Close(); err != nil {
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
		identity := ti.GetIdentity()
		v.log.Infow("🎬 Track switch starting",
			"trackSid", ti.Sid,
			"ssrc", ti.SSRC,
			"identity", identity)

		// Generate stable SSRC for this participant
		// VideoSwitcher uses this SSRC to route packets and manage transitions
		participantSSRC := generateSSRCForParticipant(ti.Sid)

		v.rewriterMu.Lock()
		// Check if we already have a rewriter for this participant
		rewriter, exists := v.participantRewriters[identity]
		if !exists {
			// Create new per-participant RTP rewriter
			// Each rewriter maintains continuity for ONE participant's stream
			rewriter = NewRTPRewriter(participantSSRC, v.log.WithValues("participant", identity))
			v.participantRewriters[identity] = rewriter
			v.log.Infow("✨ Created per-participant RTP rewriter",
				"identity", identity,
				"participantSSRC", participantSSRC,
				"sourceSSRC", ti.SSRC)
		}
		v.rewriterMu.Unlock()

		// Notify rewriter about the source SSRC for this participant
		rewriter.SwitchSource(ti.SSRC)

		// Step 1: Request VP8 keyframe from new source (WebRTC participant)
		// Burst PLI requests to maximize chances of quick keyframe
		for i := 0; i < 3; i++ {
			if err := ti.SendPLI(); err != nil {
				v.log.Debugw("PLI burst failed", "attempt", i+1, "error", err)
			}
		}

		// Step 2: Swap RTCP immediately for bidirectional communication
		if w := v.webrtcRtcpIn.Swap(ti.RtcpIn); w != nil {
			_ = w.Close()
		}

		// Step 3: Set up packet flow: TrackInput → RTPRewriter → VideoSwitcher → GStreamer
		// Create reader that rewrites packets before sending to VideoSwitcher
		rewritingReader := NewRewritingReader(ti.RtpIn, rewriter, v.log)

		// Start goroutine to read from track and write through rewriter to VideoSwitcher
		go func() {
			defer func() {
				v.log.Debugw("Track reader goroutine exiting", "identity", identity)
			}()

			buf := make([]byte, 1500) // MTU size buffer
			for {
				// Read RTP packet from track
				n, err := rewritingReader.Read(buf)
				if err != nil {
					if err != io.EOF {
						v.log.Debugw("Track read error", "identity", identity, "error", err)
					}
					return
				}

				// Parse packet to get header and payload
				var pkt pionrtp.Packet
				if unmarshalErr := pkt.Unmarshal(buf[:n]); unmarshalErr != nil {
					v.log.Debugw("Failed to unmarshal packet", "error", unmarshalErr)
					continue
				}

				// Write to VideoSwitcher
				// VideoSwitcher will buffer packets until keyframe and manage smooth transitions
				if v.videoSwitcher != nil {
					if _, writeErr := v.videoSwitcher.WriteRTP(&pkt.Header, pkt.Payload); writeErr != nil {
						v.log.Debugw("VideoSwitcher write error", "error", writeErr)
					}
				}
			}
		}()

		// Step 4: Tell VideoSwitcher to switch to this participant
		// VideoSwitcher will buffer packets until VP8 keyframe arrives, then switch seamlessly
		isFirstTrack := v.videoSwitcher.GetCurrentSSRC() == 0
		if v.videoSwitcher != nil {
			v.videoSwitcher.SetActiveSource(participantSSRC, "active speaker switch")
			v.log.Infow("✅ VideoSwitcher set to new source",
				"participantSSRC", participantSSRC,
				"identity", identity,
				"isFirstTrack", isFirstTrack)
		}

		// Step 5: Request H.264 keyframe from x264 encoder after transition
		// This ensures SIP side gets clean keyframe after speaker change
		if !isFirstTrack {
			go func() {
				time.Sleep(200 * time.Millisecond)
				if err := v.RequestKeyframe("active speaker switch"); err != nil {
					v.log.Warnw("Failed to request H.264 keyframe after track switch", err)
				}
			}()
		}
	})

	v.room.UpdateActiveParticipant(nil)

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

	if err := v.pipeline.Start(); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to playing: %w", err)
	}
	return nil
}

// RequestKeyframe requests a keyframe from the x264 encoder
// This should be called after switching active speakers to get a clean H.264 keyframe for SIP
func (v *VideoManager) RequestKeyframe(reason string) error {
	if v.pipeline == nil {
		return fmt.Errorf("pipeline not initialized")
	}
	v.log.Infow("requesting keyframe from x264 encoder", "reason", reason)
	return v.pipeline.RequestKeyframe(reason)
}

// FlushPipeline flushes the video pipeline to clear buffered frames
// This should be called after switching active speakers to remove buffered packets from the old speaker
func (v *VideoManager) FlushPipeline(reason string) error {
	if v.pipeline == nil {
		return fmt.Errorf("pipeline not initialized")
	}
	v.log.Infow("flushing video pipeline", "reason", reason)
	return v.pipeline.Flush(reason)
}
