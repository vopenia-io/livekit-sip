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

	// c := &vconn{udpConn: newUDPConn(log, conn)}

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

	// go func() {
	// 	for {
	// 		time.Sleep(time.Second)
	// 		slog.Info("video sess conn info", "local", p.conn.LocalAddr(), "remote", p.conn.RemoteAddr())
	// 	}
	// }()

	if err := p.setupInput(); err != nil {
		return err
	}

	if err := p.setupOutput(); err != nil {
		return err
	}

	return nil
}

func NewGstWriteStream(ctx context.Context, pipeline *gst.Pipeline, rw *GstReadWriteRTP, w rtp.WriteStreamCloser) *GstWriteStream {
	if ctx == nil || pipeline == nil || rw == nil || w == nil {
		panic("invalid arguments to NewGstWriteStream")
	}

	g := &GstWriteStream{
		pipeline: pipeline,
		w:        w,
		rw:       rw,
	}
	g.ctx, g.cancel = context.WithCancel(ctx)
	return g
}

type GstWriteStream struct {
	ctx      context.Context
	cancel   context.CancelFunc
	running  bool
	wg       sync.WaitGroup
	pipeline *gst.Pipeline
	w        rtp.WriteStreamCloser
	rw       *GstReadWriteRTP
}

func (g *GstWriteStream) String() string {
	pname := "nil"
	if g.pipeline != nil {
		pname = g.pipeline.GetName()
	}
	wname := "nil"
	if g.w != nil {
		wname = g.w.String()
	}

	return fmt.Sprintf("GstWriteStream(%s: %t) -> %s", pname, g.running, wname)
}

func (g *GstWriteStream) Start() error {
	if g.running {
		return nil
	}

	slog.InfoContext(g.ctx, "starting GstWriteStream", "stream", g.String())

	if err := g.pipeline.SetState(gst.StatePlaying); err != nil {
		return err
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		<-g.ctx.Done()
		g.pipeline.SetState(gst.StateNull)
	}()

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()

		var h rtp.Header
		payload := make([]byte, rtp.MTUSize)

		for {
			select {
			case <-g.ctx.Done():
				return
			default:
				n, err := g.rw.ReadRTP(&h, payload)
				if err != nil {
					if err == io.EOF {
						slog.InfoContext(g.ctx, "GstWriteStream read EOF, exiting")
						return
					}
					slog.ErrorContext(g.ctx, "GstWriteStream read RTP failed", err)
					continue
				}

				// payload = payload[:n] // trim to actual size, don't know if it's necessary

				_, err = g.w.WriteRTP(&h, payload[:n])
				if err != nil {
					slog.ErrorContext(g.ctx, "GstWriteStream write RTP failed", err)
					continue
				}
			}
		}
	}()

	g.running = true

	return nil
}

func (g *GstWriteStream) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
	if !g.running {
		if err := g.Start(); err != nil {
			return 0, err
		}
	}
	return g.rw.WriteRTP(h, payload)
}

func (g *GstWriteStream) Close() error {
	g.cancel()
	g.wg.Wait()
	return g.w.Close()
}

func NewReadWriteRTP(src *app.Source, sink *app.Sink) (*GstReadWriteRTP, error) {
	g := &GstReadWriteRTP{
		// in:   bytes.NewBuffer(nil),
		// out:  bytes.NewBuffer(nil),
		src:  src,
		sink: sink,
	}

	// buf, err := gst.NewBufferFromReader(g.in)
	// if err != nil {
	// 	p.log.Errorw("failed to create GST buffer", err)
	// 	return nil, err
	// }
	// g.gbuf = buf

	if err := g.src.SetState(gst.StatePlaying); err != nil {
		return nil, err
	}

	if err := g.sink.SetState(gst.StatePlaying); err != nil {
		return nil, err
	}

	return g, nil
}

type GstReadWriteRTP struct {
	sink *app.Sink
	src  *app.Source
}

func (g *GstReadWriteRTP) String() string {
	return "GstWriteStream"
}

