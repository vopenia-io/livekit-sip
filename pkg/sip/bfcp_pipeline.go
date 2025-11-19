package sip

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/protocol/logger"
)

const contentPipelineTemplate = `
  appsrc name=content_in format=3 is-live=true do-timestamp=true max-bytes=5000000 block=false
      caps="application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96" !
      rtpjitterbuffer name=content_jitterbuffer latency=100 do-lost=true do-retransmission=false drop-on-latency=false !
      rtpvp8depay request-keyframe=true !
      vp8dec !
      videoconvert !
      video/x-raw,format=I420 !
      x264enc bitrate=%d key-int-max=30 bframes=0 rc-lookahead=0 sliced-threads=true sync-lookahead=0 tune=zerolatency speed-preset=%s !
      h264parse config-interval=1 !
      rtph264pay pt=%d mtu=1200 config-interval=1 aggregate-mode=zero-latency !
      appsink name=content_out emit-signals=false drop=false max-buffers=100 sync=false
`

type BFCPPipelineConfig struct {
	Bitrate     int
	PayloadType uint8
	Preset      string
}

func DefaultBFCPPipelineConfig() *BFCPPipelineConfig {
	return &BFCPPipelineConfig{
		Bitrate:     2000,
		PayloadType: 126,
		Preset:      "ultrafast",
	}
}

type BFCPPipeline struct {
	config *BFCPPipelineConfig
	log    logger.Logger

	activePipeline  *gst.Pipeline
	standbyPipeline *gst.Pipeline

	activeInput  *GstWriter
	activeOutput *GstReader

	standbyInput  *GstWriter
	standbyOutput *GstReader

	activeTrackID  string
	standbyTrackID string

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.RWMutex
	running bool
}

func NewBFCPPipeline(config *BFCPPipelineConfig, log logger.Logger) *BFCPPipeline {
	if config == nil {
		config = DefaultBFCPPipelineConfig()
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &BFCPPipeline{
		config: config,
		log:    log,
		ctx:    ctx,
		cancel: cancel,
	}
}

func (p *BFCPPipeline) Start(trackID string, inputSource io.Reader, outputSink io.Writer) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return fmt.Errorf("pipeline already running")
	}

	p.log.Infow("🎬 [BFCP-Pipeline] Starting content pipeline",
		"trackID", trackID,
		"bitrate", p.config.Bitrate,
		"payloadType", p.config.PayloadType,
	)

	pipeline, input, output, err := p.createPipeline()
	if err != nil {
		return fmt.Errorf("failed to create pipeline: %w", err)
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to start pipeline: %w", err)
	}

	p.activePipeline = pipeline
	p.activeInput = input
	p.activeOutput = output
	p.activeTrackID = trackID
	p.running = true

	go p.copyInput(inputSource, input, "active")
	go p.copyOutput(output, outputSink, "active")

	p.log.Infow("🎬 [BFCP-Pipeline] ✅ Content pipeline started",
		"trackID", trackID,
	)

	return nil
}

func (p *BFCPPipeline) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil
	}

	p.log.Infow("🛑 [BFCP-Pipeline] Stopping content pipeline",
		"activeTrack", p.activeTrackID,
		"standbyTrack", p.standbyTrackID,
	)

	p.cancel()

	if p.activePipeline != nil {
		p.activePipeline.SetState(gst.StateNull)
		p.activePipeline = nil
	}

	if p.standbyPipeline != nil {
		p.standbyPipeline.SetState(gst.StateNull)
		p.standbyPipeline = nil
	}

	if p.activeInput != nil {
		p.activeInput.Close()
		p.activeInput = nil
	}

	if p.activeOutput != nil {
		p.activeOutput.Close()
		p.activeOutput = nil
	}

	if p.standbyInput != nil {
		p.standbyInput.Close()
		p.standbyInput = nil
	}

	if p.standbyOutput != nil {
		p.standbyOutput.Close()
		p.standbyOutput = nil
	}

	p.activeTrackID = ""
	p.standbyTrackID = ""
	p.running = false

	p.log.Infow("🛑 [BFCP-Pipeline] ✅ Content pipeline stopped")

	return nil
}

