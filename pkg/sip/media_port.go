// Copyright 2024 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	"github.com/google/uuid"

	"github.com/go-gst/go-gst/gst"
	msdk "github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/dtmf"
	"github.com/livekit/media-sdk/mixer"
	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/media-sdk/sdp"
	"github.com/livekit/media-sdk/srtp"
	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/sip/pkg/stats"
)

const (
	defaultMediaTimeout        = 15 * time.Second
	defaultMediaTimeoutInitial = 30 * time.Second
)

type PortStats struct {
	Streams        atomic.Uint64
	Packets        atomic.Uint64
	IgnoredPackets atomic.Uint64
	InputPackets   atomic.Uint64

	MuxPackets atomic.Uint64
	MuxBytes   atomic.Uint64

	AudioPackets atomic.Uint64
	AudioBytes   atomic.Uint64

	VideoPackets atomic.Uint64
	VideoBytes   atomic.Uint64

	DTMFPackets atomic.Uint64
	DTMFBytes   atomic.Uint64
}

type UDPConn interface {
	net.Conn
	ReadFromUDPAddrPort(b []byte) (n int, addr netip.AddrPort, err error)
	WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error)
}

func newUDPConn(log logger.Logger, conn UDPConn) *udpConn {
	return &udpConn{UDPConn: conn, log: log}
}

type udpConn struct {
	UDPConn
	log logger.Logger
	src atomic.Pointer[netip.AddrPort]
	dst atomic.Pointer[netip.AddrPort]
}

func (c *udpConn) GetSrc() (netip.AddrPort, bool) {
	ptr := c.src.Load()
	if ptr == nil {
		return netip.AddrPort{}, false
	}
	addr := *ptr
	return addr, addr.IsValid()
}

func (c *udpConn) SetDst(addr netip.AddrPort) {
	if addr.IsValid() {
		prev := c.dst.Swap(&addr)
		if prev == nil || !prev.IsValid() {
			c.log.Infow("setting media destination", "addr", addr.String())
		} else if *prev != addr {
			c.log.Infow("changing media destination", "addr", addr.String())
		}
	}
}

func (c *udpConn) Read(b []byte) (n int, err error) {
	n, addr, err := c.ReadFromUDPAddrPort(b)
	prev := c.src.Swap(&addr)
	if prev == nil || !prev.IsValid() {
		c.log.Infow("setting media source", "addr", addr.String())
	} else if *prev != addr {
		c.log.Infow("changing media source", "addr", addr.String())
	}
	return n, err
}

func (c *udpConn) Write(b []byte) (n int, err error) {
	dst := c.dst.Load()
	if dst == nil {
		return len(b), nil // ignore
	}
	return c.WriteToUDPAddrPort(b, *dst)
}

type MediaConf struct {
	sdp.MediaConfig
	Processor msdk.PCM16Processor
}

// VideoConfig represents video configuration for SIP media.
type VideoConfig struct {
	Codec msdk.Codec
	Type  byte
}

type MediaOptions struct {
	IP                  netip.Addr
	Ports               rtcconfig.PortRange
	MediaTimeoutInitial time.Duration
	MediaTimeout        time.Duration
	Stats               *PortStats
	EnableJitterBuffer  bool
}

func NewMediaPort(log logger.Logger, mon *stats.CallMonitor, opts *MediaOptions, sampleRate int) (*MediaPort, error) {
	return NewMediaPortWith(log, mon, nil, opts, sampleRate)
}

func NewMediaPortWith(log logger.Logger, mon *stats.CallMonitor, conn UDPConn, opts *MediaOptions, sampleRate int) (*MediaPort, error) {
	if opts == nil {
		opts = &MediaOptions{}
	}
	if opts.MediaTimeoutInitial <= 0 {
		opts.MediaTimeoutInitial = defaultMediaTimeoutInitial
	}
	if opts.MediaTimeout <= 0 {
		opts.MediaTimeout = defaultMediaTimeout
	}
	if opts.Stats == nil {
		opts.Stats = &PortStats{}
	}
	if conn == nil {
		c, err := rtp.ListenUDPPortRange(opts.Ports.Start, opts.Ports.End, netip.AddrFrom4([4]byte{0, 0, 0, 0}))
		slog.Info("listening for media on UDP", "addr", c.LocalAddr().String(), "startPort", opts.Ports.Start, "endPort", opts.Ports.End)
		if err != nil {
			return nil, err
		}
		conn = c
	}
	mediaTimeout := make(chan struct{})
	p := &MediaPort{
		log:              log,
		opts:             opts,
		mon:              mon,
		externalIP:       opts.IP,
		mediaTimeout:     mediaTimeout,
		timeoutResetTick: make(chan time.Duration, 1),
		jitterEnabled:    opts.EnableJitterBuffer,
		port:             newUDPConn(log, conn),
		audioOut:         msdk.NewSwitchWriter(sampleRate),
		audioIn:          msdk.NewSwitchWriter(sampleRate),
		videoIn:          msdk.NewFrameSwitchWriter(90000),
		videoOut:         msdk.NewFrameSwitchWriter(90000),
		stats:            opts.Stats,
	}
	p.timeoutInitial.Store(&opts.MediaTimeoutInitial)
	p.timeoutGeneral.Store(&opts.MediaTimeout)
	go p.timeoutLoop(func() {
		close(mediaTimeout)
	})
	p.log.Debugw("listening for media on UDP", "port", p.Port())

	slog.Info("media port created", "port", p.Port(), "externalIP", p.externalIP.String(), "mediaTimeoutInitial", opts.MediaTimeoutInitial, "mediaTimeout", opts.MediaTimeout)

	return p, nil
}

