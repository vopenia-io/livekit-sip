// Package benchmarks runs comparative latency + CPU measurements across
// multiple transcode elements at a matrix of source/target resolutions.
package benchmarks

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
)

// TraceLogPath is where TestMain points GST_DEBUG_FILE. The runner
// reads it incrementally between Run() calls.
const TraceLogPath = "testdata/gst_trace.log"

// Config describes one benchmark run.
type Config struct {
	SourceWidth, SourceHeight int
	SourceFPS                 int
	NumBuffers                int
	TargetWidth, TargetHeight int
}

// Element is the structural contract the benchmarks runner expects from
// each transcode element. BuildSource builds the upstream chain and
// returns its outgoing src pad; BuildSink builds the downstream chain
// and returns its incoming sink pad. Returning pads (instead of
// elements) sidesteps the head==tail ambiguity when a chain is a
// single element.
type Element interface {
	Name() string
	BuildSource(p *gst.Pipeline, width, height, fps, numBuffers int) (src *gst.Pad, err error)
	BuildElement(p *gst.Pipeline, targetWidth, targetHeight int) (*gst.Element, error)
	BuildSink(p *gst.Pipeline) (sink *gst.Pad, err error)
}

// ChildStats is the latency tracer percentile summary for one internal
// child of the element-under-test bin.
type ChildStats struct {
	Name               string
	Count              int
	Min, Mean          time.Duration
	P50, P95, P99, Max time.Duration
}

