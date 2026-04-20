package samplewriter

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	msdk "github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/sip/res"
)

var CAT = gst.NewDebugCategory(
	"samplewriter",
	gst.DebugColorFgBlack|gst.DebugColorBgGreen,
	"samplewriter Element",
)

func NewSampleWriter(ctx context.Context, sampleDur time.Duration, rate int, frames []msdk.PCM16Sample) (*gst.Element, error) {
	element, err := gst.NewElement("samplewriter")
	if err != nil {
		return nil, err
	}
	src, ok := gst.SubclassFromElement[*SampleWriter](element)
	if !ok {
		return nil, fmt.Errorf("failed to cast element to SampleWriter subclass")
	}
	src.sampleDur = sampleDur
	src.rate = rate
	src.frames = frames

	return element, nil
}

type SampleWriter struct {
	sampleDur time.Duration
	rate      int
	frames    []msdk.PCM16Sample
	ctx       context.Context
	cancel    context.CancelFunc
	i         int
	ptsOffset gst.ClockTime
	ptsCursor gst.ClockTime
	started   bool
}

func (*SampleWriter) New() glib.GoObjectSubclass {
	return &SampleWriter{
		sampleDur: rtp.DefFrameDur,
		rate:      res.SampleRate,
	}
}

func (*SampleWriter) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"samplewriter",
		"src",
		"plays a sequence of PCM16 samples and then EOS",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("audio/x-raw, format=S16LE, layout=interleaved, channels=1")))
}

func (e *SampleWriter) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseSrc(instance)

	self.SetLive(true)
	self.SetFormat(gst.FormatTime)
	self.SetAsync(false)
	self.SetDoTimestamp(false)

	if srcPad := self.Element.GetStaticPad("src"); srcPad != nil {
		srcPad.AddProbe(gst.PadProbeTypeQueryUpstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			q := info.GetQuery()
			if q == nil || q.Type() != gst.QueryLatency {
				return gst.PadProbeOK
			}
			q.SetLatency(true, 0, gst.ClockTimeNone)
			return gst.PadProbeHandled
		})
	}
}

func (e *SampleWriter) SetCaps(self *base.GstBaseSrc, caps *gst.Caps) bool {
	return true
}

func (e *SampleWriter) GetCaps(self *base.GstBaseSrc, filter *gst.Caps) *gst.Caps {
	capsStr := fmt.Sprintf("audio/x-raw, format=S16LE, layout=interleaved, channels=1, rate=%d", e.rate)

	caps := gst.NewCapsFromString(capsStr)
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		if intersect := caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	return caps.Copy().Ref()
}

func (e *SampleWriter) Start(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelDebug, "Starting")

	if e.ctx == nil {
		e.ctx = context.Background()
	}
	e.ctx, e.cancel = context.WithCancel(e.ctx)
	e.i = 0
	e.started = false
	e.ptsOffset = 0
	e.ptsCursor = 0

	blockSize := int(e.sampleDur.Seconds()*float64(e.rate)) * 2
	for _, f := range e.frames {
		if sz := f.Size(); sz > blockSize {
			blockSize = sz
		}
	}
	self.SetBlocksize(uint(blockSize))

	return true
}

func (e *SampleWriter) Stop(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")

	return true
}

func (e *SampleWriter) Fill(self *base.GstBaseSrc, offset uint64, length uint, buffer *gst.Buffer) gst.FlowReturn {
	mapInfo := buffer.Map(gst.MapWrite)
	defer buffer.Unmap()

	ptr := mapInfo.Data()
	data := unsafe.Slice((*byte)(ptr), length)

	if e.i >= len(e.frames) {
		self.Log(CAT, gst.LevelInfo, "All frames sent, returning EOS")
		e.cancel()
		return gst.FlowEOS
	}

	select {
	case <-e.ctx.Done():
		self.Log(CAT, gst.LevelInfo, "Fill context done, returning Flushing")
		return gst.FlowFlushing
	default:
	}

	frame := e.frames[e.i]
	n, err := frame.CopyTo(data)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to copy frame data to buffer: %v", err))
		self.Error("Failed to copy frame data to buffer", err)
		return gst.FlowError
	}

	if !e.started {
		if clock := self.GetClock(); clock != nil {
			now := clock.GetTime()
			base := self.GetBaseTime()
			if now > base {
				e.ptsOffset = now - base
			}
		}
		e.started = true
	}

	samples := n / 2 // S16 mono, 2 bytes/sample
	dur := gst.ClockTime(time.Duration(samples) * time.Second / time.Duration(e.rate))
	buffer.SetPresentationTimestamp(e.ptsCursor + e.ptsOffset)
	buffer.SetDuration(dur)
	e.ptsCursor += dur
	e.i++

	if uint(n) < length {
		buffer.SetSize(int64(n))
	}
	return gst.FlowOK
}

func (s *SampleWriter) Unlock(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelInfo, "SampleWriter Unlock called, unblocking Fill and sending EOS")

	s.cancel()

	return true
}