// MediaPort combines all functionality related to sending and accepting SIP media.
type MediaPort struct {
	log              logger.Logger
	opts             *MediaOptions
	mon              *stats.CallMonitor
	externalIP       netip.Addr
	port             *udpConn
	videoPort        *udpConn
	mediaReceived    core.Fuse
	packetCount      atomic.Uint64
	mediaTimeout     <-chan struct{}
	timeoutStart     atomic.Pointer[time.Time]
	timeoutResetTick chan time.Duration
	timeoutInitial   atomic.Pointer[time.Duration]
	timeoutGeneral   atomic.Pointer[time.Duration]
	closed           core.Fuse
	stats            *PortStats
	dtmfAudioEnabled bool
	jitterEnabled    bool

	mu           sync.Mutex
	conf         *MediaConf
	sess         rtp.Session
	vsess        rtp.Session // TODO: won't work, need to use gstreamer to merge sessions
	hnd          atomic.Pointer[rtp.HandlerCloser]
	vhnd         atomic.Pointer[rtp.HandlerCloser]
	dtmfOutRTP   *rtp.Stream
	dtmfOutAudio msdk.PCM16Writer

	audioOutRTP    *rtp.Stream
	audioOut       *msdk.SwitchWriter // LK PCM -> SIP RTP
	audioIn        *msdk.SwitchWriter // SIP RTP -> LK PCM
	audioInHandler rtp.Handler        // for debug only
	dtmfIn         atomic.Pointer[func(ev dtmf.Event)]

	// Video support
	videoOutRTP    *rtp.Stream
	videoOut       *msdk.FrameSwitchWriter // LK Video -> SIP RTP
	videoIn        *msdk.FrameSwitchWriter // SIP RTP -> LK Video
	videoInHandler rtp.Handler             // for debug only

	gstPipeline GstPipeline
}

type GstPipeline struct {
	enabled  bool
	ID       uuid.UUID
	pipeline *gst.Pipeline
	RTPPort  int
}

func (p *MediaPort) VideoPort() (int, error) {
	if p.videoPort != nil {
		return p.videoPort.LocalAddr().(*net.UDPAddr).Port, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	c, err := rtp.ListenUDPPortRange(p.opts.Ports.Start, p.opts.Ports.End, netip.AddrFrom4([4]byte{0, 0, 0, 0}))
	slog.Info("listening for media on UDP", "addr", c.LocalAddr().String(), "startPort", p.opts.Ports.Start, "endPort", p.opts.Ports.End)
	if err != nil {
		return 0, err
	}
	p.videoPort = newUDPConn(p.log, c)
	return p.videoPort.LocalAddr().(*net.UDPAddr).Port, nil
}

func (p *MediaPort) DisableOut() {
	p.audioOut.Disable()
}

func (p *MediaPort) EnableOut() {
	p.audioOut.Enable()
}

func (p *MediaPort) disableTimeout() {
	p.timeoutStart.Store(nil)
}

func (p *MediaPort) enableTimeout(initial, general time.Duration) {
	p.timeoutInitial.Store(&initial)
	p.timeoutGeneral.Store(&general)
	select {
	case p.timeoutResetTick <- general:
	default:
	}
	now := time.Now()
	p.timeoutStart.Store(&now)
	p.log.Infow("media timeout enabled",
		"packets", p.packetCount.Load(),
		"initial", initial,
		"timeout", general,
	)
}

func (p *MediaPort) EnableTimeout(enabled bool) {
	if !enabled {
		p.disableTimeout()
		return
	}
	p.enableTimeout(p.opts.MediaTimeoutInitial, p.opts.MediaTimeout)
}

func (p *MediaPort) SetTimeout(initial, general time.Duration) {
	if initial <= 0 {
		p.disableTimeout()
		return
	}
	p.enableTimeout(initial, general)
}

func (p *MediaPort) timeoutLoop(timeoutCallback func()) {
	ticker := time.NewTicker(p.opts.MediaTimeout)
	defer ticker.Stop()

	var (
		lastPackets  uint64
		startPackets uint64
		lastTime     time.Time
	)
	for {
		select {
		case <-p.closed.Watch():
			return
		case tick := <-p.timeoutResetTick:
			ticker.Reset(tick)
			startPackets = p.packetCount.Load()
			lastTime = time.Now()
		case <-ticker.C:
			curPackets := p.packetCount.Load()
			if curPackets != lastPackets {
				lastPackets = curPackets
				lastTime = time.Now()
				continue // wait for the next tick
			}
			startPtr := p.timeoutStart.Load()
			if startPtr == nil {
				continue // timeout disabled
			}
			isInitial := lastPackets == startPackets
			sinceStart := time.Since(*startPtr)
			sinceLast := time.Since(lastTime)
			var (
				since   time.Duration
				timeout time.Duration
			)
			// First timeout could be different. Usually it's longer to allow for a call setup.
			// In some cases it could be shorter (e.g. when we notice an issue with signaling and suspect media will fail).
			if isInitial {
				since = sinceStart
				timeout = p.opts.MediaTimeoutInitial
				if ptr := p.timeoutInitial.Load(); ptr != nil {
					timeout = *ptr
				}
			} else {
				since = sinceLast
				timeout = p.opts.MediaTimeout
				if ptr := p.timeoutGeneral.Load(); ptr != nil {
					timeout = *ptr
				}
			}

			// Ticker is allowed to fire earlier than the full timeout interval. Skip if it's not a full timeout yet.
			if since < timeout {
				continue
			}
			p.log.Infow("triggering media timeout",
				"packets", lastPackets,
				"startPackets", startPackets,
				"sinceStart", sinceStart,
				"sinceLast", sinceLast,
				"timeout", timeout,
				"isInitial", isInitial,
			)
			timeoutCallback()
			return
		}
	}
}

func (p *MediaPort) Close() {
	p.closed.Once(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if w := p.audioOut.Swap(nil); w != nil {
			_ = w.Close()
		}
		if w := p.audioIn.Swap(nil); w != nil {
			_ = w.Close()
		}
		// if p.videoOut != nil {
		// 	p.videoOut.Close()
		// 	p.videoOut = nil
		// }
		// if p.videoIn != nil {
		// 	p.videoIn.Close()
		// 	p.videoIn = nil
		// }
		p.audioOutRTP = nil
		p.audioInHandler = nil
		p.videoOutRTP = nil
		p.videoInHandler = nil
		p.dtmfOutRTP = nil
		if p.dtmfOutAudio != nil {
			p.dtmfOutAudio.Close()
			p.dtmfOutAudio = nil
		}
		p.dtmfIn.Store(nil)
		if p.sess != nil {
			_ = p.sess.Close()
		}
		if p.videoPort != nil {
			p.videoPort.Close()
			p.videoPort = nil
		}
		_ = p.port.Close()

		hnd := p.hnd.Load()
		if hnd != nil {
			(*hnd).Close()
		}
	})
}

