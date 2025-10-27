package sip

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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

// type vconn struct {
// 	*udpConn
// }

// func (v *vconn) Write(b []byte) (int, error) {
// 	print("writing video packet of size ", len(b), "\n")
// 	return v.udpConn.Write(b)
// }

// func (v *vconn) WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error) {
// 	print("writing video packet of size ", len(b), " to ", addr.String(), "\n")
// 	return v.udpConn.WriteToUDPAddrPort(b, addr)
// }

// func (v *vconn) Read(b []byte) (int, error) {
// 	n, err := v.udpConn.Read(b)
// 	print("read video packet of size ", n, "\n")
// 	return n, err
// }

// func (v *vconn) ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error) {
// 	n, addr, err := v.udpConn.ReadFromUDPAddrPort(b)
// 	print("read video packet of size ", n, " from ", addr.String(), "\n")
// 	return n, addr, err
// }

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

	// c := &vconn{udpConn: newUDPConn(log, conn)}

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

	// go func() {
	// 	slog.Info("test conn read")
	// 	buf := make([]byte, 1500)
	// 	i, err := p.conn.Read(buf) // prime the conn
	// 	slog.Info("test conn read done", "n", i, "err", err)
	// }()
	p.conf = c

	p.sess = rtp.NewSession(p.log.WithValues("sess-media", "video"), p.rtpConn)

	p.rtcpsess = rtp.NewSession(p.log.WithValues("sess-media", "video-rtcp"), p.rtcpConn)

	// go func() {
	// 	for {
	// 		time.Sleep(time.Second)
	// 		slog.Info("video sess conn info", "local", p.conn.LocalAddr(), "remote", p.conn.RemoteAddr())
	// 	}
	// }()

	// if err := p.setupInput(); err != nil {
	// 	return err
	// }

	// if err := p.setupOutput(); err != nil {
	// 	return err
	// }

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
		sipRtcpOut rtp.WriteStreamCloser
	)

	{
		w, err := p.sess.OpenWriteStream() // -> sip_rtp_out
		if err != nil {
			return fmt.Errorf("failed to open RTP write stream: %w", err)
		}
		sipRtpOut = rtp.NewStreamNopCloser(w) // -> sip_rtp_out
	}

	{
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

	if w := p.videoOut.Swap(vp.WebrtcIn); w != nil {
		_ = w.Close()
	}

	if err := vp.Start(); err != nil {
		return fmt.Errorf("failed to start video pipeline: %w", err)
	}

	p.log.Infow("video pipeline started", "pipeline", pstr)

	go p.rtpLoop(p.sess, vp.SipRtpIn)
	// go p.rtpLoop(p.rtcpsess, vp.SipRtcpIn)

	return nil
}

// const gstVideoPipeline = `
//   rtpsession name=session0 !

//   appsrc name=sip_rtp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//     caps="application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000,packetization-mode=1" !
//   session0.recv_rtp_sink

//   appsrc name=sip_rtcp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//     caps="application/x-rtcp" !
//   session0.recv_rtcp_sink

//   session0.recv_rtp_src !
//     rtpjitterbuffer latency=30 do-lost=true drop-on-latency=true !
//     rtph264depay !
//     h264parse config-interval=-1 !
//     avdec_h264 !
//     videoconvert !
//     vp8enc deadline=1 target-bitrate=2000000 cpu-used=6 keyframe-max-dist=60 lag-in-frames=0 threads=4 !
//     rtpvp8pay pt=96 mtu=1200 !
//   session0.send_rtp_sink

//   funnel name=webrtc_mux !
//     appsink name=webrtc_out emit-signals=false drop=false max-buffers=2 sync=false

//   session0.send_rtp_src ! queue ! webrtc_mux.sink_0
//   session0.send_rtcp_src ! queue ! webrtc_mux.sink_1

//   rtpsession name=session1 !

//   appsrc name=webrtc_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//     caps="application/x-rtp" !
//   session1.recv_rtp_sink

