package livekitbin

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

func (e *LivekitBin) OnRtpBinRequestPtMap(session, pt uint) *gst.Caps {
	self := gst.ToGstBin(e.self.Get())
	if self == nil {
		CAT.Log(gst.LevelError, "LivekitBin instance is nil in OnRtpBinRequestPtMap")
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Received request-pt-map signal for session %d, pt %d", session, pt))

	var rtpfunnel *gst.Element
	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		rtpfunnel = e.MicrophoneRtpFunnel
	case livekit.TrackSource_CAMERA:
		rtpfunnel = e.CameraRtpFunnel
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in request-pt-map signal: %d", session))
		return nil
	}

	sinks, err := rtpfunnel.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting sink pads from rtpfunnel: %v", err))
		return nil
	}
	if len(sinks) == 0 {
		self.Log(CAT, gst.LevelError, "No sink pads found on rtpfunnel")
		return nil
	}

	for _, sink := range sinks {
		caps := sink.GetCurrentCaps()
		if caps == nil {
			self.Log(CAT, gst.LevelWarning, "Sink pad has no current caps")
			continue
		}
		structure := caps.GetStructureAt(0)
		if structure == nil {
			self.Log(CAT, gst.LevelWarning, "Caps has no structure")
			continue
		}
		ptVal, err := structure.GetValue("payload")
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Error getting payload from structure: %v", err))
			continue
		}
		capsPt, ok := ptVal.(int)
		if !ok {
			self.Log(CAT, gst.LevelWarning, "Payload is not a uint")
			continue
		}
		if uint(capsPt) != pt {
			continue
		}
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Found matching payload type %d in caps: %s", pt, caps.String()))
		return e.rebuildCaps(self, capsPt, structure)
	}

	return nil
}

func (e *LivekitBin) rebuildCaps(self *gst.Bin, pt int, structure *gst.Structure) *gst.Caps {
	mediaVal, err := structure.GetValue("media")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting media from structure: %v", err))
		return nil
	}
	media, ok := mediaVal.(string)
	if !ok {
		self.Log(CAT, gst.LevelError, "Media is not a string")
		return nil
	}

	clockRateVal, err := structure.GetValue("clock-rate")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting clock-rate from structure: %v", err))
		return nil
	}
	clockRate, ok := clockRateVal.(int)
	if !ok {
		self.Log(CAT, gst.LevelError, "Clock-rate is not an int")
		return nil
	}

	encodingNameVal, err := structure.GetValue("encoding-name")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting encoding-name from structure: %v", err))
		return nil
	}
	encodingName, ok := encodingNameVal.(string)
	if !ok {
		self.Log(CAT, gst.LevelError, "Encoding-name is not a string")
		return nil
	}

	capsStr := fmt.Sprintf("application/x-rtp, media=(string)%s, clock-rate=(int)%d, encoding-name=(string)%s, payload=(int)%d", media, clockRate, encodingName, pt)

	caps := gst.NewCapsFromString(capsStr)
	if caps == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating caps from string: %s", capsStr))
		return nil
	}
	return caps
}