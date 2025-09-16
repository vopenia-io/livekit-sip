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
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"

	msdk "github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/dtmf"
	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/media-sdk/sdp"
	"github.com/livekit/media-sdk/srtp"
	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/logger"
	prtp "github.com/pion/rtp"
	psdp "github.com/pion/sdp/v3"

	"github.com/livekit/sip/pkg/mixer"
	"github.com/livekit/sip/pkg/stats"
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

	DTMFPackets atomic.Uint64
	DTMFBytes   atomic.Uint64

	VideoPackets    atomic.Uint64
	VideoBytes      atomic.Uint64
	VideoOutPackets atomic.Uint64
	VideoOutBytes   atomic.Uint64
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
	Video     *VideoConfig
	Processor msdk.PCM16Processor
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
		opts.MediaTimeoutInitial = 30 * time.Second
	}
	if opts.MediaTimeout <= 0 {
		opts.MediaTimeout = 15 * time.Second
	}
	if opts.Stats == nil {
		opts.Stats = &PortStats{}
	}
	if conn == nil {
		c, err := rtp.ListenUDPPortRange(opts.Ports.Start, opts.Ports.End, netip.AddrFrom4([4]byte{0, 0, 0, 0}))
		if err != nil {
			return nil, err
		}
		conn = c
	}
	mediaTimeout := make(chan struct{})
	p := &MediaPort{
		log:           log,
		opts:          opts,
		mon:           mon,
		externalIP:    opts.IP,
		mediaTimeout:  mediaTimeout,
		timeoutReset:  make(chan struct{}, 1),
		jitterEnabled: opts.EnableJitterBuffer,
		port:          newUDPConn(log, conn),
		audioOut:      msdk.NewSwitchWriter(sampleRate),
		audioIn:       msdk.NewSwitchWriter(sampleRate),
		stats:         opts.Stats,
	}
	go p.timeoutLoop(func() {
		close(mediaTimeout)
	})
	p.log.Debugw("listening for media on UDP", "port", p.Port())
	return p, nil
}

// MediaPort combines all functionality related to sending and accepting SIP media.
type MediaPort struct {
	log              logger.Logger
	opts             *MediaOptions
	mon              *stats.CallMonitor
	externalIP       netip.Addr
	port             *udpConn
	mediaReceived    core.Fuse
	packetCount      atomic.Uint64
	mediaTimeout     <-chan struct{}
	timeoutStart     atomic.Pointer[time.Time]
	timeoutReset     chan struct{}
	closed           core.Fuse
	stats            *PortStats
	dtmfAudioEnabled bool
	jitterEnabled    bool

	mu           sync.Mutex
	conf         *MediaConf
	offer        *sdp.Offer
	sess         rtp.Session
	hnd          atomic.Pointer[rtp.HandlerCloser]
	dtmfOutRTP   *rtp.Stream
	dtmfOutAudio msdk.PCM16Writer

	audioOutRTP    *rtp.Stream
	audioOut       *msdk.SwitchWriter // LK PCM -> SIP RTP
	audioIn        *msdk.SwitchWriter // SIP RTP -> LK PCM
	audioInHandler rtp.Handler        // for debug only

	videoOut   rtp.WriteStream
	videoConf  *VideoConfig
	videoIn    atomic.Pointer[rtp.HandlerCloser]
	videoOffer []videoCodecOffer

	dtmfIn atomic.Pointer[func(ev dtmf.Event)]
}

func (p *MediaPort) DisableOut() {
	p.audioOut.Disable()
}

func (p *MediaPort) EnableOut() {
	p.audioOut.Enable()
}

func (p *MediaPort) EnableTimeout(enabled bool) {
	if !enabled {
		p.timeoutStart.Store(nil)
		return
	}
	select {
	case p.timeoutReset <- struct{}{}:
	default:
	}
	now := time.Now()
	p.timeoutStart.Store(&now)
	p.log.Infow("media timeout enabled",
		"packets", p.packetCount.Load(),
	)
}

