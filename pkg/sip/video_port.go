package sip

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/stats"
	"github.com/pkg/errors"
)

type VideoPort struct {
	mu            sync.Mutex
	log           logger.Logger
	opts          *MediaOptions
	conf          *MediaConf
	mon           *stats.CallMonitor
	externalIP    netip.Addr
	rtpConn       *udpConn
	rtcpConn      *udpConn
	mediaTimeout  chan struct{}
	videoIn       *rtp.WriteStreamSwitcher // -> webrtc track
	videoOut      *rtp.WriteStreamSwitcher // <- webrtc track
	sess          rtp.Session
	rtcpsess      rtp.Session
	vp            *VideoPipeline
	closed        core.Fuse
	mediaReceived core.Fuse
}

func NewVideoPort(log logger.Logger, mon *stats.CallMonitor, rtcConn UDPConn, rtcpConn UDPConn, opts *MediaOptions) (*VideoPort, error) {
	mediaTimeout := make(chan struct{})

	if rtcConn == nil {
		c, err := rtp.ListenUDPPortRange(opts.Ports.Start, opts.Ports.End, netip.AddrFrom4([4]byte{0, 0, 0, 0}))
		if err != nil {
			return nil, err
		}
		rtcConn = c
	}

	if rtcpConn == nil {
		c, err := rtp.ListenUDPPortRange(opts.Ports.Start, opts.Ports.End, netip.AddrFrom4([4]byte{0, 0, 0, 0}))
		if err != nil {
			return nil, err
		}
		rtcpConn = c
	}

	log = log.WithValues("mediaType", "video", "localAddr", rtcConn.LocalAddr())

	p := &VideoPort{
		log:          log,
		mon:          mon,
		opts:         opts,
		externalIP:   opts.IP,
		mediaTimeout: mediaTimeout,
		videoIn:      rtp.NewWriteStreamSwitcher(),
		videoOut:     rtp.NewWriteStreamSwitcher(),
		rtpConn:      newUDPConn(log, rtcConn),
		rtcpConn:     newUDPConn(log, rtcpConn),
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

		if p.vp != nil {
			_ = p.vp.Close()
			p.vp = nil
		}

		if p.sess != nil {
			_ = p.sess.Close()
			p.sess = nil
		}
		if p.rtcpsess != nil {
			_ = p.rtcpsess.Close()
			p.rtcpsess = nil
		}
		if p.rtpConn != nil {
			_ = p.rtpConn.Close()
			p.rtpConn = nil
		}
		if p.rtcpConn != nil {
			_ = p.rtcpConn.Close()
			p.rtcpConn = nil
		}
	})
}

func (p *VideoPort) RTPPort() int {
	return p.rtpConn.LocalAddr().(*net.UDPAddr).Port
}

func (p *VideoPort) RTCPPort() int {
	return p.rtcpConn.LocalAddr().(*net.UDPAddr).Port
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

	p.rtpConn.SetDst(c.Video.Remote)

	p.conf = c

	p.sess = rtp.NewSession(p.log.WithValues("sess-media", "video"), p.rtpConn)

	p.rtcpsess = rtp.NewSession(p.log.WithValues("sess-media", "video-rtcp"), p.rtcpConn)

	if err := p.setupIO(); err != nil {
		return err
	}

	return nil
}

func writerFromPipeline(pipeline *gst.Pipeline, name string) (*GstWriteRTP, error) {
	element, err := pipeline.GetElementByName(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get %s element: %w", name, err)
	}
	src := app.SrcFromElement(element)
	writer, err := NewGstWriteRTP(src)
	if err != nil {
		return nil, fmt.Errorf("failed to create GST write RTP for %s: %w", name, err)
	}
	return writer, nil
}

func readerFromPipeline(pipeline *gst.Pipeline, name string) (*GstReadRTP, error) {
	element, err := pipeline.GetElementByName(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get %s element: %w", name, err)
	}
	sink := app.SinkFromElement(element)
	reader, err := NewGstReadRTP(sink)
	if err != nil {
		return nil, fmt.Errorf("failed to create GST read RTP for %s: %w", name, err)
	}
	return reader, nil
}

func NewVideoPipeline(pipeline *gst.Pipeline, sipRtpOut, sipRtcpOut, webrtcOut rtp.WriteStreamCloser) (*VideoPipeline, error) {
	var err error
	v := &VideoPipeline{
		ctx: context.Background(),
	}

	gpr := NewGstPipelineRunner(v.ctx, pipeline)
	v.runner = gpr

	v.SipRtpIn, err = writerFromPipeline(pipeline, "sip_rtp_in")
	if err != nil {
		return nil, fmt.Errorf("failed to create GST writer for sip_rtp_in: %w", err)
	}

	// v.SipRtcpIn, err = writerFromPipeline(pipeline, "sip_rtcp_in")
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create GST writer for sip_rtcp_in: %w", err)
	// }

	sipRtpOutReader, err := readerFromPipeline(pipeline, "sip_rtp_out")
	if err != nil {
		return nil, fmt.Errorf("failed to create GST reader for sip_rtp_out: %w", err)
	}

	if err := gpr.NewSinkWriter(sipRtpOutReader, sipRtpOut); err != nil {
		return nil, fmt.Errorf("failed to create GST sink writer for sip_out: %w", err)
	}

	// sipRtcpOutReader, err := readerFromPipeline(pipeline, "sip_rtcp_out")
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create GST reader for sip_rtcp_out: %w", err)
	// }

	// if err := gpr.NewSinkWriter(sipRtcpOutReader, sipRtcpOut); err != nil {
	// 	return nil, fmt.Errorf("failed to create GST sink writer for sip_rtcp_out: %w", err)
	// }

	v.WebrtcIn, err = writerFromPipeline(pipeline, "webrtc_in")
	if err != nil {
		return nil, fmt.Errorf("failed to create GST writer for webrtc_in: %w", err)
	}

	webrtcOutReader, err := readerFromPipeline(pipeline, "webrtc_out")
	if err != nil {
		return nil, fmt.Errorf("failed to create GST reader for webrtc_out: %w", err)
	}

	if err := gpr.NewSinkWriter(webrtcOutReader, webrtcOut); err != nil {
		return nil, fmt.Errorf("failed to create GST sink writer for webrtc_out: %w", err)
	}

	return v, nil
}