func (p *BFCPPipeline) Switch(newTrackID string, inputSource io.Reader, outputSink io.Writer) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return fmt.Errorf("pipeline not running")
	}

	p.log.Infow("🔄 [BFCP-Pipeline] Switching content source",
		"fromTrack", p.activeTrackID,
		"toTrack", newTrackID,
	)

	standbyPipeline, standbyInput, standbyOutput, err := p.createPipeline()
	if err != nil {
		return fmt.Errorf("failed to create standby pipeline: %w", err)
	}

	if err := standbyPipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to start standby pipeline: %w", err)
	}

	p.standbyPipeline = standbyPipeline
	p.standbyInput = standbyInput
	p.standbyOutput = standbyOutput
	p.standbyTrackID = newTrackID

	go p.copyInput(inputSource, standbyInput, "standby")
	go p.copyOutput(standbyOutput, outputSink, "standby")

	time.Sleep(100 * time.Millisecond)

	if p.activePipeline != nil {
		p.activePipeline.SetState(gst.StateNull)
	}
	if p.activeInput != nil {
		p.activeInput.Close()
	}
	if p.activeOutput != nil {
		p.activeOutput.Close()
	}

	p.activePipeline = p.standbyPipeline
	p.activeInput = p.standbyInput
	p.activeOutput = p.standbyOutput
	p.activeTrackID = p.standbyTrackID

	p.standbyPipeline = nil
	p.standbyInput = nil
	p.standbyOutput = nil
	p.standbyTrackID = ""

	p.log.Infow("🔄 [BFCP-Pipeline] ✅ Content source switched",
		"newTrack", newTrackID,
	)

	return nil
}

func (p *BFCPPipeline) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

func (p *BFCPPipeline) GetActiveTrack() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.activeTrackID
}

func (p *BFCPPipeline) createPipeline() (*gst.Pipeline, *GstWriter, *GstReader, error) {
	pipelineStr := fmt.Sprintf(
		contentPipelineTemplate,
		p.config.Bitrate,
		p.config.Preset,
		p.config.PayloadType,
	)

	pipeline, err := gst.NewPipelineFromString(pipelineStr)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create pipeline: %w", err)
	}

	inputElement, err := pipeline.GetElementByName("content_in")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get input element: %w", err)
	}

	outputElement, err := pipeline.GetElementByName("content_out")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get output element: %w", err)
	}

	inputWriter, err := NewGstWriter(app.SrcFromElement(inputElement))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create input writer: %w", err)
	}

	outputReader, err := NewGstReader(app.SinkFromElement(outputElement))
	if err != nil {
		inputWriter.Close()
		return nil, nil, nil, fmt.Errorf("failed to create output reader: %w", err)
	}

	return pipeline, inputWriter, outputReader, nil
}

func (p *BFCPPipeline) copyInput(src io.Reader, dst *GstWriter, label string) {
	p.log.Infow("📥 [BFCP-Pipeline] Starting input copy",
		"label", label,
		"track", p.getTrackForLabel(label),
	)

	buf := make([]byte, 32768)
	totalBytes := 0

	for {
		select {
		case <-p.ctx.Done():
			p.log.Infow("📥 [BFCP-Pipeline] Input copy stopped (context)",
				"label", label,
				"totalBytes", totalBytes,
			)
			return
		default:
		}

		n, err := src.Read(buf)
		if n > 0 {
			totalBytes += n
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				p.log.Warnw("📥 [BFCP-Pipeline] Input write error", writeErr,
					"label", label,
					"totalBytes", totalBytes,
				)
				return
			}
		}

		if err != nil {
			if err != io.EOF {
				p.log.Warnw("📥 [BFCP-Pipeline] Input read error", err,
					"label", label,
					"totalBytes", totalBytes,
				)
			} else {
				p.log.Infow("📥 [BFCP-Pipeline] Input copy completed",
					"label", label,
					"totalBytes", totalBytes,
				)
			}
			return
		}
	}
}

func (p *BFCPPipeline) copyOutput(src *GstReader, dst io.Writer, label string) {
	p.log.Infow("📤 [BFCP-Pipeline] Starting output copy",
		"label", label,
		"track", p.getTrackForLabel(label),
	)

	buf := make([]byte, 32768)
	totalBytes := 0

	for {
		select {
		case <-p.ctx.Done():
			p.log.Infow("📤 [BFCP-Pipeline] Output copy stopped (context)",
				"label", label,
				"totalBytes", totalBytes,
			)
			return
		default:
		}

		n, err := src.Read(buf)
		if n > 0 {
			totalBytes += n
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				p.log.Warnw("📤 [BFCP-Pipeline] Output write error", writeErr,
					"label", label,
					"totalBytes", totalBytes,
				)
				return
			}
		}

		if err != nil {
			if err != io.EOF {
				p.log.Warnw("📤 [BFCP-Pipeline] Output read error", err,
					"label", label,
					"totalBytes", totalBytes,
				)
			} else {
				p.log.Infow("📤 [BFCP-Pipeline] Output copy completed",
					"label", label,
					"totalBytes", totalBytes,
				)
			}
			return
		}
	}
}

func (p *BFCPPipeline) getTrackForLabel(label string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if label == "active" {
		return p.activeTrackID
	} else if label == "standby" {
		return p.standbyTrackID
	}
	return ""
}

func (p *BFCPPipeline) GetStats() map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return map[string]interface{}{
		"running":      p.running,
		"activeTrack":  p.activeTrackID,
		"standbyTrack": p.standbyTrackID,
		"hasActive":    p.activePipeline != nil,
		"hasStandby":   p.standbyPipeline != nil,
		"bitrate":      p.config.Bitrate,
		"payloadType":  p.config.PayloadType,
	}
}