func (p *MediaPort) timeoutLoop(timeoutCallback func()) {
	tickInterval := p.opts.MediaTimeout
	ticker := time.NewTicker(tickInterval)
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
		case <-p.timeoutReset:
			ticker.Reset(tickInterval)
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

			// First timeout is allowed to be longer. Skip ticks if it's too early.
			sinceStart := time.Since(*startPtr)
			if lastPackets == startPackets && sinceStart < p.opts.MediaTimeoutInitial {
				continue
			}

			// Ticker is allowed to fire earlier than the full timeout interval. Skip if it's not a full timeout yet.
			sinceLast := time.Since(lastTime)
			if sinceLast < p.opts.MediaTimeout {
				continue
			}
			p.log.Infow("triggering media timeout",
				"packets", lastPackets,
				"startPackets", startPackets,
				"sinceStart", sinceStart,
				"sinceLast", sinceLast,
				"initial", p.opts.MediaTimeoutInitial,
				"timeout", p.opts.MediaTimeout,
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
		p.audioOutRTP = nil
		p.audioInHandler = nil
		p.dtmfOutRTP = nil
		if p.dtmfOutAudio != nil {
			p.dtmfOutAudio.Close()
			p.dtmfOutAudio = nil
		}
		p.dtmfIn.Store(nil)
		prevVideo := p.videoIn.Swap(nil)
		if prevVideo != nil && *prevVideo != nil {
			(*prevVideo).Close()
		}
		p.videoOut = nil
		p.videoConf = nil
		p.videoOffer = nil
		p.offer = nil
		if p.sess != nil {
			_ = p.sess.Close()
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

// WriteAudioTo sets audio writer that will receive decoded PCM from incoming RTP packets.
func (p *MediaPort) WriteAudioTo(w msdk.PCM16Writer) {
	if p == nil {
		return
	}

	var processor msdk.PCM16Processor
	if w != nil {
		p.mu.Lock()
		if p.conf != nil {
			processor = p.conf.Processor
		}
		p.mu.Unlock()
		if processor != nil {
			w = processor(w)
		}
	}

	if pw := p.audioIn.Swap(w); pw != nil {
		_ = pw.Close()
	}
}

// GetAudioWriter returns audio writer that will send PCM to the destination via RTP.
func (p *MediaPort) GetAudioWriter() msdk.PCM16Writer {
	return p.audioOut
}

// NewOffer generates an SDP offer for the media.
func (p *MediaPort) NewOffer(encrypted sdp.Encryption) (*sdp.Offer, error) {
	offer, err := sdp.NewOffer(p.externalIP, p.Port(), encrypted)
	if err != nil {
		return nil, err
	}
	p.offer = offer
	p.videoOffer = nil

	cryptoProfiles := offer.MediaDesc.CryptoProfiles
	encryptedVideo := encrypted != sdp.EncryptionNone && len(cryptoProfiles) > 0
	offers, video := offerVideoDescription(p.Port(), cryptoProfiles, encryptedVideo)
	if video != nil {
		offer.SDP.MediaDescriptions = append(offer.SDP.MediaDescriptions, video)
		p.videoOffer = offers
	}
	return offer, nil
}

// SetAnswer decodes and applies SDP answer for offer from NewOffer. SetConfig must be called with the decoded configuration.
func (p *MediaPort) SetAnswer(offer *sdp.Offer, answerData []byte, enc sdp.Encryption) (*MediaConf, error) {
	answer, err := sdp.ParseAnswer(answerData)
	if err != nil {
		return nil, err
	}
	audio, err := sdp.SelectAudio(answer.MediaDesc, true)
	if err != nil {
		return nil, err
	}

	var sconf *srtp.Config
	if enc != sdp.EncryptionNone {
		sconf, _, err = sdp.SelectCrypto(offer.CryptoProfiles, answer.MediaDesc.CryptoProfiles, false)
		if err != nil {
			return nil, err
		}
	}
	if sconf == nil && enc == sdp.EncryptionRequire {
		return nil, sdp.ErrNoCommonCrypto
	}

	conf := &MediaConf{MediaConfig: sdp.MediaConfig{
		Local:  offer.Addr,
		Remote: answer.Addr,
		Audio:  *audio,
		Crypto: sconf,
	}}

	if md := findVideoDescription(&answer.SDP); md != nil {
		if cfg := mapVideoAnswer(md, p.videoOffer); cfg != nil {
			conf.Video = cfg
		}
	}

	return conf, nil
}

// SetOffer decodes the offer from another party and returns encoded answer. To accept the offer, call SetConfig.
func (p *MediaPort) SetOffer(offerData []byte, enc sdp.Encryption) (*sdp.Answer, *MediaConf, error) {
	offer, err := sdp.ParseOffer(offerData)
	if err != nil {
		return nil, nil, err
	}
	p.videoOffer = nil
	audio, err := sdp.SelectAudio(offer.MediaDesc, false)
	if err != nil {
		return nil, nil, err
	}

	var (
		sconf          *srtp.Config
		sprof          *srtp.Profile
		answerProfiles []srtp.Profile
	)
	if len(offer.CryptoProfiles) != 0 && enc != sdp.EncryptionNone {
		answerProfiles, err = srtp.DefaultProfiles()
		if err != nil {
			return nil, nil, err
		}
		sconf, sprof, err = sdp.SelectCrypto(offer.CryptoProfiles, answerProfiles, true)
		if err != nil {
			return nil, nil, err
		}
	}
	if sprof == nil && enc == sdp.EncryptionRequire {
		return nil, nil, sdp.ErrNoCommonCrypto
	}

	port := p.Port()
	audioAnswer := sdp.AnswerMedia(port, audio, sprof)

	encryptedVideo := enc != sdp.EncryptionNone && len(answerProfiles) > 0
	answerSDP := psdp.SessionDescription{
		Version: 0,
		Origin: psdp.Origin{
			Username:       "-",
			SessionID:      offer.SDP.Origin.SessionID,
			SessionVersion: offer.SDP.Origin.SessionID + 2,
			NetworkType:    "IN",
			AddressType:    "IP4",
			UnicastAddress: p.externalIP.String(),
		},
		SessionName: "LiveKit",
		ConnectionInformation: &psdp.ConnectionInformation{
			NetworkType: "IN",
			AddressType: "IP4",
			Address:     &psdp.Address{Address: p.externalIP.String()},
		},
		TimeDescriptions:  []psdp.TimeDescription{{Timing: psdp.Timing{StartTime: 0, StopTime: 0}}},
		MediaDescriptions: []*psdp.MediaDescription{audioAnswer},
	}

	var videoConf *VideoConfig
	if md := findVideoDescription(&offer.SDP); md != nil {
		if cfg, ok := parseVideoOffer(md); ok {
			videoConf = cfg
			if desc := answerVideoDescription(port, cfg, answerProfiles, encryptedVideo); desc != nil {
				answerSDP.MediaDescriptions = append(answerSDP.MediaDescriptions, desc)
			}
		}
	}

	answer := &sdp.Answer{
		SDP:  answerSDP,
		Addr: netip.AddrPortFrom(p.externalIP, uint16(port)),
		MediaDesc: sdp.MediaDesc{
			Codecs:         []sdp.CodecInfo{{Type: audio.Type, Codec: audio.Codec}},
			DTMFType:       audio.DTMFType,
			CryptoProfiles: answerProfiles,
		},
	}

	conf := &MediaConf{MediaConfig: sdp.MediaConfig{
		Local:  netip.AddrPortFrom(p.externalIP, uint16(port)),
		Remote: offer.Addr,
		Audio:  *audio,
		Crypto: sconf,
	}, Video: videoConf}

	return answer, conf, nil
}

func (p *MediaPort) SetConfig(c *MediaConf) error {
	if p.closed.IsBroken() {
		return errors.New("media is already closed")
	}
	var crypto string
	if c.Crypto != nil {
		crypto = c.Crypto.Profile.String()
	}
	p.log.Infow("using codecs",
		"audio-codec", c.Audio.Codec.Info().SDPName, "audio-rtp", c.Audio.Type,
		"dtmf-rtp", c.Audio.DTMFType,
		"srtp", crypto,
	)
	if vc := c.Video; vc != nil {
		p.log.Infow("using video codec",
			"video-codec", vc.CodecName,
			"video-rtp", vc.Type,
			"video-direction-send", vc.Direction.Send,
			"video-direction-recv", vc.Direction.Recv,
		)
	}

	p.port.SetDst(c.Remote)
	var (
		sess rtp.Session
		err  error
	)
	if c.Crypto != nil {
		sess, err = srtp.NewSession(p.log, p.port, c.Crypto)
	} else {
		sess = rtp.NewSession(p.log, p.port)
	}
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.port.SetDst(c.Remote)
	p.conf = c
	p.videoConf = c.Video
	p.sess = sess

	if err = p.setupOutput(); err != nil {
		return err
	}
	p.setupInput()
	return nil
}

func (p *MediaPort) rtpLoop(sess rtp.Session) {
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
		go p.rtpReadLoop(log, r)
	}
}

func (p *MediaPort) rtpReadLoop(log logger.Logger, r rtp.ReadStream) {
	const maxErrors = 50 // 1 sec, given 20 ms frames
	buf := make([]byte, rtp.MTUSize+1)
	overflow := false
	var (
		h        rtp.Header
		pipeline string
		errorCnt int
	)
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
			if !overflow {
				log.Errorw("RTP packet is larger than MTU limit", nil, "payloadSize", n)
				overflow = true
			}
			p.stats.IgnoredPackets.Add(1)
			continue // ignore partial messages
		}
		overflow = false

		ptr := p.hnd.Load()
		if ptr == nil {
			p.stats.IgnoredPackets.Add(1)
			continue
		}
		hnd := *ptr
		if hnd == nil {
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
			log.Debugw("handle RTP failed", "error", err)
			errorCnt++
			if errorCnt >= maxErrors {
				log.Errorw("killing RTP loop due to persisted errors", err)
				return
			}
			continue
		}
		errorCnt = 0
		pipeline = ""
	}
}

