package sip

import (
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/frostbyte73/core"
	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/stats"
	"github.com/pkg/errors"
)

// func NewVideoConn(c *net.UDPConn) (*videoConn, error) {
// 	return &videoConn{
// 		conn: c,
// 	}, nil
// }

// type videoConn struct {
// 	locked atomic.Bool
// 	conn   *net.UDPConn
// 	port   uint16
// }

// func (vc *videoConn) Port() uint16 {
// 	if !vc.locked.Load() {
// 		return uint16(vc.conn.LocalAddr().(*net.UDPAddr).Port)
// 	}
// 	return vc.port
// }

// func (vc *videoConn) Release() uint16 {
// 	if !vc.locked.Swap(true) {
// 		vc.port = uint16(vc.conn.LocalAddr().(*net.UDPAddr).Port)
// 		vc.conn.Close()
// 	}
// 	return vc.port
// }

type VideoPort struct {
	mu            sync.Mutex
	log           logger.Logger
	opts          *MediaOptions
	conf          *MediaConf
	mon           *stats.CallMonitor
	externalIP    netip.Addr
	conn          *udpConn
	mediaTimeout  chan struct{}
	videoIn       *rtp.WriteStreamSwitcher
	videoOut      *rtp.WriteStreamSwitcher
	sess          rtp.Session
	closed        core.Fuse
	mediaReceived core.Fuse
}

func NewVideoPort(log logger.Logger, mon *stats.CallMonitor, conn UDPConn, opts *MediaOptions) (*VideoPort, error) {
	mediaTimeout := make(chan struct{})

	if conn == nil {
		c, err := rtp.ListenUDPPortRange(opts.Ports.Start, opts.Ports.End, netip.AddrFrom4([4]byte{0, 0, 0, 0}))
		if err != nil {
			return nil, err
		}
		conn = c
	}

	log = log.WithValues("mediaType", "video", "localAddr", conn.LocalAddr())

	p := &VideoPort{
		log:          log,
		mon:          mon,
		opts:         opts,
		externalIP:   opts.IP,
		mediaTimeout: mediaTimeout,
		videoIn:      rtp.NewWriteStreamSwitcher(),
		videoOut:     rtp.NewWriteStreamSwitcher(),
		conn:         newUDPConn(log, conn),
	}
	return p, nil
}

func (p *VideoPort) Close() {
	p.closed.Once(func() {
		p.mu.Lock()
		defer p.mu.Unlock()

		close(p.mediaTimeout)

		p.videoIn.Close()
		p.videoOut.Close()
	})
}

func (p *VideoPort) Port() int {
	return p.conn.LocalAddr().(*net.UDPAddr).Port
}

func (p *VideoPort) WriteVideoTo(w rtp.WriteStreamCloser) {
	if vw := p.videoIn.Swap(w); vw != nil {
		_ = vw.Close()
	}
	p.log.Infow("video writer set", "videoWriter", p.videoIn.String())
}

func (p *VideoPort) GetVideoWriter() rtp.WriteStreamCloser {
	return p.videoOut
}

func (p *VideoPort) SetConfig(c *MediaConf) error {

	if c.Video == nil {
		p.log.Infow("no video config provided")
		return nil
	}

	// TODO: handle crypto

	p.mu.Lock()
	defer p.mu.Unlock()

	p.conn.SetDst(c.Video.Remote)

	// go func() {
	// 	slog.Info("test conn read")
	// 	buf := make([]byte, 1500)
	// 	i, err := p.conn.Read(buf) // prime the conn
	// 	slog.Info("test conn read done", "n", i, "err", err)
	// }()

	sess := rtp.NewSession(p.log.WithValues("sess-media", "video"), p.conn)

	p.conf = c
	p.sess = sess

	if err := p.setupInput(); err != nil {
		return err
	}

	if err := p.setupOutput(); err != nil {
		return err
	}

	return nil
}

func (p *VideoPort) setupInput() error {
	if p.closed.IsBroken() {
		return errors.New("media is already closed")
	}

	go p.rtpLoop(p.sess, p.videoIn)

	// TODO: handle crypto

	// TODO: gst pipeline

	return nil
}

func (p *VideoPort) rtpLoop(sess rtp.Session, w rtp.WriteStream) {
	// Need a loop to process all incoming packets.
	for {
		p.log.Debugw("waiting to accept RTP stream")
		r, ssrc, err := sess.AcceptStream()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "closed") {
				p.log.Errorw("cannot accept RTP stream", err)
			} else {
				p.log.Infow("RTP session closed")
			}
			return
		}
		p.log.Debugw("RTP stream accepted", "ssrc", ssrc)
		// p.stats.Streams.Add(1)
		p.mediaReceived.Break()
		log := p.log.WithValues("ssrc", ssrc)
		log.Infow("accepting RTP stream")
		go p.rtpReadLoop(log, r, w)
	}
}

func (p *VideoPort) rtpReadLoop(log logger.Logger, r rtp.ReadStream, w rtp.WriteStream) {
	const maxErrors = 50 // 1 sec, given 20 ms frames
	buf := make([]byte, rtp.MTUSize+1)
	overflow := false
	var (
		h        rtp.Header
		pipeline string
		errorCnt int
	)
	log.Infow("starting RTP read loop")
	for {
		h = rtp.Header{}
		n, err := r.ReadRTP(&h, buf)
		if err == io.EOF {
			return
		} else if err != nil {
			log.Errorw("read RTP failed", err)
			return
		}
		// p.packetCount.Add(1)
		// p.stats.Packets.Add(1)
		if n > rtp.MTUSize {
			overflow = true
			if !overflow {
				log.Errorw("RTP packet is larger than MTU limit", nil, "payloadSize", n)
			}
			// p.stats.IgnoredPackets.Add(1)
			continue // ignore partial messages
		}
		// p.stats.InputPackets.Add(1)
		i, err := w.WriteRTP(&h, buf[:n])
		if err != nil {
			if pipeline == "" {
				pipeline = p.videoIn.String()
			}
			log := log.WithValues(
				"payloadSize", n,
				"rtpHeader", h,
				"pipeline", pipeline,
				"errorCount", errorCnt,
				"writeCount", i,
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

func (p *VideoPort) setupOutput() error {

	// w, err := p.sess.OpenWriteStream()
	// if err != nil {
	// 	return err
	// }

	// wc := rtp.NewStreamNopCloser(w)

	// if w := p.videoOut.Swap(wc); w != nil {
	// 	_ = w.Close()
	// }
	return nil
}