func (p *MediaPort) Port() int {
	return p.port.LocalAddr().(*net.UDPAddr).Port
}

func (p *MediaPort) Received() <-chan struct{} {
	return p.mediaReceived.Watch()
}

func (p *MediaPort) Timeout() <-chan struct{} {
	return p.mediaTimeout
}

func (p *MediaPort) Config() *MediaConf {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conf
}

func (p *MediaPort) WriteVideoTo(w msdk.FrameWriter) {
	if pw := p.videoIn.Swap(w); pw != nil {
		_ = pw.Close()
	}
}

// WriteAudioTo sets audio writer that will receive decoded PCM from incoming RTP packets.
func (p *MediaPort) WriteAudioTo(w msdk.PCM16Writer) {
	if processor := p.conf.Processor; processor != nil {
		w = processor(w)
	}
	if pw := p.audioIn.Swap(w); pw != nil {
		_ = pw.Close()
	}
}

// GetAudioWriter returns audio writer that will send PCM to the destination via RTP.
func (p *MediaPort) GetAudioWriter() msdk.PCM16Writer {
	return p.audioOut
}

// GetVideoWriter returns video writer that will send video to the destination via RTP.
func (p *MediaPort) GetVideoWriter() msdk.FrameWriter {
	return p.videoOut
}

// NewOffer generates an SDP offer for the media.
func (p *MediaPort) NewOffer(encrypted sdp.Encryption) (*sdp.Offer, error) {
	var vp *int = nil
	if p.videoPort != nil {
		slog.Info("including video port in SDP offer")
		tp, err := p.VideoPort()
		if err != nil {
			return nil, err
		}
		vp = new(int)
		*vp = tp
	}
	return sdp.NewOffer(p.externalIP, p.Port(), vp, encrypted)
}

// // NewOfferWithVideo generates an SDP offer for both audio and video media.
// func (p *MediaPort) NewOfferWithVideo(encrypted sdp.Encryption) (*sdp.Offer, *VideoMediaDesc, error) {
// 	// For now, just return audio-only offer
// 	// TODO: Implement proper video SDP generation
// 	offer, err := sdp.NewOffer(p.externalIP, p.Port(), encrypted)
// 	if err != nil {
// 		return nil, nil, err
// 	}
// 	return offer, &VideoMediaDesc{}, nil
// }

// VideoMediaDesc represents video media description for SDP.
// type VideoMediaDesc struct {
// 	Codecs         []sdp.CodecInfo
// 	CryptoProfiles []srtp.Profile
// }