// Must be called holding the lock
func (p *MediaPort) setupOutput() error {
	if p.closed.IsBroken() {
		return errors.New("media is already closed")
	}
	go p.rtpLoop(p.sess)
	w, err := p.sess.OpenWriteStream()
	if err != nil {
		return err
	}

	// TODO: this says "audio", but actually includes DTMF too
	s := rtp.NewSeqWriter(newRTPStatsWriter(p.mon, "audio", w))
	p.audioOutRTP = s.NewStream(p.conf.Audio.Type, p.conf.Audio.Codec.Info().RTPClockRate)

	// Encoding pipeline (LK PCM -> SIP RTP)
	audioOut := p.conf.Audio.Codec.EncodeRTP(p.audioOutRTP)

	if p.conf.Audio.DTMFType != 0 {
		p.dtmfOutRTP = s.NewStream(p.conf.Audio.DTMFType, dtmf.SampleRate)
		if p.dtmfAudioEnabled {
			// Add separate mixer for DTMF audio.
			// TODO: optimize, if we'll ever need this code path
			mix := mixer.NewMixer(audioOut, rtp.DefFrameDur, nil)
			audioOut = mix.NewInput()
			p.dtmfOutAudio = mix.NewInput()
		}
	}

	if w := p.audioOut.Swap(audioOut); w != nil {
		_ = w.Close()
	}

	if vc := p.videoConf; vc != nil && vc.Direction.Recv {
		p.videoOut = newRTPStatsWriter(p.mon, "video", w)
	} else {
		p.videoOut = nil
	}
	return nil
}