func (g *GstReadWriteRTP) Close() error {
	// if g.w != nil && *g.w != nil {
	// 	return (*g.w).Close()
	// }

	if err := g.src.SetState(gst.StateNull); err != nil {
		return err
	}
	if err := g.sink.SetState(gst.StateNull); err != nil {
		return err
	}

	return nil
}

func (g *GstReadWriteRTP) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
	p := rtp.Packet{
		Header:  *h,
		Payload: payload,
	}
	data, err := p.Marshal()
	if err != nil {
		return 0, err
	}
	n := len(payload)

	buf := gst.NewBufferFromBytes(data)
	if buf == nil {
		return 0, errors.New("failed to create GST buffer from RTP packet")
	}
	g.src.PushBuffer(buf)

	return n, nil
}

func (g *GstReadWriteRTP) ReadRTP(h *rtp.Header, payload []byte) (int, error) {
	var pkt rtp.Packet

	sample := g.sink.PullSample()
	if sample == nil {
		if g.sink.IsEOS() {
			return 0, io.EOF
		}
		return 0, errors.New("failed to pull sample from appsink")
	}
	buf := sample.GetBuffer()
	if buf == nil {
		return 0, errors.New("failed to get buffer from sample")
	}

	if err := pkt.Unmarshal(buf.Bytes()); err != nil {
		return 0, err
	}

	*h = pkt.Header
	n := copy(payload, pkt.Payload)
	return n, nil
}

// func (p *VideoPort) gstInputPipeline() (*GstWriteStream, error) {
// 	// TODO: create GST pipeline

// 	// Create the pipeline
// 	pipeline, err := gst.NewPipeline("test-pipeline")
// 	if err != nil {
// 		return nil, err
// 	}

// 	// Create appsrc and appsink elements
// 	appsrc, err := app.NewAppSrc()
// 	if err != nil {
// 		return nil, err
// 	}
// 	appsink, err := app.NewAppSink()
// 	if err != nil {
// 		return nil, err
// 	}

// 	// Add them to the pipeline
// 	if err := pipeline.AddMany(appsrc.Element, appsink.Element); err != nil {
// 		return nil, err
// 	}

// 	// Link them (appsrc -> appsink)
// 	if err := gst.ElementLinkMany(appsrc.Element, appsink.Element); err != nil {
// 		return nil, err
// 	}

// 	rw, err := NewReadWriteRTP(appsrc, appsink)
// 	if err != nil {
// 		return nil, err
// 	}
// 	gw := NewGstWriteStream(context.Background(), pipeline, rw, p.videoIn)
// 	if err := gw.Start(); err != nil {
// 		return nil, err
// 	}
// 	return gw, nil
// }

// func (p *VideoPort) gstInputPipeline() (*GstWriteStream, error) {
// 	// TODO: create GST pipeline

// 	// Create the pipeline
// 	pipeline, err := gst.NewPipeline("test-pipeline")
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create GST pipeline: %w", err)
// 	}

// 	// Create appsrc and appsink elements
// 	appsrc, err := app.NewAppSrc()
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create GST appsrc: %w", err)
// 	}

// 	appsink, err := app.NewAppSink()
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create GST appsink: %w", err)
// 	}

// 	jitter, err := gst.NewElement("rtpjitterbuffer")
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create GST rtpjitterbuffer: %w", err)
// 	}

// 	depay, err := gst.NewElement("rtph264depay")
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create GST rtph264depay: %w", err)
// 	}

// 	parse, err := gst.NewElement("h264parse")
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create GST h264parse: %w", err)
// 	}

// 	decoder, err := gst.NewElement("avdec_h264")
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create GST avdec_h264: %w", err)
// 	}

// 	converter, err := gst.NewElement("videoconvert")
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create GST videoconvert: %w", err)
// 	}

// 	// scale, err := gst.NewElement("videoscale")
// 	// if err != nil {
// 	// 	return nil, fmt.Errorf("failed to create GST videoscale: %w", err)
// 	// }

// 	rate, err := gst.NewElement("videorate")
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create GST videorate: %w", err)
// 	}

// 	encoder, err := gst.NewElement("vp8enc")
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create GST vp8enc: %w", err)
// 	}