// SetAnswer decodes and applies SDP answer for offer from NewOffer. SetConfig must be called with the decoded configuration.
func (p *MediaPort) SetAnswer(offer *sdp.Offer, answerData []byte, enc sdp.Encryption) (*MediaConf, error) {
	answer, err := sdp.ParseAnswer(answerData)
	if err != nil {
		return nil, err
	}
	mc, err := answer.Apply(offer, enc)
	if err != nil {
		return nil, err
	}
	return &MediaConf{MediaConfig: *mc}, nil
}

// func BuildRtpMuxPipeline(destIP string, rtpInPort, rtcpInPort, outPort, payloadType uint16) (*gst.Pipeline, error) {

// 	// Caps for the incoming RTP/H264. Adjust payload, or remove it if you don’t want to pin PT.
// 	caps := fmt.Sprintf(
// 		"application/x-rtp, media=video, encoding-name=H264, clock-rate=90000, payload=%d",
// 		payloadType,
// 	)

// 	// One udpsink is fed by a funnel that merges rtpbin’s RTP+RTCP src pads.
// 	// bind-port=outPort forces a single LOCAL port as well (optional; remove if not needed).
// 	pipe := fmt.Sprintf(`
// 		rtpbin name=rb latency=100

// 		udpsrc port=%d caps="%s" ! rb.recv_rtp_sink_0
// 		# udpsrc port=%%d caps="application/x-rtcp" ! rb.recv_rtcp_sink_0

// 		# turn received RTP straight back through rtpbin’s sender to get paired RTP/RTCP src pads
// 		rb.recv_rtp_src_0 ! queue max-size-buffers=0 max-size-time=0 max-size-bytes=0 ! rb.send_rtp_sink_0

// 		funnel name=mux
// 		rb.send_rtp_src_0  ! queue ! mux.sink_0
// 		# rb.send_rtcp_src_0 ! queue ! mux.sink_1

// 		mux.src ! udpsink host=%s port=%d bind-port=%d sync=false async=false
// 	`,
// 		rtpInPort, caps,
// 		// rtcpInPort,
// 		destIP, outPort, outPort,
// 	)

// 	return gst.NewPipelineFromString(pipe)
// }

func gstElement(factory, name string, props map[string]interface{}) (*gst.Element, error) {
	el, err := gst.NewElementWithName(factory, name)
	if err != nil {
		slog.Error("cannot create gstreamer element", "factory", factory, "name", name, "error", err)
		return nil, err
	}
	for k, v := range props {
		if err := el.SetProperty(k, v); err != nil {
			slog.Error("cannot set gstreamer element property", "factory", factory, "name", name, "property", k, "value", v, "error", err)
			return nil, err
		}
	}
	return el, nil
}

