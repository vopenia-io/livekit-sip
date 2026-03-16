package livekitbin

import (
	"fmt"
	"strings"
	"weak"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
)

func (e *LivekitBin) setupRtpBinSignals(self *gst.Bin) {
	eweak := weak.Make(e)
	if _, err := e.RtpBin.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		ptr := eweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "LivekitBin instance is nil in rtpbin pad-added callback")
			return
		}
		ptr.OnRtpBinPadAdded(pad)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin pad-added signal: %v", err))
		self.Error("Error connecting to rtpbin pad-added signal", err)
		return
	}

	if _, err := e.RtpBin.Connect("pad-removed", func(_ *gst.Element, pad *gst.Pad) {
		ptr := eweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "LivekitBin instance is nil in rtpbin pad-removed callback")
			return
		}
		ptr.OnRtpBinPadRemoved(pad)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin pad-removed signal: %v", err))
		self.Error("Error connecting to rtpbin pad-removed signal", err)
		return
	}

	if _, err := e.RtpBin.Connect("element-removed", func(_ *gst.Element, element *gst.Element) {
		ptr := eweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "LivekitBin instance is nil in rtpbin element-removed callback")
			return
		}
		ptr.OnElementRemoved(element)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin element-removed signal: %v", err))
		self.Error("Error connecting to rtpbin element-removed signal", err)
		return
	}

	if _, err := e.RtpBin.Connect("request-pt-map", func(_ *gst.Element, session, pt uint) *gst.Caps {
		ptr := eweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "LivekitBin instance is nil in rtpbin request-pt-map callback")
			return nil
		}
		return ptr.OnRtpBinRequestPtMap(session, pt)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin request-pt-map signal: %v", err))
		self.Error("Error connecting to rtpbin request-pt-map signal", err)
		return
	}

	if _, err := e.RtpBin.Connect("on-ssrc-collision", func(_ *gst.Element, session uint, ssrc uint) {
		ptr := eweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "LivekitBin instance is nil in rtpbin on-ssrc-collision callback")
			return
		}
		ptr.OnSSRCCollision(session, ssrc)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin on-ssrc-collision signal: %v", err))
		self.Error("Error connecting to rtpbin on-ssrc-collision signal", err)
		return
	}

	if _, err := e.RtpBin.Connect("on-timeout", func(_ *gst.Element, session uint, ssrc uint) {
		ptr := eweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "LivekitBin instance is nil in rtpbin on-timeout callback")
			return
		}
		ptr.OnTimeout(session, ssrc)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin on-timeout signal: %v", err))
		self.Error("Error connecting to rtpbin on-timeout signal", err)
		return
	}
}

func (e *LivekitBin) OnRtpBinPadAdded(pad *gst.Pad) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	pname := pad.GetName()
	if strings.Contains(pname, "_sink_") {
		return
	}

	handles := []struct {
		prefix  string
		handler func(self *gst.Bin, pad *gst.Pad, pname string)
	}{
		{"send_rtp_src_", e.PublishTrack},
		{"recv_rtp_src_", e.ForwardSubscribeTrack},
	}

	for _, h := range handles {
		if strings.HasPrefix(pname, h.prefix) {
			h.handler(self, pad, pname)
			return
		}
	}
}

func (e *LivekitBin) OnRtpBinPadRemoved(pad *gst.Pad) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	pname := pad.GetName()
	if strings.Contains(pname, "_sink_") {
		return
	}

	handles := []struct {
		prefix  string
		handler func(self *gst.Bin, pad *gst.Pad, pname string)
	}{
		{"send_rtp_src_", e.CleanupRtpSink},
		{"recv_rtp_src_", e.GhostPadRemove},
		{"send_rtcp_src_", e.CleanupRtcpPad},
	}

	for _, h := range handles {
		if strings.HasPrefix(pname, h.prefix) {
			h.handler(self, pad, pname)
			return
		}
	}
}

func (e *LivekitBin) OnElementRemoved(element *gst.Element) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	name := element.GetName()
	if !strings.HasPrefix(name, livekittracks.SrcTrackNamePrefix) {
		return
	}
	e.CleanupSrcTrack(self, element, name)
}

func (e *LivekitBin) OnRtpBinRequestPtMap(session, pt uint) *gst.Caps {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return nil
	}

	e.encodingMu.RLock()
	encoding, knownEnc := e.encodingPT[uint8(pt)]
	e.encodingMu.RUnlock()

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Received request-pt-map signal for session %d, pt %d", session, pt))
	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		if !knownEnc {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown payload type %d for microphone track, defaulting to OPUS", pt))
			encoding = "OPUS"
		}
		return gst.NewCapsFromString(fmt.Sprintf("application/x-rtp, media=(string)audio, clock-rate=(int)48000, encoding-name=(string)%s, payload=(int)%d, rtcp-fb-nack-pli=(boolean)true, rtcp-fb-ccm-fir=(boolean)true", encoding, pt))
	case livekit.TrackSource_CAMERA:
		if !knownEnc {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown payload type %d for camera track, defaulting to VP8", pt))
			encoding = "VP8"
		}
		return gst.NewCapsFromString(fmt.Sprintf("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)%s, payload=(int)%d, rtcp-fb-nack-pli=(boolean)true, rtcp-fb-ccm-fir=(boolean)true", encoding, pt))
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in request-pt-map signal: %d", session))
		return nil
	}
}

func (e *LivekitBin) OnSSRCCollision(session, ssrc uint) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelWarning, fmt.Sprintf("SSRC collision detected in session %d for SSRC %d", session, ssrc))
}

// func (e *LivekitBin) OnByeSSRC(session, ssrc uint) {
// 	self := gst.ToGstBin(e.self.Get())
// 	if self == nil || self.Instance() == nil {
// 		return
// 	}

// 	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Received BYE for SSRC %d in session %d", ssrc, session))

// 	if _, err := e.RtpBin.Emit("clear-ssrc", session, ssrc); err != nil {
// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting clear-ssrc signal: %v", err))
// 		self.Error("Error emitting clear-ssrc signal", err)
// 		return
// 	}
// }

func (e *LivekitBin) OnTimeout(session, ssrc uint) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("SSRC %d in session %d has timed out", ssrc, session))

	if _, err := e.RtpBin.Emit("clear-ssrc", session, ssrc); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting clear-ssrc signal: %v", err))
		self.Error("Error emitting clear-ssrc signal", err)
		return
	}
}