func (p *MediaPort) setupInput() {
	// Decoding pipeline (SIP RTP -> LK PCM)
	audioHandler := p.conf.Audio.Codec.DecodeRTP(p.audioIn, p.conf.Audio.Type)
	p.audioInHandler = audioHandler

	mux := rtp.NewMux(nil)
	mux.SetDefault(newRTPStatsHandler(p.mon, "", nil))
	mux.Register(
		p.conf.Audio.Type, newRTPHandlerCount(
			newRTPStatsHandler(p.mon, p.conf.Audio.Codec.Info().SDPName, audioHandler),
			&p.stats.AudioPackets, &p.stats.AudioBytes,
		),
	)
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
	if vc := p.videoConf; vc != nil && vc.Direction.Send {
		statsName := strings.ToLower(vc.CodecName)
		mux.Register(
			vc.Type, newRTPHandlerCount(
				newRTPStatsHandler(p.mon, statsName, rtp.HandlerFunc(func(h *rtp.Header, payload []byte) error {
					ptr := p.videoIn.Load()
					if ptr == nil {
						return nil
					}
					hnd := *ptr
					if hnd == nil {
						return nil
					}
					return hnd.HandleRTP(h, payload)
				})),
				&p.stats.VideoPackets, &p.stats.VideoBytes,
			),
		)
	} else {
		prev := p.videoIn.Swap(nil)
		if prev != nil && *prev != nil {
			(*prev).Close()
		}
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

func (p *MediaPort) SetVideoHandler(h rtp.HandlerCloser) {
	if p == nil {
		if h != nil {
			h.Close()
		}
		return
	}
	p.mu.Lock()
	vc := p.videoConf
	p.mu.Unlock()
	if vc == nil || !vc.Direction.Send {
		if h != nil {
			h.Close()
		}
		prev := p.videoIn.Swap(nil)
		if prev != nil && *prev != nil {
			(*prev).Close()
		}
		return
	}
	if h == nil {
		prev := p.videoIn.Swap(nil)
		if prev != nil && *prev != nil {
			(*prev).Close()
		}
		return
	}
	ptr := &h
	prev := p.videoIn.Swap(ptr)
	if prev != nil && *prev != nil {
		(*prev).Close()
	}
}

func (p *MediaPort) WriteVideoRTP(h *prtp.Header, payload []byte) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	writer := p.videoOut
	vc := p.videoConf
	p.mu.Unlock()
	if writer == nil || vc == nil || !vc.Direction.Recv {
		return nil
	}
	header := *h
	header.PayloadType = vc.Type
	if _, err := writer.WriteRTP(&header, payload); err != nil {
		return err
	}
	p.stats.VideoOutPackets.Add(1)
	p.stats.VideoOutBytes.Add(uint64(len(payload)))
	return nil
}

func (p *MediaPort) VideoConfig() *VideoConfig {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.videoConf
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
