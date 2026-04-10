package benchmarks

import (
	"sort"
	"time"
)

type gpuSample struct {
	t       time.Time
	smUtil  uint32
	encUtil uint32
	decUtil uint32
}

type gpuLoadStats struct {
	Samples       int
	Min, Max      float64
	Mean          float64
	P50, P90      float64
}

type gpuProbe struct {
	interval  time.Duration
	startCh   <-chan struct{}
	available bool

	samples []gpuSample
	stopCh  chan struct{}
	doneCh  chan struct{}
}

func newGPUProbe(interval time.Duration, startCh <-chan struct{}) *gpuProbe {
	p := &gpuProbe{
		interval: interval,
		startCh:  startCh,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	p.available = gpuprobeInit()
	return p
}

func (p *gpuProbe) start() {
	go p.run()
}

func (p *gpuProbe) run() {
	defer close(p.doneCh)
	if !p.available {
		return
	}

	select {
	case <-p.startCh:
	case <-p.stopCh:
		return
	}

	sample := func() {
		raw, ok := gpuprobeSample()
		if !ok {
			return
		}
		p.samples = append(p.samples, gpuSample{
			t:       time.Now(),
			smUtil:  raw.smUtil,
			encUtil: raw.encUtil,
			decUtil: raw.decUtil,
		})
	}

	sample()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			sample()
			return
		case <-ticker.C:
			sample()
		}
	}
}

func (p *gpuProbe) stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
	<-p.doneCh
	if p.available {
		gpuprobeShutdown()
		p.available = false
	}
}

func (p *gpuProbe) loadStats(cutoff time.Time) (sm, enc, dec gpuLoadStats) {
	samples := p.samples
	if !cutoff.IsZero() {
		n := len(samples)
		for i, s := range samples {
			if s.t.After(cutoff) {
				n = i
				break
			}
		}
		samples = samples[:n]
	}
	if len(samples) < 1 {
		return
	}

	trim := int(float64(len(samples)) * trimRatio)
	if len(samples)-2*trim < 1 {
		return
	}
	samples = samples[trim : len(samples)-trim]

	sm = computeGPUStats(samples, func(s gpuSample) float64 { return float64(s.smUtil) })
	enc = computeGPUStats(samples, func(s gpuSample) float64 { return float64(s.encUtil) })
	dec = computeGPUStats(samples, func(s gpuSample) float64 { return float64(s.decUtil) })
	return
}

func computeGPUStats(samples []gpuSample, extract func(gpuSample) float64) gpuLoadStats {
	vals := make([]float64, len(samples))
	var sum float64
	for i, s := range samples {
		v := extract(s)
		vals[i] = v
		sum += v
	}
	sort.Float64s(vals)
	pct := func(q float64) float64 {
		idx := int(float64(len(vals)-1) * q)
		return vals[idx]
	}
	return gpuLoadStats{
		Samples: len(vals),
		Min:     vals[0],
		Max:     vals[len(vals)-1],
		Mean:    sum / float64(len(vals)),
		P50:     pct(0.50),
		P90:     pct(0.90),
	}
}