func (p *MediaPort) EnableGst(desc *sdp.Description) error {
	ID := uuid.New()
	vp, err := p.VideoPort()
	if err != nil {
		slog.Error("cannot get video port", "error", err)
		return err
	}

	caps := fmt.Sprintf(
		"application/x-rtp, media=video, encoding-name=H264, clock-rate=90000, payload=%d",
		96,
	)

	// rtpConn, rtpPort, rtpFD, err := rtp.ReserveUDP()
	// if err != nil {
	// 	slog.Error("cannot reserve UDP port for RTP", "error", err)
	// 	return err
	// }
	// defer rtpConn.Close()

	rtpPort, err := rtp.FindFreeUDPPort(p.externalIP)
	if err != nil {
		slog.Error("cannot find free UDP port for RTP", "error", err)
		return err
	}

	rtcpPort, err := rtp.FindFreeUDPPort(p.externalIP)
	if err != nil {
		slog.Error("cannot find free UDP port for RTP", "error", err)
		return err
	}

	// pipe := fmt.Sprintf(`
	// udpsrc port=%d caps="application/x-rtp,media=video,encoding-name=H264,clock-rate=90000,payload=96" name=rtps \
	// udpsrc port=%d caps="application/x-rtcp" name=rtcps \
	// rtcpmux name=m \
	// 	rtps. ! m.rtp_sink \
	// 	rtcps. ! m.rtcp_sink \
	// m. ! udpsink host=%s port=%d sync=false async=false
	// `, rtpPort, rtcpPort, p.externalIP.String(), vp)

	// pipeline, err := gst.NewPipelineFromString(pipe)
	// if err != nil {
	// 	slog.Error("cannot create gstreamer pipeline", "error", err)
	// 	return err
	// }

	pipeline, err := gst.NewPipeline(fmt.Sprintf("sip-media-%s", ID.String()))
	if err != nil {
		slog.Error("cannot create gstreamer pipeline", "error", err)
		return err
	}

	var (
		errs  error
		elems []*gst.Element
	)

	wrapElem := func(factory string, f ...func(*gst.Element) error) *gst.Element {
		log := slog.With("factory", factory)
		elem, err := gst.NewElement(factory)
		if err != nil {
			errs = errors.Join(errs, err)
			log.Error("cannot create gstreamer element", "error", err)
			return elem
		}
		name := elem.GetName()

		log = slog.With("name", name)

		log.Info("created gstreamer element")
		for _, fn := range f {
			if err := fn(elem); err != nil {
				errs = errors.Join(errs, err)
				log.Error("cannot configure gstreamer element", "error", err)
			}
		}
		elems = append(elems, elem)
		return elem
	}

	slog.Info("gst port config", "rtpPort", rtpPort, "rtcpPort", rtcpPort, "destIP", p.externalIP.String(), "destPort", vp)

	rtpSrc := wrapElem("udpsrc",
		func(e *gst.Element) error { return e.SetProperty("port", rtpPort) },
		func(e *gst.Element) error { return e.SetProperty("caps", gst.NewCapsFromString(caps)) }, // e.g. application/x-rtp,...
	)
	rtcpSrc := wrapElem("udpsrc",
		func(e *gst.Element) error { return e.SetProperty("port", rtcpPort) },
		func(e *gst.Element) error { return e.SetProperty("caps", gst.NewCapsFromString("application/x-rtcp")) },
	)

	// Two udpsinks to the SAME destination host/port
	rtpOut := wrapElem("udpsink",
		func(e *gst.Element) error { return e.SetProperty("host", p.externalIP.String()) },
		func(e *gst.Element) error { return e.SetProperty("port", vp) },
		func(e *gst.Element) error { return e.SetProperty("sync", false) },
		func(e *gst.Element) error { return e.SetProperty("async", false) },
	)
	rtcpOut := wrapElem("udpsink",
		func(e *gst.Element) error { return e.SetProperty("host", p.externalIP.String()) },
		func(e *gst.Element) error { return e.SetProperty("port", vp) },
		func(e *gst.Element) error { return e.SetProperty("sync", false) },
		func(e *gst.Element) error { return e.SetProperty("async", false) },
	)

	if errs != nil {
		slog.Error("cannot create gstreamer elements", "error", errs)
		return errs
	}

	if err := pipeline.AddMany(elems...); err != nil {
		slog.Error("cannot add gstreamer elements to pipeline", "error", err)
		pipeline.SetState(gst.StateNull)
		return err
	}

	if err := gst.ElementLinkMany(rtpSrc, rtpOut); err != nil {
		slog.Error("cannot link gstreamer elements", "error", err)
		pipeline.SetState(gst.StateNull)
		return err
	}
	if err := gst.ElementLinkMany(rtcpSrc, rtcpOut); err != nil {
		slog.Error("cannot link gstreamer elements", "error", err)
		pipeline.SetState(gst.StateNull)
		return err
	}

	if err = pipeline.SetState(gst.StatePlaying); err != nil {
		slog.Error("cannot set gstreamer pipeline to playing", "error", err)
		pipeline.SetState(gst.StateNull)
		return err
	}

	p.gstPipeline.enabled = true
	p.gstPipeline.ID = ID
	p.gstPipeline.pipeline = pipeline
	p.gstPipeline.RTPPort = rtpPort

	return nil
}

// SetOffer decodes the offer from another party and returns encoded answer. To accept the offer, call SetConfig.
func (p *MediaPort) SetOffer(offerData []byte, enc sdp.Encryption) (*sdp.Answer, *MediaConf, error) {
	offer, err := sdp.ParseOffer(offerData)
	if err != nil {
		return nil, nil, err
	}

	var (
		vp *int = nil
	)

	// offer.Video = nil // disable video for now
	if offer.Video != nil {
		slog.Info("including video port in SDP answer")
		desc := sdp.Description(*offer)

		tp, err := p.VideoPort()
		if err != nil {
			slog.Error("cannot get video port", "error", err)
			return nil, nil, err
		}

		// if err := p.EnableGst(&desc); err != nil {
		// 	slog.Error("cannot enable gstreamer for video", "error", err)
		// 	return nil, nil, err
		// }

		// slog.Info("gstreamer pipeline started", "id", p.gstPipeline.ID, "rtpPort", p.gstPipeline.RTPPort)

		slog.Info("including video port in SDP answer", "video-port", tp)

		*offer = sdp.Offer(desc)
		vp = new(int)
		// *vp = p.gstPipeline.RTPPort
		*vp = tp
	}
	answer, mc, err := offer.Answer(p.externalIP, p.Port(), vp, enc)
	if err != nil {
		return nil, nil, err
	}
	return answer, &MediaConf{MediaConfig: *mc}, nil
}

