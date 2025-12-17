package camera_pipeline

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type CameraPipeline struct {
	*pipeline.BasePipeline

	SipToWebrtc *SipToWebrtc
	WebrtcToSip *WebrtcToSip

	// Signal handles for auto-switching cleanup
	inputSelectorPadAddedHandle   glib.SignalHandle
	inputSelectorPadRemovedHandle glib.SignalHandle
	inputSelectorActivePadHandle  glib.SignalHandle
}

func New(log logger.Logger) (*CameraPipeline, error) {
	cp := &CameraPipeline{}

	p, err := pipeline.New(log)
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	cp.BasePipeline = p

	cp.SipToWebrtc, err = pipeline.CastErr[*SipToWebrtc](cp.AddChain(buildSipToWebRTCChain(log.WithComponent("sip_to_webrtc"))))
	if err != nil {
		return nil, err
	}

	cp.WebrtcToSip, err = pipeline.CastErr[*WebrtcToSip](cp.AddChain(buildSelectorToSipChain(log.WithComponent("selector_to_sip"))))
	if err != nil {
		return nil, err
	}

	if err := cp.setupAutoSwitching(); err != nil {
		return nil, err
	}

	return cp, nil
}

func (cp *CameraPipeline) Configure(media *sdpv2.SDPMedia) error {
	if err := cp.SipToWebrtc.Configure(media); err != nil {
		return fmt.Errorf("failed to configure SIP to WebRTC chain: %w", err)
	}
	if err := cp.WebrtcToSip.Configure(media); err != nil {
		return fmt.Errorf("failed to configure WebRTC to SIP chain: %w", err)
	}
	return nil
}

// RtpSink implements sip.SipPipeline.
func (gp *CameraPipeline) RtpSink() *app.Sink {
	return gp.WebrtcToSip.SipRtpAppSink
}

// RtpSrc implements sip.SipPipeline.
func (gp *CameraPipeline) RtpSrc() *app.Source {
	return gp.SipToWebrtc.SipRtpAppSrc
}

// RtcpSink implements sip.SipPipeline.
func (gp *CameraPipeline) RtcpSink() *app.Sink {
	return gp.SipToWebrtc.SipRtcpAppSink
}

// RtcpSrc implements sip.SipPipeline.
func (gp *CameraPipeline) RtcpSrc() *app.Source {
	return gp.SipToWebrtc.SipRtcpAppSrc
}

// Close overrides BasePipeline.Close to clean up chains before base cleanup
func (cp *CameraPipeline) Close() error {
	cp.Mu.Lock()
	defer cp.Mu.Unlock()

	if cp.Closed() {
		return nil
	}

	// Mark as closed first to stop monitor goroutine
	cp.BasePipeline.MarkClosed()

	// 1. Disconnect InputSelector signal handlers first (prevents callbacks during cleanup)
	if cp.WebrtcToSip != nil && cp.WebrtcToSip.InputSelector != nil {
		pipeline.DisconnectSignal(cp.WebrtcToSip.InputSelector, cp.inputSelectorPadAddedHandle)
		pipeline.DisconnectSignal(cp.WebrtcToSip.InputSelector, cp.inputSelectorPadRemovedHandle)
		pipeline.DisconnectSignal(cp.WebrtcToSip.InputSelector, cp.inputSelectorActivePadHandle)
	}
	cp.inputSelectorPadAddedHandle = 0
	cp.inputSelectorPadRemovedHandle = 0
	cp.inputSelectorActivePadHandle = 0

	// 2. Close chains in reverse order of creation
	// Each chain sets its elements to NULL, releases pads, removes elements, and unrefs
	if cp.WebrtcToSip != nil {
		if err := cp.WebrtcToSip.Close(cp.Pipeline); err != nil {
			cp.Log.Warnw("failed to close WebrtcToSip chain", err)
		}
		cp.WebrtcToSip = nil
	}

	if cp.SipToWebrtc != nil {
		if err := cp.SipToWebrtc.Close(cp.Pipeline); err != nil {
			cp.Log.Warnw("failed to close SipToWebrtc chain", err)
		}
		cp.SipToWebrtc = nil
	}

	// 3. Set pipeline to NULL state after chains are cleaned up
	if cp.Pipeline != nil {
		if err := cp.Pipeline.SetState(gst.StateNull); err != nil {
			cp.Log.Warnw("failed to set pipeline to NULL", err)
		}
		// 4. Unref the pipeline
		cp.Pipeline.Unref()
		cp.Pipeline = nil
	}

	return nil
}