// Result is everything one benchmark run produced.
type Result struct {
	Config Config

	ElementName string

	// Residency (pad probes on q_in.src and q_out.sink, correlated by PTS).
	ResidencySamples                  int
	ResidencyMin, ResidencyMean       time.Duration
	ResidencyP50, ResidencyP95        time.Duration
	ResidencyP99, ResidencyMax        time.Duration

	// Per-child from the GST latency tracer.
	Children  []ChildStats
	SumMean   time.Duration
	CrossRatio float64 // ResidencyP50 / SumMean

	// CPU load of the isolated streaming thread.
	IsolatedTID        int
	RusageSamples      int
	WallSpan, CPUUsed  time.Duration
	CPULoadPct         float64 // cpu/wall
	TracerAvgLoadPct   float64 // rusage tracer avg
	TracerMaxCurrPct   float64 // rusage tracer max of current-cpuload
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[mGKH]`)

// element-latency, element-id=(string)0x..., element=(string)vp9dec0, src=(string)src, time=(guint64)1234567, ts=(guint64)...
var elementLatencyRe = regexp.MustCompile(`element-latency,.*?element=\(string\)([^,]+),.*?time=\(guint64\)(\d+)`)

// thread-rusage, thread-id=(guint64)X, ts=(guint64)ns, average-cpuload=(uint)pm, current-cpuload=(uint)pm, time=(guint64)ns
var threadRusageRe = regexp.MustCompile(`thread-rusage,.*?ts=\(guint64\)(\d+),.*?average-cpuload=\(uint\)(\d+),.*?current-cpuload=\(uint\)(\d+),.*?time=\(guint64\)(\d+)`)

type rusageSample struct {
	tsNs      uint64
	avgPerMil uint32
	curPerMil uint32
	cpuTimeNs uint64
}

// prefixTID extracts the Linux TID from a GStreamer debug log line.
// After stripping ANSI codes, the line is
// "TIMESTAMP PID TID LEVEL CATEGORY FILE:...". Returns -1 on failure.
func prefixTID(plainLine string) int {
	fields := strings.Fields(plainLine)
	if len(fields) < 4 {
		return -1
	}
	if !strings.Contains(fields[0], ":") {
		return -1
	}
	tid, err := strconv.Atoi(fields[2])
	if err != nil {
		return -1
	}
	return tid
}

// tracerSamples is the parsed output of one pipeline run.
type tracerSamples struct {
	samples     map[string][]time.Duration // element name -> per-buffer latency
	elemThreads map[string]map[int]int     // element name -> tid -> count
	rusage      map[int][]rusageSample     // tid -> rusage samples
}

// traceReader reads GST_DEBUG_FILE incrementally. mark() before a run,
// parse() after, and only the bytes written in between are scanned —
// sidesteps having to fight go test for stderr ownership.
type traceReader struct {
	path       string
	readOffset int64
}

func newTraceReader(path string) *traceReader { return &traceReader{path: path} }

func (tr *traceReader) mark() error {
	fi, err := os.Stat(tr.path)
	if err != nil {
		if os.IsNotExist(err) {
			tr.readOffset = 0
			return nil
		}
		return err
	}
	tr.readOffset = fi.Size()
	return nil
}

func (tr *traceReader) parse(track map[string]bool) (*tracerSamples, error) {
	f, err := os.Open(tr.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(tr.readOffset, io.SeekStart); err != nil {
		return nil, err
	}
	out := &tracerSamples{
		samples:     make(map[string][]time.Duration),
		elemThreads: make(map[string]map[int]int),
		rusage:      make(map[int][]rusageSample),
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "element-latency,") && !strings.Contains(line, "thread-rusage,") {
			continue
		}
		plain := ansiRe.ReplaceAllString(line, "")
		tid := prefixTID(plain)

		if m := elementLatencyRe.FindStringSubmatch(plain); m != nil {
			name := m[1]
			if !track[name] {
				continue
			}
			ns, err := strconv.ParseUint(m[2], 10, 64)
			if err != nil {
				continue
			}
			out.samples[name] = append(out.samples[name], time.Duration(ns))
			if tid >= 0 {
				byTid, ok := out.elemThreads[name]
				if !ok {
					byTid = make(map[int]int)
					out.elemThreads[name] = byTid
				}
				byTid[tid]++
			}
			continue
		}
		if m := threadRusageRe.FindStringSubmatch(plain); m != nil {
			if tid < 0 {
				continue
			}
			tsNs, _ := strconv.ParseUint(m[1], 10, 64)
			avg, _ := strconv.ParseUint(m[2], 10, 32)
			cur, _ := strconv.ParseUint(m[3], 10, 32)
			cpuNs, _ := strconv.ParseUint(m[4], 10, 64)
			out.rusage[tid] = append(out.rusage[tid], rusageSample{
				tsNs: tsNs, avgPerMil: uint32(avg), curPerMil: uint32(cur), cpuTimeNs: cpuNs,
			})
			continue
		}
	}
	return out, scanner.Err()
}

// percentileAndMean returns p50/p95/p99/min/max/mean of the input after
// dropping `drop` warmup samples. The returned slice is a sorted copy.
func percentileAndMean(in []time.Duration, drop int) (sorted []time.Duration, p50, p95, p99, min, max, mean time.Duration) {
	used := in
	if len(used) > drop {
		used = used[drop:]
	}
	if len(used) == 0 {
		return nil, 0, 0, 0, 0, 0, 0
	}
	sorted = make([]time.Duration, len(used))
	copy(sorted, used)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	pct := func(p float64) time.Duration {
		idx := int(float64(len(sorted)-1) * p)
		return sorted[idx]
	}
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	return sorted, pct(0.50), pct(0.95), pct(0.99), sorted[0], sorted[len(sorted)-1], sum / time.Duration(len(sorted))
}

const (
	warmupDrop = 15
	// minResidencyRatio is the minimum fraction of numBuffers we expect
	// to see reach the sink via the residency probes. Anything lower
	// suggests the pipeline silently dropped frames and the percentiles
	// below would be computed on a biased sub-sample. Set to 0.70 to
	// tolerate elements with deeper pipelining (NVDEC AV1 can buffer
	// ~25 frames before emitting output, and live sources drop the
	// first few while the element warms up).
	minResidencyRatio = 0.70
)

// Run executes one benchmark and returns a Result. Pipeline layout:
//
//	source -> q_in -> ELEMENT -> q_out -> sink
//
// q_in and q_out isolate the element under test onto its own streaming
// thread so the rusage tracer can attribute CPU to just that work.
// Asserts the internal cross-checks and fails the test on divergence.
func Run(t *testing.T, elem Element, cfg Config) Result {
	t.Helper()

	pipeline, err := gst.NewPipeline(fmt.Sprintf("bench-%s-%dx%d-%dx%d", elem.Name(), cfg.SourceWidth, cfg.SourceHeight, cfg.TargetWidth, cfg.TargetHeight))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	srcPad, err := elem.BuildSource(pipeline, cfg.SourceWidth, cfg.SourceHeight, cfg.SourceFPS, cfg.NumBuffers)
	if err != nil {
		t.Fatalf("BuildSource: %v", err)
	}
	eut, err := elem.BuildElement(pipeline, cfg.TargetWidth, cfg.TargetHeight)
	if err != nil {
		t.Fatalf("BuildElement: %v", err)
	}
	sinkPad, err := elem.BuildSink(pipeline)
	if err != nil {
		t.Fatalf("BuildSink: %v", err)
	}

	qIn, err := gst.NewElementWithName("queue", "q_in")
	if err != nil {
		t.Fatalf("q_in: %v", err)
	}
	qOut, err := gst.NewElementWithName("queue", "q_out")
	if err != nil {
		t.Fatalf("q_out: %v", err)
	}
	if err := pipeline.AddMany(qIn, qOut); err != nil {
		t.Fatalf("add queues: %v", err)
	}
	if err := gst.ElementLinkMany(qIn, eut, qOut); err != nil {
		t.Fatalf("link q_in -> eut -> q_out: %v", err)
	}
	if ret := srcPad.Link(qIn.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatalf("link source -> q_in: %s", ret)
	}
	if ret := qOut.GetStaticPad("src").Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatalf("link q_out -> sink: %s", ret)
	}

	// Discover children of the element under test for tracer filtering.
	bin := gst.ToGstBin(eut)
	if bin == nil {
		t.Fatalf("%s did not cast to a GstBin", elem.Name())
	}
	children, err := bin.GetElementsRecursive()
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	tracked := make(map[string]bool, len(children))
	ordered := make([]string, 0, len(children))
	for _, c := range children {
		n := c.GetName()
		tracked[n] = true
		ordered = append(ordered, n)
	}

	// Residency measurement via pad probes on the isolated thread.
	type resSample struct {
		pts     gst.ClockTime
		latency time.Duration
	}
	var (
		resMu        sync.Mutex
		resEntry     = make(map[gst.ClockTime]time.Time)
		resOutSmp    []resSample
		resIn        atomic.Int32
		resOut       atomic.Int32
		ptsOffset    gst.ClockTime // exit_pts - entry_pts, discovered on first miss
		ptsOffsetSet bool
	)
	recordEntry := func(buf *gst.Buffer) {
		if buf == nil {
			return
		}
		pts := buf.PresentationTimestamp()
		if pts == gst.ClockTimeNone {
			return
		}
		resIn.Add(1)
		resMu.Lock()
		if _, exists := resEntry[pts]; !exists {
			resEntry[pts] = time.Now()
		}
		resMu.Unlock()
	}
	recordExit := func(buf *gst.Buffer) {
		if buf == nil {
			return
		}
		pts := buf.PresentationTimestamp()
		if pts == gst.ClockTimeNone {
			return
		}
		now := time.Now()
		resMu.Lock()
		defer resMu.Unlock()
		resOut.Add(1)

		// Fast path: direct PTS match. VP9 elements hit this every time.
		if start, ok := resEntry[pts]; ok {
			resOutSmp = append(resOutSmp, resSample{pts: pts, latency: now.Sub(start)})
			delete(resEntry, pts)
			if !ptsOffsetSet {
				ptsOffsetSet = true // offset is zero
			}
			return
		}
		// Known offset (h264 encoders: x264enc and nvh264enc rebase PTS
		// onto a ~1000h clock to give room for "time travel" DTS of
		// B-frames, even when bframes=0).
		if ptsOffsetSet {
			if pts < ptsOffset {
				return
			}
			adjusted := pts - ptsOffset
			if start, ok := resEntry[adjusted]; ok {
				resOutSmp = append(resOutSmp, resSample{pts: adjusted, latency: now.Sub(start)})
				delete(resEntry, adjusted)
			}
			return
		}
		// Offset unknown: use the oldest unmatched entry as the pair
		// (assumes FIFO ordering, which holds when the element under
		// test doesn't reorder frames — true for all our encoders with
		// bframes=0 and tune=zerolatency / zerolatency=true).
		var oldestPTS gst.ClockTime = ^gst.ClockTime(0)
		for k := range resEntry {
			if k < oldestPTS {
				oldestPTS = k
			}
		}
		if oldestPTS == ^gst.ClockTime(0) || pts < oldestPTS {
			return
		}
		ptsOffset = pts - oldestPTS
		ptsOffsetSet = true
		start := resEntry[oldestPTS]
		resOutSmp = append(resOutSmp, resSample{pts: oldestPTS, latency: now.Sub(start)})
		delete(resEntry, oldestPTS)
	}
	inPad := qIn.GetStaticPad("src")
	if inPad == nil {
		t.Fatal("q_in.src pad")
	}
	inPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if info.Type()&gst.PadProbeTypeBufferList != 0 {
			if list := info.GetBufferList(); list != nil {
				list.ForEach(func(b *gst.Buffer, _ uint) bool {
					recordEntry(b)
					return true
				})
			}
			return gst.PadProbeOK
		}
		recordEntry(info.GetBuffer())
		return gst.PadProbeOK
	})
	outPad := qOut.GetStaticPad("sink")
	if outPad == nil {
		t.Fatal("q_out.sink pad")
	}
	outPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if info.Type()&gst.PadProbeTypeBufferList != 0 {
			if list := info.GetBufferList(); list != nil {
				list.ForEach(func(b *gst.Buffer, _ uint) bool {
					recordExit(b)
					return true
				})
			}
			return gst.PadProbeOK
		}
		recordExit(info.GetBuffer())
		return gst.PadProbeOK
	})

	// Mark the current end of the GStreamer debug log file so we only
	// parse tracer records generated by this run.
	reader := newTraceReader(TraceLogPath)
	if err := reader.mark(); err != nil {
		t.Fatalf("trace mark: %v", err)
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("SetState PLAYING: %v", err)
	}

	bus := pipeline.GetPipelineBus()
	timeout := gst.ClockTime(time.Second)
	deadline := time.Now().Add(60 * time.Second)
	eos := false
	for time.Now().Before(deadline) && !eos {
		msg := bus.TimedPop(timeout)
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			eos = true
		case gst.MessageError:
			gerr := msg.ParseError()
			_ = pipeline.SetState(gst.StateNull)
			t.Fatalf("pipeline error: %v", gerr.Error())
		}
	}
	if !eos {
		_ = pipeline.SetState(gst.StateNull)
		t.Fatal("pipeline timed out waiting for EOS")
	}
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("SetState NULL: %v", err)
	}

	// GStreamer buffers writes to GST_DEBUG_FILE through stdio; give it
	// a moment to flush before we parse. No public flush API in go-gst.
	time.Sleep(50 * time.Millisecond)

	tr, err := reader.parse(tracked)
	if err != nil {
		t.Fatalf("parse trace log: %v", err)
	}

	result := Result{Config: cfg, ElementName: elem.Name()}

	resMu.Lock()
	resCopy := make([]resSample, len(resOutSmp))
	copy(resCopy, resOutSmp)
	resMu.Unlock()

	if len(resCopy) == 0 {
		t.Fatalf("no residency samples (PTS correlation failed) for %s %dx%d -> %dx%d",
			elem.Name(), cfg.SourceWidth, cfg.SourceHeight, cfg.TargetWidth, cfg.TargetHeight)
	}
	// Guard against silent frame drops: if we received fewer residency
	// samples than expected, the percentiles below are computed on a
	// biased subset and the numbers can't be trusted.
	minSamples := int(float64(cfg.NumBuffers) * minResidencyRatio)
	if len(resCopy) < minSamples {
		t.Fatalf("%s %dx%d->%dx%d: only %d residency samples for %d buffers (< %.0f%%)",
			elem.Name(), cfg.SourceWidth, cfg.SourceHeight, cfg.TargetWidth, cfg.TargetHeight,
			len(resCopy), cfg.NumBuffers, minResidencyRatio*100)
	}
	resLatencies := make([]time.Duration, 0, len(resCopy))
	for _, s := range resCopy {
		resLatencies = append(resLatencies, s.latency)
	}
	_, resP50, resP95, resP99, resMin, resMax, resMean := percentileAndMean(resLatencies, warmupDrop)
	result.ResidencySamples = len(resCopy) - warmupDrop
	if result.ResidencySamples < 0 {
		result.ResidencySamples = 0
	}
	result.ResidencyMin = resMin
	result.ResidencyMean = resMean
	result.ResidencyP50 = resP50
	result.ResidencyP95 = resP95
	result.ResidencyP99 = resP99
	result.ResidencyMax = resMax

	var sumMeanNs int64
	tracerHadData := false
	for _, n := range ordered {
		raw := tr.samples[n]
		sorted, p50, p95, p99, min, max, mean := percentileAndMean(raw, warmupDrop)
		cs := ChildStats{Name: n, Count: len(sorted), Min: min, Mean: mean, P50: p50, P95: p95, P99: p99, Max: max}
		if len(sorted) > 0 {
			tracerHadData = true
			sumMeanNs += mean.Nanoseconds()
		}
		result.Children = append(result.Children, cs)
	}
	if !tracerHadData {
		t.Fatal("no tracer samples — is the latency tracer active?")
	}
	result.SumMean = time.Duration(sumMeanNs)
	if result.SumMean > 0 {
		result.CrossRatio = float64(resP50) / float64(result.SumMean)
	}

	// CPU load of the isolated thread.
	tidVotes := make(map[int]int)
	for _, byTid := range tr.elemThreads {
		for tid, n := range byTid {
			tidVotes[tid] += n
		}
	}
	isolatedTID, maxVotes := -1, 0
	for tid, n := range tidVotes {
		if n > maxVotes {
			maxVotes = n
			isolatedTID = tid
		}
	}
	result.IsolatedTID = isolatedTID
	if isolatedTID >= 0 {
		samples := tr.rusage[isolatedTID]
		result.RusageSamples = len(samples)
		if len(samples) > 0 {
			first := samples[0]
			last := samples[len(samples)-1]
			result.WallSpan = time.Duration(last.tsNs - first.tsNs)
			result.CPUUsed = time.Duration(last.cpuTimeNs - first.cpuTimeNs)
			if result.WallSpan > 0 {
				result.CPULoadPct = float64(result.CPUUsed) / float64(result.WallSpan) * 100
			}
			var avgSum, maxCur uint64
			for _, s := range samples {
				avgSum += uint64(s.avgPerMil)
				if uint64(s.curPerMil) > maxCur {
					maxCur = uint64(s.curPerMil)
				}
			}
			result.TracerAvgLoadPct = float64(avgSum/uint64(len(samples))) / 10
			result.TracerMaxCurrPct = float64(maxCur) / 10
		}
	}

	// Measurement-integrity checks. These stay fatal because crossing
	// them means the numbers can't be trusted. Timing expectations (is
	// the element fast enough for realtime?) are NOT asserted here —
	// they belong to the human reading the results table.
	// Lower bound only: a cross_ratio below ~0.4 means wall-clock
	// residency is much shorter than the sum of per-child CPU means,
	// which is impossible if the tracers are working — that signals a
	// measurement bug worth failing on. The upper bound is unbounded
	// because elements with deep pipelining (NVDEC) or multi-threaded
	// children (dav1ddec, libvpx) legitimately report wall time much
	// larger than summed per-child CPU time.
	if result.CrossRatio < 0.4 {
		t.Fatalf("%s %dx%d->%dx%d: cross-check ratio %.2f < 0.4 — residency and tracer disagree",
			elem.Name(), cfg.SourceWidth, cfg.SourceHeight, cfg.TargetWidth, cfg.TargetHeight, result.CrossRatio)
	}

	return result
}

// dominantChild returns the child with the highest mean latency, or a
// zero-value ChildStats if there are none.
func dominantChild(r Result) ChildStats {
	var dom ChildStats
	for _, c := range r.Children {
		if c.Mean > dom.Mean {
			dom = c
		}
	}
	return dom
}

// FormatMarkdown returns a markdown document with one `# element` section
// per unique ElementName, in the order the results first appear. Each
// section contains a table of the results for that element.
func FormatMarkdown(results []Result) string {
	var b strings.Builder
	prev := ""
	for _, r := range results {
		if r.ElementName != prev {
			if prev != "" {
				fmt.Fprintln(&b)
			}
			fmt.Fprintf(&b, "# %s\n\n", r.ElementName)
			fmt.Fprintln(&b, "| source | target | residency_p50 | residency_p95 | cpu_load | sum_of_means | cross_ratio | dominant_child |")
			fmt.Fprintln(&b, "|---|---|---|---|---|---|---|---|")
			prev = r.ElementName
		}
		src := fmt.Sprintf("%dx%d", r.Config.SourceWidth, r.Config.SourceHeight)
		dst := fmt.Sprintf("%dx%d", r.Config.TargetWidth, r.Config.TargetHeight)
		dom := dominantChild(r)
		domStr := "-"
		if dom.Name != "" {
			domStr = fmt.Sprintf("%s %v", dom.Name, dom.Mean)
		}
		fmt.Fprintf(&b, "| %s | %s | %v | %v | %.1f%% | %v | %.2f | %s |\n",
			src, dst, r.ResidencyP50, r.ResidencyP95, r.CPULoadPct, r.SumMean, r.CrossRatio, domStr)
	}
	return b.String()
}

// FormatCSV returns the results as CSV with a single header row covering
// all elements. Durations are emitted in nanoseconds so numbers
// round-trip cleanly into spreadsheets and pandas.
func FormatCSV(results []Result) string {
	var b strings.Builder
	fmt.Fprintln(&b, "element,source_w,source_h,target_w,target_h,fps,num_buffers,residency_p50_ns,residency_p95_ns,cpu_load_pct,sum_of_means_ns,cross_ratio,dominant_child,dominant_child_mean_ns")
	for _, r := range results {
		dom := dominantChild(r)
		fmt.Fprintf(&b, "%s,%d,%d,%d,%d,%d,%d,%d,%d,%.3f,%d,%.4f,%s,%d\n",
			r.ElementName,
			r.Config.SourceWidth, r.Config.SourceHeight,
			r.Config.TargetWidth, r.Config.TargetHeight,
			r.Config.SourceFPS, r.Config.NumBuffers,
			r.ResidencyP50.Nanoseconds(), r.ResidencyP95.Nanoseconds(),
			r.CPULoadPct,
			r.SumMean.Nanoseconds(),
			r.CrossRatio,
			dom.Name, dom.Mean.Nanoseconds(),
		)
	}
	return b.String()
}