func (p *MediaPort) SetConfig(c *MediaConf) error {
	if p.closed.IsBroken() {
		return errors.New("media is already closed")
	}
	var crypto string
	if c.Audio.Crypto != nil {
		crypto = c.Audio.Crypto.Profile.String()
	}
	p.log.Infow("using codecs",
		"audio-codec", c.Audio.Codec.Info().SDPName, "audio-rtp", c.Audio.Type,
		"dtmf-rtp", c.Audio.DTMFType,
		"srtp", crypto,
	)

	p.port.SetDst(c.Audio.Remote)
	var (
		sess rtp.Session
		err  error
	)
	if c.Audio.Crypto != nil {
		sess, err = srtp.NewSession(p.log.WithValues("sess-media", "audio"), p.port, c.Audio.Crypto)
	} else {
		sess = rtp.NewSession(p.log.WithValues("sess-media", "audio"), p.port)
	}
	if err != nil {
		return err
	}

	var vsess rtp.Session = nil
	if c.Video != nil {
		var crypto string
		if c.Video.Crypto != nil {
			crypto = c.Video.Crypto.Profile.String()
		}
		p.log.Infow("using video codec",
			"video-codec", c.Video.Codec.Info().SDPName, "video-rtp", c.Video.Type,
			"srtp", crypto,
		)
		p.VideoPort() // ensure video port is created
		p.videoPort.SetDst(c.Video.Remote)
		if c.Video.Crypto != nil {
			vsess, err = srtp.NewSession(p.log.WithValues("sess-media", "video"), p.videoPort, c.Video.Crypto)
		} else {
			vsess = rtp.NewSession(p.log.WithValues("sess-media", "video"), p.videoPort)
		}
		if err != nil {
			sess.Close()
			return err
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.port.SetDst(c.Audio.Remote)
	p.conf = c
	p.sess = sess
	p.vsess = vsess

	if err = p.setupOutput(); err != nil {
		return err
	}
	p.setupInput()
	return nil
}

func (p *MediaPort) rtpLoop(sess rtp.Session, hndp *atomic.Pointer[rtp.HandlerCloser]) {
	// Need a loop to process all incoming packets.
	for {
		r, ssrc, err := sess.AcceptStream()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "closed") {
				p.log.Errorw("cannot accept RTP stream", err)
			}
			return
		}
		p.stats.Streams.Add(1)
		p.mediaReceived.Break()
		log := p.log.WithValues("ssrc", ssrc)
		log.Infow("accepting RTP stream")
		r.L().Infow("accepted RTP stream", "ssrc", ssrc)
		go p.rtpReadLoop(log, r, hndp)
	}
}

type rtpStreamCloser struct {
	rtp.ReadStream
}

func (r rtpStreamCloser) Close() error { return nil }

func (r rtpStreamCloser) Read(buf []byte) (int, error) {
	var h rtp.Header
	n, err := r.ReadRTP(&h, buf)
	if err != nil {
		if err != io.EOF { /* log error */
		}
		return 0, err
	}

	pkt := &rtp.Packet{
		Header:  h,
		Payload: buf[:n],
	}
	raw, err := pkt.Marshal()
	if err != nil {
		/* log error */
		return 0, err
	}

	copy(buf, raw)
	return len(raw), nil
}

func (p *MediaPort) rtpLoopVideo(sess rtp.Session) {
	// Need a loop to process all incoming packets.
	for {
		r, ssrc, err := sess.AcceptStream()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "closed") {
				p.log.Errorw("cannot accept RTP stream", err)
			}
			return
		}
		p.stats.Streams.Add(1)
		p.mediaReceived.Break()
		log := p.log.WithValues("ssrc", ssrc)
		log.Infow("accepting RTP stream")
		// go p.rtpReadLoop(log, r)

		rc := rtpStreamCloser{ReadStream: r}
		_ = rc

		// go func() {
		// 	// - `in` implements io.ReadCloser, such as buffer or file
		// 	// - `mime` has to be one of webrtc.MimeType...
		// 	track, err := lksdk.NewLocalReaderTrack(rc, webrtc.MimeTypeH264,
		// 		lksdk.ReaderTrackWithFrameDuration(33*time.Millisecond),
		// 		lksdk.ReaderTrackWithOnWriteComplete(func() { fmt.Println("track finished") }),
		// 	)
		// 	if err != nil {
		// 		slog.Error("cannot create local track", "error", err)
		// 		return
		// 	}
		// 	if _, err = p.room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{}); err != nil {
		// 		slog.Error("cannot publish local track", "error", err)
		// 		return
		// 	}
		// }()

		go func() {
			addr := "127.0.0.1:5004"
			conn, err := net.Dial("udp", addr)
			if err != nil {
				panic(err)
			}
			defer conn.Close()

			buf := make([]byte, 1400) // payload buffer
			var h rtp.Header

			for {
				h = rtp.Header{}             // reset
				n, err := r.ReadRTP(&h, buf) // buf := payload only
				if err != nil {
					if err != io.EOF { /* log error */
					}
					return
				}

				pkt := &rtp.Packet{
					Header:  h,
					Payload: buf[:n],
				}
				raw, err := pkt.Marshal()
				if err != nil {
					/* log error */
					return
				}

				if _, err = conn.Write(raw); err != nil {
					/* log error */
					return
				}
			}
		}()
	}
}