// 	rtppay, err := gst.NewElement("rtpvp8pay")
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create GST rtpvp8pay: %w", err)
// 	}

// 	{
// 		// Configure appsrc
// 		caps := gst.NewCapsFromString("application/x-rtp,media=video,encoding-name=H264,payload=96,clock-rate=90000")
// 		appsrc.SetCaps(caps)
// 		if err := appsrc.SetProperty("is-live", true); err != nil {
// 			return nil, fmt.Errorf("failed to set GST appsrc is-live property: %w", err)
// 		}
// 		// if err := appsrc.SetProperty("format", int32(3)); err != nil {
// 		// 	return nil, fmt.Errorf("failed to set GST appsrc format property: %w", err)
// 		// }
// 		if err := appsrc.SetProperty("do-timestamp", true); err != nil {
// 			return nil, fmt.Errorf("failed to set GST appsrc do-timestamp property: %w", err)
// 		}

// 		// Configure appsink
// 		if err := appsink.SetProperty("emit-signals", true); err != nil {
// 			return nil, fmt.Errorf("failed to set GST appsink emit-signals property: %w", err)
// 		}
// 		if err := appsink.SetProperty("drop", true); err != nil {
// 			return nil, fmt.Errorf("failed to set GST appsink drop property: %w", err)
// 		}
// 		if err := appsink.SetProperty("max-buffers", uint(1)); err != nil {
// 			return nil, fmt.Errorf("failed to set GST appsink max-buffers property: %w", err)
// 		}

// 		// Configure jitter buffer
// 		if err := jitter.SetProperty("latency", uint(50)); err != nil {
// 			return nil, fmt.Errorf("failed to set GST rtpjitterbuffer latency property: %w", err)
// 		}
// 		if err := jitter.SetProperty("do-lost", true); err != nil {
// 			return nil, fmt.Errorf("failed to set GST rtpjitterbuffer do-lost property: %w", err)
// 		}
// 		// if err := jitter.SetProperty("do-retransmission", false); err != nil {
// 		// 	return nil, fmt.Errorf("failed to set GST rtpjitterbuffer do-retransmission property: %w", err)
// 		// }

// 		// Configure encoder
// 		if err := encoder.SetProperty("deadline", int64(1)); err != nil {
// 			return nil, fmt.Errorf("failed to set GST vp8enc deadline property: %w", err)
// 		}
// 		if err := encoder.SetProperty("cpu-used", 4); err != nil {
// 			return nil, fmt.Errorf("failed to set GST vp8enc cpu-used property: %w", err)
// 		}
// 		// if err := encoder.SetProperty("target-bitrate", uint(1000000)); err != nil {
// 		// 	return nil, fmt.Errorf("failed to set GST vp8enc target-bitrate property: %w", err)
// 		// }
// 		// if err := encoder.SetProperty("end-usage", 1); err != nil {
// 		// 	return nil, fmt.Errorf("failed to set GST vp8enc end-usage property: %w", err)
// 		// }
// 		// if err := encoder.SetProperty("error-resilient", 1); err != nil {
// 		// 	return nil, fmt.Errorf("failed to set GST vp8enc error-resilient property: %w", err)
// 		// }
// 		// if err := encoder.SetProperty("keyframe-max-dist", uint(30)); err != nil {
// 		// 	return nil, fmt.Errorf("failed to set GST vp8enc keyframe-max-dist property: %w", err)
// 		// }
// 		// if err := encoder.SetProperty("min-quantizer", uint(4)); err != nil {
// 		// 	return nil, fmt.Errorf("failed to set GST vp8enc min-quantizer property: %w", err)
// 		// }
// 		// if err := encoder.SetProperty("max-quantizer", uint(20)); err != nil {
// 		// 	return nil, fmt.Errorf("failed to set GST vp8enc max-quantizer property: %w", err)
// 		// }
// 		// if err := encoder.SetProperty("threads", uint(4)); err != nil {
// 		// 	return nil, fmt.Errorf("failed to set GST vp8enc threads property: %w", err)
// 		// }
// 		// if err := encoder.SetProperty("lag-in-frames", uint(0)); err != nil {
// 		// 	return nil, fmt.Errorf("failed to set GST vp8enc lag-in-frames property: %w", err)
// 		// }

