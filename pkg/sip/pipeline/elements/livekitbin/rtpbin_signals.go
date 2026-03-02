package livekitbin

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

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