func (p *MediaPort) rtpReadLoop(log logger.Logger, r rtp.ReadStream, hndp *atomic.Pointer[rtp.HandlerCloser]) {
	const maxErrors = 50 // 1 sec, given 20 ms frames
	buf := make([]byte, rtp.MTUSize+1)
	overflow := false
	var (
		h        rtp.Header
		pipeline string
		errorCnt int
	)
	r.L().Infow("starting RTP read loop")
	r.L().Infow("RTP handler", "handler", (*(hndp.Load())).String())
	defer r.L().Infow("RTP read loop ended")
	for {
		h = rtp.Header{}
		n, err := r.ReadRTP(&h, buf)
		if err == io.EOF {
			return
		} else if err != nil {
			log.Errorw("read RTP failed", err)
			return
		}
		p.packetCount.Add(1)
		p.stats.Packets.Add(1)
		if n > rtp.MTUSize {
			overflow = true
			if !overflow {
				log.Errorw("RTP packet is larger than MTU limit", nil, "payloadSize", n)
			}
			p.stats.IgnoredPackets.Add(1)
			r.L().Infow("RTP packet is larger than MTU limit, ignoring", "payloadSize", n)
			continue // ignore partial messages
		}

		ptr := hndp.Load()
		if ptr == nil {
			r.L().Infow("no RTP handler ptr, ignoring packet", "payloadSize", n, "rtpHeader", h)
			p.stats.IgnoredPackets.Add(1)
			continue
		}
		hnd := *ptr
		if hnd == nil {
			r.L().Infow("no RTP handler, ignoring packet", "payloadSize", n, "rtpHeader", h)
			p.stats.IgnoredPackets.Add(1)
			continue
		}
		p.stats.InputPackets.Add(1)
		err = hnd.HandleRTP(&h, buf[:n])
		if err != nil {
			if pipeline == "" {
				pipeline = hnd.String()
			}
			log := log.WithValues(
				"payloadSize", n,
				"rtpHeader", h,
				"pipeline", pipeline,
				"errorCount", errorCnt,
			)
			log.Infow("handle RTP failed", "error", err)
			r.L().Infow("handle RTP stream failed")
			errorCnt++
			if errorCnt >= maxErrors {
				log.Errorw("killing RTP loop due to persisted errors", err)
				return
			}
			continue
		}
		// r.L().Infow("handled RTP stream", "payloadSize", n, "rtpHeader", h)
		errorCnt = 0
		pipeline = ""
	}
}

// Must be called holding the lock
func (p *MediaPort) setupOutput() error {
	slog.Info("setting up media output")
	if p.closed.IsBroken() {
		return errors.New("media is already closed")
	}
	go p.rtpLoop(p.sess, &p.hnd)
	if p.vsess != nil && p.conf.Video != nil {
		slog.Info("setting up video output")
		go p.rtpLoop(p.vsess, &p.vhnd)

		// go p.rtpLoopVideo(p.vsess)

		w, err := p.vsess.OpenWriteStream()
		if err != nil {
			return err
		}
		s := rtp.NewSeqWriter(newRTPStatsWriter(p.mon, "video", w))

		if p.conf.Video.Codec.Info().RTPClockRate != 90000 {
			slog.Error("video codec must use 90kHz clock", "codec", p.conf.Video.Codec.Info().SDPName, "clock", p.conf.Video.Codec.Info().RTPClockRate)
			panic("video codec must use 90kHz clock")
		}

		p.videoOutRTP = s.NewStream(p.conf.Video.Type, 90000)
		// For now, we'll use a simple pass-through for video
		// In a real implementation, you'd want proper H.264 encoding
		videoOut := p.conf.Video.Codec.(rtp.VideoCodec).EncodeRTP(p.videoOutRTP)
		//! webrtc to sip
		if pw := p.videoOut.Swap(videoOut); pw != nil {
			_ = pw.Close()
		}
	}
	w, err := p.sess.OpenWriteStream()
	if err != nil {
		return err
	}

	// TODO: this says "audio", but actually includes DTMF too
	s := rtp.NewSeqWriter(newRTPStatsWriter(p.mon, "audio", w))
	p.audioOutRTP = s.NewStream(p.conf.Audio.Type, p.conf.Audio.Codec.Info().RTPClockRate)

	// Encoding pipeline (LK PCM -> SIP RTP)
	audioOut := p.conf.Audio.Codec.(rtp.AudioCodec).EncodeRTP(p.audioOutRTP)

	if p.conf.Audio.DTMFType != 0 {
		p.dtmfOutRTP = s.NewStream(p.conf.Audio.DTMFType, dtmf.SampleRate)
		if p.dtmfAudioEnabled {
			// Add separate mixer for DTMF audio.
			// TODO: optimize, if we'll ever need this code path
			mix, err := mixer.NewMixer(audioOut, rtp.DefFrameDur, nil, 1, mixer.DefaultInputBufferFrames)
			if err != nil {
				return err
			}
			audioOut = mix.NewInput()
			p.dtmfOutAudio = mix.NewInput()
		}
	}

	if w := p.audioOut.Swap(audioOut); w != nil {
		_ = w.Close()
	}
	return nil
}

type DebugHandler struct {
	rtp.Handler
	conn net.Conn
}

var _ rtp.Handler = (*DebugHandler)(nil)

func NewDebugHandler(h rtp.Handler) *DebugHandler {
	addr := "127.0.0.1:5004"
	conn, err := net.Dial("udp", addr)
	if err != nil {
		panic(err)
	}
	return &DebugHandler{
		Handler: h,
		conn:    conn,
	}
}

