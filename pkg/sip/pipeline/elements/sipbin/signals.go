package sipbin

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

func (e *SipBin) OnAckSDP(self *gst.Bin, b []byte) error {
	unlock := e.transaction.Ack()
	defer unlock()

	// TODO: handle late answer here

	return nil
}

func (e *SipBin) onRtpBinRequestPtMap(self *gst.Bin, session int, pt uint8) *gst.Caps {
	e.mu.Lock()
	defer e.mu.Unlock()

	kind := livekit.TrackSource(session)

	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received request for payload type map for unsupported track source %d", kind))
		return nil
	}

	caps, exist := e.PtMap[kind][pt]
	if !exist {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received request for payload type map for payload type %d which was not in the original offer for track source %d", pt, kind))
		return nil
	}

	return caps
}

func (e *SipBin) onRtpBinPadAdded(self *gst.Bin, pad *gst.Pad) {
	templ := pad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad %s on rtpbin, but failed to get template", pad.GetName()))
		return
	}

	switch templ.GetName() {
	case "send_rtp_src_%u":
		e.onRtpBinPadAddedSendRtpSrc(self, pad)
	case "recv_rtp_src_%u_%u_%u":
		e.onRtpBinPadAddedRecvRtpSrc(self, pad)
	default:
		self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Received new pad %s on rtpbin with unrecognized template %s", pad.GetName(), templ.GetName()))
	}
}

func (e *SipBin) onRtpBinPadAddedSendRtpSrc(self *gst.Bin, pad *gst.Pad) {
	var session int
	if _, err := fmt.Sscanf(pad.GetName(), "send_rtp_src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad %s on rtpbin, but failed to parse session number: %v", pad.GetName(), err))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad %s for unsupported track source %d", pad.GetName(), kind))
		return
	}

	// WARNING: this is callind in sync after the rtpbin pad request which already hold the lock. locking here cause deadlock because we are already locked
	// e.mu.Lock()
	// defer e.mu.Unlock()

	ti := e.Tracks[kind]
	if ti == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad %s for track source %d, but no track info found", pad.GetName(), kind))
		return
	}

	if ret := pad.Link(ti.RtpSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link new pad %s to RTP sink for track source %d: %v", pad.GetName(), kind, ret))
		self.Error(fmt.Sprintf("Failed to link new pad %s to RTP sink for track source %d", pad.GetName(), kind), fmt.Errorf("link failed: %v", ret))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linked new pad %s from rtpbin to RTP sink for track source %d", pad.GetName(), kind))

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()

		e.mu.Lock()
		defer e.mu.Unlock()

		if e.RtpBin == nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("RtpBin is nil when trying to link RTCP pad for new RTP pad %s", pad.GetName()))
			return
		}

		ti := e.Tracks[kind]
		if ti == nil || ti.RtcpSink == nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Track info for track source %d not found when linking RTCP pad after new RTP pad %s added", kind, pad.GetName()))
			return
		}

		rtcpPad := e.RtpBin.GetRequestPad(fmt.Sprintf("send_rtcp_src_%d", session))
		if rtcpPad == nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad for RTCP source for track source %d", kind))
			return
		}
		if ret := rtcpPad.Link(ti.RtcpSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link RTCP pad %s to RTCP sink for track source %d: %v", rtcpPad.GetName(), kind, ret))
			self.Error(fmt.Sprintf("Failed to link RTCP pad %s to RTCP sink for track source %d", rtcpPad.GetName(), kind), fmt.Errorf("link failed: %v", ret))
			return
		}

		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linked RTCP pad %s from rtpbin to RTCP sink for track source %d", rtcpPad.GetName(), kind))
	}()
}

func (e *SipBin) onRtpBinPadAddedRecvRtpSrc(self *gst.Bin, pad *gst.Pad) {
	var (
		session int
		ssrc    int
		pt      int
	)

	if _, err := fmt.Sscanf(pad.GetName(), "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad %s on rtpbin, but failed to parse session, ssrc, and payload type: %v", pad.GetName(), err))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad %s for unsupported track source %d", pad.GetName(), kind))
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exist := e.PtMap[kind][uint8(pt)]; !exist {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad %s for payload type %d which was not in the original offer for track source %d", pad.GetName(), pt, kind))
		return
	}

	class := gst.ToElementClass(self.Class())

	gpad := gst.NewGhostPadFromTemplate(pad.GetName(), pad, class.GetPadTemplate("recv_rtp_src_%u_%u_%u"))
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for new RTP source pad %s", pad.GetName()))
		return
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad for new RTP source pad %s", pad.GetName()))
		return
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad for new RTP source pad %s", pad.GetName()))
		return
	}
}

func (e *SipBin) onRtpBinPadRemoved(self *gst.Bin, pad *gst.Pad) {
	templ := pad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Pad %s removed from rtpbin, but failed to get template", pad.GetName()))
		return
	}

	switch templ.GetName() {
	case "send_rtp_src_%u":
		e.onRtpBinPadRemovedSendRtpSrc(self, pad)
	default:
		self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Pad %s removed from rtpbin with unrecognized template %s", pad.GetName(), templ.GetName()))
	}
}

func (e *SipBin) onRtpBinPadRemovedSendRtpSrc(self *gst.Bin, pad *gst.Pad) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var session int
	if _, err := fmt.Sscanf(pad.GetName(), "send_rtp_src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Pad %s removed from rtpbin, but failed to parse session number: %v", pad.GetName(), err))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Pad %s removed from rtpbin for unsupported track source %d", pad.GetName(), kind))
		return
	}

	ti := e.Tracks[kind]
	if ti == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Pad %s removed from rtpbin for track source %d, but no track info found", pad.GetName(), kind))
		return
	}

	if err := e.CleanupTrack(self, ti); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to cleanup track for track source %d after pad %s removed from rtpbin: %v", kind, pad.GetName(), err))
		self.Error(fmt.Sprintf("Failed to cleanup track for track source %d after pad %s removed from rtpbin", kind, pad.GetName()), err)
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Pad %s removed from rtpbin, cleaned up track for track source %d", pad.GetName(), kind))
}