// 		// Configure payloader
// 		if err := rtppay.SetProperty("pt", uint(96)); err != nil {
// 			return nil, fmt.Errorf("failed to set GST rtpvp8pay pt property: %w", err)
// 		}
// 		if err := rtppay.SetProperty("mtu", uint(1200)); err != nil {
// 			return nil, fmt.Errorf("failed to set GST rtpvp8pay mtu property: %w", err)
// 		}
// 		// if err := rtppay.SetProperty("picture-id-mode", 2); err != nil {
// 		// 	return nil, fmt.Errorf("failed to set GST rtpvp8pay picture-id-mode property: %w", err)
// 		// }
// 	}

// 	// Add them to the pipeline
// 	elements := []*gst.Element{
// 		appsrc.Element,
// 		jitter,
// 		depay,
// 		parse,
// 		decoder,
// 		converter,
// 		// scale,
// 		rate,
// 		encoder,
// 		rtppay,
// 		appsink.Element,
// 	}

// 	for _, elem := range elements {
// 		if err := pipeline.Add(elem); err != nil {
// 			return nil, fmt.Errorf("failed to add element %s:%s to GST pipeline: %w", elem.GetFactory().GetName(), elem.GetName(), err)
// 		}
// 	}

// 	if err := appsrc.Link(jitter); err != nil {
// 		return nil, fmt.Errorf("failed to link appsrc to rtpjitterbuffer: %w", err)
// 	}
// 	if err := jitter.Link(depay); err != nil {
// 		return nil, fmt.Errorf("failed to link rtpjitterbuffer to rtph264depay: %w", err)
// 	}
// 	if err := depay.Link(parse); err != nil {
// 		return nil, fmt.Errorf("failed to link rtph264depay to h264parse: %w", err)
// 	}
// 	if err := parse.Link(decoder); err != nil {
// 		return nil, fmt.Errorf("failed to link h264parse to avdec_h264: %w", err)
// 	}
// 	if err := decoder.Link(converter); err != nil {
// 		return nil, fmt.Errorf("failed to link avdec_h264 to videoconvert: %w", err)
// 	}
// 	// if err := converter.Link(scale); err != nil {
// 	// 	return nil, fmt.Errorf("failed to link videoconvert to videoscale: %w", err)
// 	// }
// 	if err := converter.Link(rate); err != nil {
// 		return nil, fmt.Errorf("failed to link videoconvert to videorate: %w", err)
// 	}
// 	if err := rate.Link(encoder); err != nil {
// 		return nil, fmt.Errorf("failed to link videorate to vp8enc: %w", err)
// 	}
// 	if err := encoder.Link(rtppay); err != nil {
// 		return nil, fmt.Errorf("failed to link vp8enc to rtpvp8pay: %w", err)
// 	}
// 	if err := rtppay.Link(appsink.Element); err != nil {
// 		return nil, fmt.Errorf("failed to link rtpvp8pay to appsink: %w", err)
// 	}

// 	rw, err := NewReadWriteRTP(appsrc, appsink)
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create RTP read/write stream: %w", err)
// 	}
// 	gw := NewGstWriteStream(context.Background(), pipeline, rw, p.videoIn)
// 	if err := gw.Start(); err != nil {
// 		return nil, fmt.Errorf("failed to start GST write stream: %w", err)
// 	}
// 	return gw, nil
// }

func (p *VideoPort) gstInputPipeline() (*GstWriteStream, error) {
	builder := NewPipelineBuilder()
	pipeline, err := builder.StrPipeline(inputPipeline).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build GST pipeline: %w", err)
	}

	appsrc := builder.appsrc
	appsink := builder.appsink

	rw, err := NewReadWriteRTP(appsrc, appsink)
	if err != nil {
		return nil, fmt.Errorf("failed to create RTP read/write stream: %w", err)
	}
	gw := NewGstWriteStream(context.Background(), pipeline, rw, p.videoIn)
	if err := gw.Start(); err != nil {
		return nil, fmt.Errorf("failed to start GST write stream: %w", err)
	}
	return gw, nil
}