func (d DebugHandler) HandleRTP(h *rtp.Header, payload []byte) error {

	pkt := &rtp.Packet{
		Header:  *h,
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		slog.Error("cannot marshal RTP packet", "error", err)
	} else {
		if _, err = d.conn.Write(raw); err != nil {
			slog.Error("cannot write to debug conn", "error", err)
		}
	}

	return d.Handler.HandleRTP(h, payload)
}

func (d DebugHandler) String() string {
	return "debugHandler"
}

func (p *MediaPort) setupInput() {
	// Decoding pipeline (SIP RTP -> LK PCM)
	audioHandler := p.conf.Audio.Codec.(rtp.AudioCodec).DecodeRTP(p.audioIn, p.conf.Audio.Type)
	p.audioInHandler = audioHandler

	mux := rtp.NewMux(nil)
	mux.SetDefault(newRTPStatsHandler(p.mon, "", nil))
	mux.Register(
		p.conf.Audio.Type, newRTPHandlerCount(
			newRTPStatsHandler(p.mon, p.conf.Audio.Codec.Info().SDPName, audioHandler),
			&p.stats.AudioPackets, &p.stats.AudioBytes,
		),
	)

	// Setup video input if video is configured
	if p.conf.Video != nil {
		slog.Info("setting up video input")

		// videoHandler := p.conf.Video.Codec.(rtp.VideoCodec).DecodeRTP(p.videoIn, p.conf.Video.Type)
		videoHandler := p.conf.Video.Codec.(rtp.VideoCodec).DecodeRTP(p.videoIn, p.conf.Video.Type)

		// videoHandler := rtp.NewMediaStreamIn[msdk.FrameSample](p.videoIn)
		p.videoInHandler = videoHandler

		// vmux := rtp.NewMux(nil)
		// vmux.SetDefault(newRTPStatsHandler(p.mon, "", nil))
		// vmux.Register(
		// 	p.conf.Video.Type, newRTPHandlerCount(
		// 		newRTPStatsHandler(p.mon, p.conf.Video.Codec.Info().SDPName, videoHandler),
		// 		&p.stats.VideoPackets, &p.stats.VideoBytes,
		// 	),
		// )

		// vhnd := rtp.NewNopCloser(newRTPHandlerCount(vmux, &p.stats.MuxPackets, &p.stats.MuxBytes))
		// vhnd := rtp.NewNopCloser(newRTPHandlerCount(
		// 	newRTPStatsHandler(p.mon, p.conf.Video.Codec.Info().SDPName, videoHandler),
		// 	&p.stats.VideoPackets, &p.stats.VideoBytes,
		// ))

		vhnd := rtp.NewNopCloser(videoHandler)

		p.vhnd.Store(&vhnd)

		// mux.Register(
		// 	p.conf.Video.Type, newRTPHandlerCount(
		// 		newRTPStatsHandler(p.mon, p.conf.Video.Codec.Info().SDPName, videoHandler),
		// 		&p.stats.VideoPackets, &p.stats.VideoBytes,
		// 	),
		// )
	}
	if p.conf.Audio.DTMFType != 0 {
		mux.Register(
			p.conf.Audio.DTMFType, newRTPHandlerCount(
				newRTPStatsHandler(p.mon, dtmf.SDPName, rtp.HandlerFunc(func(h *rtp.Header, payload []byte) error {
					ptr := p.dtmfIn.Load()
					if ptr == nil {
						return nil
					}
					fnc := *ptr
					if ev, ok := dtmf.DecodeRTP(h, payload); ok && fnc != nil {
						fnc(ev)
					}
					return nil
				})),
				&p.stats.DTMFPackets, &p.stats.DTMFBytes,
			),
		)
	}
	var hnd rtp.HandlerCloser = rtp.NewNopCloser(newRTPHandlerCount(mux, &p.stats.MuxPackets, &p.stats.MuxBytes))
	if p.jitterEnabled {
		hnd = rtp.HandleJitter(hnd)
	}
	p.hnd.Store(&hnd)
}

// SetDTMFAudio forces SIP to generate audio dTMF tones in addition to digital signals.
func (p *MediaPort) SetDTMFAudio(enabled bool) {
	p.dtmfAudioEnabled = enabled
}

// HandleDTMF sets an incoming DTMF handler.
func (p *MediaPort) HandleDTMF(h func(ev dtmf.Event)) {
	if h == nil {
		p.dtmfIn.Store(nil)
	} else {
		p.dtmfIn.Store(&h)
	}
}

func (p *MediaPort) WriteDTMF(ctx context.Context, digits string) error {
	if len(digits) == 0 {
		return nil
	}
	p.mu.Lock()
	dtmfOut := p.dtmfOutRTP
	audioOut := p.dtmfOutAudio
	audioOutRTP := p.audioOutRTP
	p.mu.Unlock()
	if !p.dtmfAudioEnabled {
		audioOut = nil
	}
	if dtmfOut == nil && audioOut == nil {
		return nil
	}

	var rtpTs uint32
	if audioOutRTP != nil {
		rtpTs = audioOutRTP.GetCurrentTimestamp()
	}

	return dtmf.Write(ctx, audioOut, dtmfOut, rtpTs, digits)
}
