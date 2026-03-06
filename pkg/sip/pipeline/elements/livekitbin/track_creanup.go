package livekitbin

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/tracks"
	"github.com/pion/webrtc/v4"
)

func (e *LivekitBin) CleanupSrcTrack(self *gst.Bin, element *gst.Element, name string) {

	session, ok := element.GetQData(QDataSessionID).(int)
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Removed element %s does not have a session ID", name))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Element removed: %s with session ID: %d", name, session))

	src, ok := gst.SubclassFromElement[*tracks.SrcTrack](element)
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Element %s is not a SrcTrack", name))
		return
	}

	ssrc := src.SSRC
	if ssrc == 0 {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("SrcTrack element %s does not have a valid SSRC", name))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Cleaning up track with session ID: %d and SSRC: %d", session, ssrc))

	var (
		rtpFunnel  *gst.Element
		rtcpFunnel *gst.Element
	)
	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		rtpFunnel = e.MicrophoneRtpFunnel
		rtcpFunnel = e.MicrophoneRtcpFunnel
	case livekit.TrackSource_CAMERA:
		rtpFunnel = e.CameraRtpFunnel
		rtcpFunnel = e.CameraRtcpFunnel
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in element name: %s", name))
		return
	}

	rtpPad := rtpFunnel.GetStaticPad(fmt.Sprintf("sink_%d", ssrc))
	if rtpPad != nil {
		rtpFunnel.ReleaseRequestPad(rtpPad)
	}

	rtcpPad := rtcpFunnel.GetStaticPad(fmt.Sprintf("sink_%d", ssrc))
	if rtcpPad != nil {
		rtcpFunnel.ReleaseRequestPad(rtcpPad)
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Cleaned up track with session ID: %d and SSRC: %d", session, ssrc))
}

func (e *LivekitBin) GhostPadRemove(self *gst.Bin, pad *gst.Pad, pname string) {
	gpad := self.GetStaticPad(pname)
	if gpad == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No ghost pad found for removed pad %s", pname))
		return
	}

	if !gpad.SetActive(false) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to deactivate ghost pad %s for removal", pname))
	}

	if !self.RemovePad(gpad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad %s for removal", pname))
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed ghost pad %s", pname))
}

func (e *LivekitBin) UnsubscribeAll() {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, "Unsubscribing from all tracks")

	participants := e.room.GetRemoteParticipants()
	for _, rp := range participants {
		for _, track := range rp.TrackPublications() {
			pub, ok := track.(*lksdk.RemoteTrackPublication)
			if !ok {
				continue
			}
			track := pub.TrackRemote()
			pub.SetSubscribed(false)
			e.UnsubscribeTrack(track, pub, rp)
		}
	}
}

func (e *LivekitBin) UnsubscribeTrack(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	src, err := self.GetElementByName(tracks.SrcTrackName(pub.SID()))
	if err != nil {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("No source element found for track %s of participant %s", pub.SID(), rp.SID()))
		return
	}

	e.RemoveTrack(src)
}

func (e *LivekitBin) RemoveTrack(src *gst.Element) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removing track with element name: %s", src.GetName()))

	if err := src.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting track element to NULL for track %s: %v", src.GetName(), err))
		self.Error(fmt.Sprintf("Error setting track element to NULL for track %s", src.GetName()), err)
		return
	}

	if err := self.Remove(src); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error removing source element for track %s: %v", src.GetName(), err))
		self.Error(fmt.Sprintf("Error removing source element for track %s", src.GetName()), err)
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed track with element name: %s", src.GetName()))
}

func (e *LivekitBin) CleanupRtpSink(self *gst.Bin, pad *gst.Pad, pname string) {
	session, ok := pad.GetQData(QDataSessionID).(int)
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No session ID found for RTP pad %s", pname))
		return
	}

	sink, err := self.GetElementByName(tracks.SinkTrackName(session))
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No sink element found for RTP pad %s with session ID %d", pname, session))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Cleaning up RTP pad %s connected to sink %s", pname, sink.GetName()))

	e.UnpublishTrack(self, sink)

	rtcpPad := e.RtpBin.GetStaticPad(fmt.Sprintf("send_rtcp_src_%d", session))
	if rtcpPad != nil {
		e.RtpBin.ReleaseRequestPad(rtcpPad)
	}
}

func (e *LivekitBin) UnpublishTrack(self *gst.Bin, sink *gst.Element) {
	if err := sink.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting sink element to NULL for track %s: %v", sink.GetName(), err))
		self.Error(fmt.Sprintf("Error setting sink element to NULL for track %s", sink.GetName()), err)
		return
	}

	if err := self.Remove(sink); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error removing sink element for track %s: %v", sink.GetName(), err))
		self.Error(fmt.Sprintf("Error removing sink element for track %s", sink.GetName()), err)
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Unpublished track with sink element name: %s", sink.GetName()))
}

func (e *LivekitBin) CleanupRtcpPad(self *gst.Bin, pad *gst.Pad, pname string) {

	var session int
	if _, err := fmt.Sscanf(pname, "send_rtcp_src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Error parsing pad name %s: %v", pname, err))
		return
	}

	funnelSink := e.RtcpFunnel.GetStaticPad(fmt.Sprintf("sink_%d", session))
	if funnelSink == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No sink pad found on RTCP funnel for session %d", session))
		return
	}

	e.RtcpFunnel.ReleaseRequestPad(funnelSink)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Cleaning up RTCP pad %s connected to RTCP funnel", pname))

}