type noopWriter struct{}

func (n *noopWriter) String() string {
	return "NoopWriter"
}

func (n *noopWriter) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
	return len(payload), nil
}

const inputPipeline = `appsrc name=src is-live=true format=time do-timestamp=true
  caps="application/x-rtp,media=video,encoding-name=VP8,payload=96,clock-rate=90000" !
  rtpjitterbuffer latency=0 do-lost=true !
  rtpvp8depay !
  vp8dec !
  videoconvert !
  tee name=t

  t. ! queue ! fpsdisplaysink name=preview text-overlay=true sync=true

  t. ! queue !
      vp8enc deadline=1 target-bitrate=1200000 keyframe-max-dist=60 !
      rtpvp8pay pt=96 mtu=1200 ssrc=0x11223344 !
      appsink name=sink sync=false emit-signals=true enable-last-sample=false
      caps="application/x-rtp,media=video,encoding-name=VP8,payload=96,clock-rate=90000"`

func (p *VideoPort) setupInput() error {
	if p.closed.IsBroken() {
		return errors.New("media is already closed")
	}

	p.log.Infow("setting up video pipeline")
	gw, err := p.gstPipeline(p.videoIn, inputPipeline)
	if err != nil {
		p.log.Errorw("failed to create GST input pipeline", err)
		return err
	}
	p.log.Infow("video input pipeline created", "pipeline", gw.String())

	// w := &noopWriter{}

	wc := rtp.NewStreamNopCloser(gw)

	go p.rtpLoop(p.sess, wc)

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
		slog.Info("lilili conn", "local", p.conn.LocalAddr(), "remote", p.conn.RemoteAddr())
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
		errorCnt = 0
		pipeline = ""
	}
}

func (p *VideoPort) gstPipeline(in rtp.WriteStreamCloser, pstr string) (*GstWriteStream, error) {
	builder := NewPipelineBuilder()
	pipeline, err := builder.StrPipeline(pstr).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build GST pipeline: %w", err)
	}

	appsrc := builder.appsrc
	appsink := builder.appsink

	rw, err := NewReadWriteRTP(appsrc, appsink)
	if err != nil {
		return nil, fmt.Errorf("failed to create RTP read/write stream: %w", err)
	}
	gw := NewGstWriteStream(context.Background(), pipeline, rw, in)
	if err := gw.Start(); err != nil {
		return nil, fmt.Errorf("failed to start GST write stream: %w", err)
	}
	return gw, nil
}

// type connWriter struct {
// 	conn UDPConn
// }

// func (w *connWriter) String() string {
// 	return fmt.Sprintf("UDPConnWriter(%s)", w.conn.RemoteAddr())
// }

// func (w *connWriter) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
// 	p := rtp.Packet{
// 		Header:  *h,
// 		Payload: payload,
// 	}
// 	data, err := p.Marshal()
// 	if err != nil {
// 		return 0, err
// 	}
// 	return w.conn.Write(data)
// }

const inputPipelineStr = `appsrc name=src is-live=true format=time do-timestamp=true
  caps="application/x-rtp,media=video,encoding-name=VP8,payload=96,clock-rate=90000" !
  rtpjitterbuffer latency=0 do-lost=true !
  rtpvp8depay !
  vp8dec !
  videoconvert !
  tee name=t

  t. ! queue ! fpsdisplaysink name=preview text-overlay=true sync=true

  t. ! queue !
      vp8enc deadline=1 target-bitrate=1200000 keyframe-max-dist=60 !
      rtpvp8pay pt=96 mtu=1200 ssrc=0x11223344 !
      appsink name=sink sync=false emit-signals=true enable-last-sample=false
      caps="application/x-rtp,media=video,encoding-name=VP8,payload=96,clock-rate=90000"`

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

	gw, err := p.gstPipeline(wc, inputPipelineStr)
	if err != nil {
		p.log.Errorw("failed to create GST output pipeline1", err)
		return err
	}

	if w := p.videoOut.Swap(gw); w != nil {
		_ = w.Close()
	}

	p.log.Infow("video output writer set", "videoWriter", p.videoOut.String())

	return nil
}