type VideoPipeline struct {
	ctx       context.Context
	runner    *GstPipelineRunner
	SipRtpIn  rtp.WriteStreamCloser
	SipRtcpIn rtp.WriteStreamCloser
	WebrtcIn  rtp.WriteStreamCloser
}

func (v *VideoPipeline) Start() error {
	return v.runner.Run(v.ctx)
}

func (v *VideoPipeline) Close() error {
	return v.runner.Close()
}

func (p *VideoPort) setupIO() error {
	if p.closed.IsBroken() {
		return errors.New("media is already closed")
	}

	pt := int(p.conf.Video.Type)

	pstr := fmt.Sprintf(gstVideoPipeline, pt, pt)

	var (
		sipRtpOut  rtp.WriteStreamCloser
		sipRtcpOut rtp.WriteStreamCloser //! unused
	)

	{
		w, err := p.sess.OpenWriteStream() // -> sip_rtp_out
		if err != nil {
			return fmt.Errorf("failed to open RTP write stream: %w", err)
		}
		sipRtpOut = rtp.NewStreamNopCloser(w) // -> sip_rtp_out
	}

	{ //! unused
		w, err := p.rtcpsess.OpenWriteStream() // -> sip_rtcp_out
		if err != nil {
			return fmt.Errorf("failed to open RTCP write stream: %w", err)
		}
		sipRtcpOut = rtp.NewStreamNopCloser(w) // -> sip_rtcp_out
	}

	p.log.Infow("setting up video pipeline", "payloadType", pt, "pipeline", pstr)
	pipeline, err := gst.NewPipelineFromString(pstr)
	if err != nil {
		return fmt.Errorf("failed to create pipeline: %w", err)
	}

	vp, err := NewVideoPipeline(pipeline, sipRtpOut, sipRtcpOut, p.videoIn)
	if err != nil {
		return fmt.Errorf("failed to create video pipeline: %w", err)
	}
	p.vp = vp

	if w := p.videoOut.Swap(vp.WebrtcIn); w != nil {
		_ = w.Close()
	}

	if err := vp.Start(); err != nil {
		return fmt.Errorf("failed to start video pipeline: %w", err)
	}

	p.log.Infow("video pipeline started", "pipeline", pstr)

	go p.rtpLoop(p.sess, vp.SipRtpIn)
	// go p.rtpLoop(p.rtcpsess, vp.SipRtcpIn) //! unused

	return nil
}

const gstVideoPipeline = `
  ( appsrc name=sip_rtp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
      caps="application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000" !
      rtpjitterbuffer latency=100 do-lost=true mode=slave !
      rtph264depay request-keyframe=false wait-for-keyframe=false !
      h264parse !
      avdec_h264 max-threads=0 skip-frame=default !
      videoconvert !
      vp8enc deadline=1 target-bitrate=2000000 cpu-used=16 keyframe-max-dist=60 noise-sensitivity=2 buffer-size=60 static-threshold=100 !
      rtpvp8pay pt=96 mtu=1200 !
      appsink name=webrtc_out emit-signals=false drop=false max-buffers=0 sync=false
  )
  ( appsrc name=webrtc_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
      caps="application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96" !
      rtpjitterbuffer latency=30 do-lost=true do-retransmission=false drop-on-latency=true !
      rtpvp8depay !
      vp8dec !
      videoconvert !
      video/x-raw,format=I420 !
      x264enc bitrate=2000 key-int-max=60 bframes=0 rc-lookahead=0 sliced-threads=true sync-lookahead=0 !
      h264parse config-interval=-1 !
      rtph264pay pt=%d mtu=1200 config-interval=-1 aggregate-mode=zero-latency !
      appsink name=sip_rtp_out emit-signals=false drop=false max-buffers=2 sync=false
  )`

func (p *VideoPort) rtpLoop(sess rtp.Session, w rtp.WriteStream) {
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
		h           rtp.Header
		pipeline    string
		errorCnt    int
		packetCount int
	)
	log.Infow("starting RTP read loop")
	for {
		h = rtp.Header{}
		n, err := r.ReadRTP(&h, buf)
		if err == io.EOF {
			return
		} else if err != nil {
			log.Errorw("read RTP failed", err)
			continue
		}
		packetCount++
		if n > rtp.MTUSize {
			overflow = true
			if !overflow {
				log.Errorw("RTP packet is larger than MTU limit", nil, "payloadSize", n)
			}
			continue
		}
		// Copy payload to prevent buffer reuse issues
		payload := make([]byte, n)
		copy(payload, buf[:n])
		i, err := w.WriteRTP(&h, payload)
		if packetCount%500 == 0 {
			log.Infow("video RTP stats", "packets", packetCount, "errors", errorCnt, "marker", h.Marker, "seq", h.SequenceNumber)
		}
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