//   session1.recv_rtp_src !
//     rtpjitterbuffer latency=30 do-lost=true drop-on-latency=true !
//     rtpvp8depay !
//     vp8dec !
//     videoconvert !
//     video/x-raw,format=I420 !
//     x264enc bitrate=2000 key-int-max=60 bframes=0 rc-lookahead=0 sliced-threads=true sync-lookahead=0 !
//     h264parse config-interval=-1 !
//     rtph264pay pt=%d mtu=1200 config-interval=-1 aggregate-mode=zero-latency !
//   session1.send_rtp_sink

//   session1.send_rtp_src !
//     appsink name=sip_rtp_out emit-signals=false drop=false max-buffers=2 sync=false

//   session1.send_rtcp_src !
//     appsink name=sip_rtcp_out emit-signals=false drop=false max-buffers=10 sync=false
// `

const gstVideoPipeline = `
  ( appsrc name=sip_rtp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
      caps="application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000,packetization-mode=1" !
      rtpjitterbuffer latency=30 do-lost=true do-retransmission=false drop-on-latency=true !
      rtph264depay !
      h264parse config-interval=-1 !
      avdec_h264 !
      videoconvert !
      vp8enc deadline=1 target-bitrate=2000000 cpu-used=6 keyframe-max-dist=60 lag-in-frames=0 threads=4 buffer-initial-size=100 buffer-optimal-size=120 buffer-size=150 !
      rtpvp8pay pt=96 mtu=1200 !
      appsink name=webrtc_out emit-signals=false drop=false max-buffers=2 sync=false
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

// const gstPipeline = `
// appsrc name=sip_rtp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//     caps="application/x-rtp,media=video,encoding-name=H264,clock-rate=90000,payload=%d,packetization-mode=1"
//     ! rtpbin name=rb

// appsrc name=sip_rtcp_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//     caps="application/x-rtcp"
//     ! rb.recv_rtcp_sink_0

// rb.recv_rtp_src_0
//     ! rtph264depay
//     ! h264parse config-interval=-1
//     ! avdec_h264
//     ! videoconvert
//     ! vp8enc deadline=1 target-bitrate=2000000 cpu-used=6 keyframe-max-dist=60 lag-in-frames=0 threads=4 buffer-initial-size=100 buffer-optimal-size=120 buffer-size=150
//     ! rtpvp8pay pt=96 mtu=1200 picture-id-mode=2 use-tl0picidx=true
//     ! appsink name=webrtc_out emit-signals=false drop=false max-buffers=2 sync=false

// appsrc name=webrtc_in format=3 is-live=true do-timestamp=true max-bytes=0 block=false
//     caps="application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96"
//     ! rtpvp8depay
//     ! vp8dec
//     ! videoconvert
//     ! video/x-raw,format=I420
//     ! x264enc bitrate=2000 key-int-max=60 bframes=0 rc-lookahead=0 sliced-threads=true sync-lookahead=0 tune=zerolatency
//     ! h264parse config-interval=-1
//     ! rtph264pay pt=%d mtu=1200 config-interval=-1 aggregate-mode=zero-latency
//     ! rb.send_rtp_sink_0

// rb.send_rtp_src_0
//     ! appsink name=sip_rtp_out emit-signals=false drop=false max-buffers=2 sync=false

// rb.send_rtcp_src_0
//     ! appsink name=sip_rtcp_out emit-signals=false drop=false sync=false
// `

// func (p *VideoPort) setupIO() error {
// 	if p.closed.IsBroken() {
// 		return errors.New("media is already closed")
// 	}

// 	pt := int(p.conf.Video.Type)

// 	pstr := fmt.Sprintf(gstPipeline, pt, pt)
// }

const inputPipeline = `appsrc name=src format=3 is-live=true do-timestamp=true max-bytes=0 block=false
  caps="application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000,packetization-mode=1" !
  rtpjitterbuffer latency=30 do-lost=true do-retransmission=false drop-on-latency=true !
  rtph264depay !
  h264parse config-interval=-1 !
  avdec_h264 !
  videoconvert !
  vp8enc deadline=1 target-bitrate=2000000 cpu-used=6 keyframe-max-dist=60 lag-in-frames=0 threads=4 buffer-initial-size=100 buffer-optimal-size=120 buffer-size=150 !
  rtpvp8pay pt=96 mtu=1200 !
  appsink name=sink emit-signals=false drop=false max-buffers=2 sync=false`

