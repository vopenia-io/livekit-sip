package sip

import (
	"fmt"
	"sync/atomic"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/screenshare_pipeline"
)

type ScreenshareManager struct {
	*VideoManager
	active atomic.Bool
}

func NewScreenshareManager(log logger.Logger, room *Room, opts *MediaOptions) (*ScreenshareManager, error) {
	sm := &ScreenshareManager{}

	vm, err := NewVideoManager(log, room, opts, sm)
	if err != nil {
		return nil, err
	}
	sm.VideoManager = vm

	return sm, nil
}

func (sm *ScreenshareManager) NewPipeline(media *sdpv2.SDPMedia) (pipeline.GspPipeline, error) {
	pipeline, err := screenshare_pipeline.New(sm.log, media.Codec.PayloadType)
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP WebRTC pipeline: %w", err)
	}
	// pipeline.Monitor()

	// setup SIP to WebRTC pipeline
	// link rtp path
	sipRtpIn, err := NewGstWriter(pipeline.WebrtcToSip.RtpAppSrc)
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP RTP reader: %w", err)
	}
	go sm.Copy(sipRtpIn, sm.sipRtpIn)

	webrtcRtpOut, err := NewGstReader(pipeline.WebrtcToSip.RtpAppSink)
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP writer: %w", err)
	}
	go sm.Copy(sm.webrtcRtpOut, webrtcRtpOut)

	return pipeline, nil
}

func (sm *ScreenshareManager) WebrtcTrackInput(ti *TrackInput, sid string, ssrc uint32) {
	if sm.active.Swap(true) {
		sm.log.Warnw("WebRTC→SIP screenshare pipeline already active, ignoring additional track input", nil)
		return
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.status != VideoStatusStarted {
		sm.log.Warnw("video manager not started, cannot add WebRTC track input", nil, "status", sm.status)
		return
	}

	sm.log.Infow("WebRTC video track subscribed - connecting WebRTC→SIP pipeline",
		"hasRtpIn", ti.RtpIn != nil,
		"hasRtcpIn", ti.RtcpIn != nil)

	if r := sm.sipRtpIn.Swap(ti.RtpIn); r != nil {
		_ = r.Close()
	}
}

func (sm *ScreenshareManager) Stop() error {
	if err := sm.VideoManager.Stop(); err != nil {
		return err
	}
	sm.active.Store(false)
	return nil
}
