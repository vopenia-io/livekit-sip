package sip

import (
	"context"
	"fmt"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline/camera_pipeline"
)

type CameraManager struct {
	*VideoManager
	ssrcs map[string]uint32
}

func NewCameraManager(log logger.Logger, ctx context.Context, room *Room, opts *MediaOptions) (*CameraManager, error) {
	cm := &CameraManager{
		ssrcs: make(map[string]uint32),
	}

	vm, err := NewVideoManager(log, ctx, opts, cm)
	if err != nil {
		return nil, err
	}
	cm.VideoManager = vm

	return cm, nil
}

func (cm *CameraManager) CreateVideoPipeline(opt *MediaOptions) (SipPipeline, error) {
	pipeline, err := camera_pipeline.New(cm.log)
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP WebRTC pipeline: %w", err)
	}
	pipeline.Monitor()

	return pipeline, nil
}

func (cm *CameraManager) WebrtcTrackInput(ti *TrackInput, sid string, ssrc uint32) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.status != VideoStatusStarted {
		cm.log.Warnw("video manager not started, cannot add WebRTC track input", nil, "status", cm.status)
		return
	}

	cm.log.Infow("WebRTC video track subscribed - connecting WebRTC→SIP pipeline",
		"hasRtpIn", ti.RtpIn != nil,
		"hasRtcpIn", ti.RtcpIn != nil)

	s, err := cm.pipeline.(*camera_pipeline.CameraPipeline).AddWebRTCSourceToSelector(ssrc)
	if err != nil {
		cm.log.Errorw("failed to add WebRTC source to selector", err)
		return
	}

	cm.ssrcs[sid] = ssrc

	rtpMi, err := NewMediaInput(cm.ctx, s.RtpAppSrc, ti.RtpIn)
	if err != nil {
		cm.log.Errorw("failed to create WebRTC RTP media input", err)
		return
	}
	if err := cm.io.AddInputs(rtpMi); err != nil {
		cm.log.Errorw("failed to add WebRTC RTP media input", err)
		return
	}

	rtcpMi, err := NewMediaInput(cm.ctx, s.RtcpAppSrc, ti.RtcpIn)
	if err != nil {
		cm.log.Errorw("failed to create WebRTC RTCP media input", err)
		return
	}
	if err := cm.io.AddInputs(rtcpMi); err != nil {
		cm.log.Errorw("failed to add WebRTC RTCP media input", err)
		return
	}
}

func (cm *CameraManager) WebrtcTrackOutput(to *TrackOutput) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.status != VideoStatusStarted {
		cm.log.Warnw("video manager not started, cannot add WebRTC track output", nil, "status", cm.status)
		return
	}

	cm.log.Infow("WebRTC video track published - connecting SIP→WebRTC pipeline",
		"hasRtpOut", to.RtpOut != nil,
		"hasRtcpOut", to.RtcpOut != nil)

	pipeline := cm.pipeline.(*camera_pipeline.CameraPipeline)

	rtpMo, err := NewMediaOutput(cm.ctx, to.RtpOut, pipeline.SipToWebrtc.WebrtcRtpAppSink)
	if err != nil {
		cm.log.Errorw("failed to create WebRTC RTP media output", err)
		return
	}
	if err := cm.io.AddOutputs(rtpMo); err != nil {
		cm.log.Errorw("failed to add WebRTC RTP media output", err)
		return
	}

	rtcpMo, err := NewMediaOutput(cm.ctx, to.RtcpOut, pipeline.WebrtcToSip.WebrtcRtcpAppSink)
	if err != nil {
		cm.log.Errorw("failed to create WebRTC RTCP media output", err)
		return
	}
	if err := cm.io.AddOutputs(rtcpMo); err != nil {
		cm.log.Errorw("failed to add WebRTC RTCP media output", err)
		return
	}

}

func (cm *CameraManager) SwitchActiveWebrtcTrack(sid string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	ssrc, ok := cm.ssrcs[sid]
	if !ok {
		return fmt.Errorf("no SSRC found for sid %s", sid)
	}
	cm.log.Debugw("switching active WebRTC video track", "sid", sid, "ssrc", ssrc)

	if err := cm.pipeline.(*camera_pipeline.CameraPipeline).SwitchWebRTCSelectorSource(ssrc); err != nil {
		return fmt.Errorf("failed to switch WebRTC selector source: %w", err)
	}

	return nil
}