func (p *VideoPort) setupInput() error {
	if p.closed.IsBroken() {
		return errors.New("media is already closed")
	}

	pt := int(p.conf.Video.Type)

	pstr := fmt.Sprintf(inputPipeline, pt)

	p.log.Infow("setting up video pipeline", "payloadType", pt, "pipeline", pstr)
	gp, gw, err := p.gstPipeline(p.videoIn, pstr)
	if err != nil {
		p.log.Errorw("failed to create GST input pipeline", err)
		return err
	}
	p.log.Infow("video input pipeline created", "pipeline", gw.String())

	if err := gp.Run(context.Background()); err != nil {
		p.log.Errorw("failed to run GST input pipeline", err)
		return err
	}

	go p.rtpLoop(p.sess, gw)

	// TODO: handle crypto

	// TODO: gst pipeline

	return nil
}

const outputPipeline = `appsrc name=src format=3 is-live=true do-timestamp=true max-bytes=0 block=false
  caps="application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96" !
  rtpjitterbuffer latency=30 do-lost=true do-retransmission=false drop-on-latency=true !
  rtpvp8depay !
  vp8dec !
  videoconvert !
  video/x-raw,format=I420 !
  x264enc bitrate=2000 key-int-max=60 bframes=0 rc-lookahead=0 sliced-threads=true sync-lookahead=0 !
  h264parse config-interval=-1 !
  rtph264pay pt=%d mtu=1200 config-interval=-1 aggregate-mode=zero-latency !
  appsink name=sink emit-signals=false drop=false max-buffers=2 sync=false`

func (p *VideoPort) setupOutput() error {
	if p.closed.IsBroken() {
		return errors.New("media is already closed")
	}

	w, err := p.sess.OpenWriteStream()
	if err != nil {
		return err
	}

	// w := &connWriter{conn: p.conn}

	p.log.Infow("video output write stream opened", "writeStream", w.String())

	wc := rtp.NewStreamNopCloser(w)

	pt := int(p.conf.Video.Type)

	pstr := fmt.Sprintf(outputPipeline, pt)

	p.log.Infow("setting up video output pipeline", "payloadType", pt, "pipelineStr", pstr)

	gp, gw, err := p.gstPipeline(wc, pstr)
	if err != nil {
		p.log.Errorw("failed to create GST output pipeline1", err)
		return err
	}

	if w := p.videoOut.Swap(gw); w != nil {
		_ = w.Close()
	}

	if err := gp.Run(context.Background()); err != nil {
		p.log.Errorw("failed to run GST output pipeline", err)
		return err
	}

	p.log.Infow("video output writer set", "videoWriter", p.videoOut.String())

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
		slog.Info("lilili conn", "local", p.rtpConn.LocalAddr(), "remote", p.rtpConn.RemoteAddr())
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
			continue
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
		log.Debugw("RTP packet processed", "payloadSize", n, "rtpHeader", h)
		errorCnt = 0
		pipeline = ""
	}
}

func (p *VideoPort) gstPipeline(in rtp.WriteStreamCloser, pstr string) (*GstPipelineRunner, *GstWriteRTP, error) {
	builder := NewPipelineBuilder()
	pipeline, err := builder.StrPipeline(pstr).Build()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build GST pipeline: %w", err)
	}

	appsrc := builder.appsrc
	appsink := builder.appsink

	r, err := NewGstReadRTP(appsink)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create GstReadRTP: %w", err)
	}

	gpr := NewGstPipelineRunner(context.Background(), pipeline)

	if err := gpr.NewSinkWriter(r, in); err != nil {
		return nil, nil, fmt.Errorf("failed to create GST sink writer: %w", err)
	}

	w, err := NewGstWriteRTP(appsrc)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create GstWriteRTP: %w", err)
	}

	// gw := NewGstWriteStream(context.Background(), pipeline, rw, in)
	// if err := gw.Start(); err != nil {
	// 	return nil, fmt.Errorf("failed to start GST write stream: %w", err)
	// }
	return gpr, w, nil
}
