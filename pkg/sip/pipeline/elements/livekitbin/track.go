package livekitbin

import (
	"fmt"
	"strings"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/pion/webrtc/v4"
)

const QDataPadPeerRef = "livekitbin-pad-peer-ref"
const QDataSessionID = "livekitbin-session-id"

func (e *LivekitBin) PublishTrack(self *gst.Bin, pad *gst.Pad, pname string) {
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Publishing track with pad name: %s", pname))

	var session int
	_, err := fmt.Sscanf(pname, "send_rtp_src_%d", &session)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error parsing pad name %s: %v", pname, err))
		return
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
	case livekit.TrackSource_CAMERA:
	case livekit.TrackSource_SCREEN_SHARE:
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in pad name: %s", pname))
		return
	}

	element, _, err := livekittracks.NewSinkTrack(e.room.LocalParticipant, livekit.TrackSource(session))
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating sink track for pad name %s: %v", pname, err))
		return
	}
	if err := self.Add(element); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding sink track for pad name %s: %v", pname, err))
		self.Error(fmt.Sprintf("Error adding sink track for pad name %s", pname), err)
		return
	}

	sinkPad := element.GetStaticPad("sink")
	if e.Is(RoomStateJoined) {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("LivekitBin already joined room, publishing track for pad name: %s", pname))
		if !element.SyncStateWithParent() {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error syncing state with parent for pad name %s: %v", pname, err))
			self.Error(fmt.Sprintf("Error syncing state with parent for pad name %s", pname), fmt.Errorf("sync error"))
			return
		}
	} else {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("LivekitBin not joined room yet, deferring publish for pad name: %s", pname))
		probe := sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, livekittracks.PadProbeDrop)
		element.SetLockedState(true)
		welement := glib.WeakRefInit(element)
		e.wg.Add(1)
		go e.handleTrackJoin(welement, pname, probe)
	}

	if ret := pad.Link(sinkPad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking pad to sink track for pad name %s: %v", pname, ret))
		self.Error(fmt.Sprintf("Error linking pad to sink track for pad name %s", pname), fmt.Errorf("link error: %v", ret))
		return
	}

	pad.SetQData(QDataSessionID, session)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Added sink track for pad name: %s", pname))

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		rtcpSrc := e.RtpBin.GetRequestPad(fmt.Sprintf("send_rtcp_src_%d", session))
		if rtcpSrc == nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get RTCP source pad for session: %d", session))
			self.Error(fmt.Sprintf("Failed to get RTCP source pad for session: %d", session), fmt.Errorf("pad error"))
			return
		}
		dstPad := e.RtcpFunnel.GetRequestPad(fmt.Sprintf("sink_%d", session))
		if ret := rtcpSrc.Link(dstPad); ret != gst.PadLinkOK {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking RTCP source pad to RTCP funnel for session %d: %v", session, ret))
			self.Error(fmt.Sprintf("Error linking RTCP source pad to RTCP funnel for session %d", session), fmt.Errorf("link error: %v", ret))
			return
		}

		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linked RTCP source pad to RTCP funnel for session: %d", session))
	}()
}

func (e *LivekitBin) handleTrackJoin(welement *glib.WeakRef, pname string, probe uint64) {
	defer e.wg.Done()
	err := e.Wait(RoomStateJoined)
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}
	element := gst.ToElement(welement.Get())
	if element == nil {
		self.Log(CAT, gst.LevelError, "Element is nil in PublishTrack goroutine")
		return
	}
	sinkPad := element.GetStaticPad("sink")
	if sinkPad == nil {
		self.Log(CAT, gst.LevelError, "Sink pad is nil in PublishTrack goroutine")
		return
	}
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("LivekitBin failed to join room for pad name %s: %v", pname, err))
		self.Error(fmt.Sprintf("LivekitBin failed to join room for pad name %s", pname), err)
		element.SetLockedState(false)
		return
	}

	if !element.SyncStateWithParent() {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error syncing state with parent for pad name %s", pname))
		self.Error(fmt.Sprintf("Error syncing state with parent for pad name %s", pname), fmt.Errorf("sync error"))
		element.SetLockedState(false)
		return
	}

	element.SetLockedState(false)

	sinkPad.RemoveProbe(probe)
}

func (e *LivekitBin) ForwardPublishTrack(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	if name == "" {
		self.Log(CAT, gst.LevelError, "Requested pad name is empty")
		return nil
	}

	var session int
	if _, err := fmt.Sscanf(name, "send_rtp_sink_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error parsing requested pad name %s: %v", name, err))
		return nil
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
	case livekit.TrackSource_CAMERA:
	case livekit.TrackSource_SCREEN_SHARE:
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in pad name: %s", name))
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Forwarding publish track with session: %d", session))

	pad := e.RtpBin.GetRequestPad(fmt.Sprintf("send_rtp_sink_%d", session))
	if pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad for name: %s", name))
		return nil
	}

	class := gst.ToElementClass(e.RtpBin.Class())

	gpad := gst.NewGhostPadFromTemplate(name, pad, class.GetPadTemplate("send_rtp_sink_%u"))
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating ghost pad for requested pad name %s", name))
		return nil
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding ghost pad for requested pad name %s", name))
		return nil
	}

	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad for name: %s", name))
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Created ghost pad for requested pad name: %s", name))

	return gpad.Pad
}

func (e *LivekitBin) SubscribeTrack(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}
	if err := e.Wait(RoomStateJoined); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error waiting for room to be joined: %v", err))
		self.Error(fmt.Sprintf("Error waiting for room to be joined: %v", err), err)
		return
	}

	// if err := e.Wait(RoomStatePlaying); err != nil {
	// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Error waiting for room to be playing: %v", err))
	// 	self.Error(fmt.Sprintf("Error waiting for room to be playing: %v", err), err)
	// 	return
	// }

	_, enc, ok := strings.Cut(track.Codec().MimeType, "/")
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid codec mime type for pt (%d): %s", track.PayloadType(), track.Codec().MimeType))
	} else {
		e.encodingMu.RLock()
		existing, ok := e.encodingPT[uint8(track.PayloadType())]
		e.encodingMu.RUnlock()
		if !ok {
			e.encodingMu.Lock()
			e.encodingPT[uint8(track.PayloadType())] = strings.ToUpper(enc)
			e.encodingMu.Unlock()
		} else {
			if existing != strings.ToUpper(enc) {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Codec mime type for pt (%d) changed from %s to %s", track.PayloadType(), existing, enc))
				self.Error(fmt.Sprintf("Codec mime type for pt (%d) changed from %s to %s", track.PayloadType(), existing, enc), fmt.Errorf("codec change"))
				return
			}
		}
	}

	if element, err := self.GetElementByName(livekittracks.SrcTrackName(publication.SID())); err == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Track with SID %s already exists", publication.SID()))
		if err := element.SetProperty("subscribed", true); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting subscribed property for track %s: %v", track.ID(), err))
			self.Error(fmt.Sprintf("Error setting subscribed property for track %s", track.ID()), err)
		}
		return
	}

	element, err := livekittracks.NewSrcTrack(track, publication, rp)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating source track for track %s: %v", track.ID(), err))
		return
	}
	element.GetStaticPad("src").AddProbe(gst.PadProbeTypeEventDownstream, livekittracks.PadProbeDropEOS)
	if err := self.Add(element); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding source track for track %s: %v", track.ID(), err))
		self.Error(fmt.Sprintf("Error adding source track for track %s", track.ID()), err)
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Subscribed to track with ID: %s", track.ID()))

	var rtpFunnel *gst.Element
	var rtcpFunnel *gst.Element
	switch publication.Source() {
	case livekit.TrackSource_MICROPHONE:
		rtpFunnel = e.MicrophoneRtpFunnel
		rtcpFunnel = e.MicrophoneRtcpFunnel
	case livekit.TrackSource_CAMERA:
		rtpFunnel = e.CameraRtpFunnel
		rtcpFunnel = e.CameraRtcpFunnel
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source for track %s: %d", track.ID(), publication.Source()))
		return
	}

	element.SetQData(QDataSessionID, int(publication.Source()))

	if ret := element.GetStaticPad("src").Link(rtpFunnel.GetRequestPad(fmt.Sprintf("sink_%d", track.SSRC()))); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking source pad to sink funnel for track %s: %v", track.ID(), ret))
		self.Error(fmt.Sprintf("Error linking source pad to sink funnel for track %s", track.ID()), fmt.Errorf("link error: %v", ret))
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linked source pad to sink funnel for track ID: %s", track.ID()))

	if ret := element.GetStaticPad("src_rtcp").Link(rtcpFunnel.GetRequestPad(fmt.Sprintf("sink_%d", track.SSRC()))); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking RTCP source pad to sink funnel for track %s: %v", track.ID(), ret))
		self.Error(fmt.Sprintf("Error linking RTCP source pad to sink funnel for track %s", track.ID()), fmt.Errorf("link error: %v", ret))
		return
	}

	if !element.SyncStateWithParent() {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error syncing state with parent for track %s: %v", track.ID(), err))
		self.Error(fmt.Sprintf("Error syncing state with parent for track %s", track.ID()), fmt.Errorf("sync error"))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linked RTCP source pad to sink funnel for track ID: %s", track.ID()))
}

func (e *LivekitBin) ForwardSubscribeTrack(self *gst.Bin, pad *gst.Pad, pname string) {
	var session, ssrc, pt int
	_, err := fmt.Sscanf(pname, "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error parsing pad name %s: %v", pname, err))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Forwarding track with session: %d, ssrc: %d, pt: %d", session, ssrc, pt))
	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
	case livekit.TrackSource_CAMERA:
	case livekit.TrackSource_SCREEN_SHARE:
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in pad name: %s", pname))
		return
	}

	srcElem := e.TrackSourceFromSessionSSRC(uint(session), uint(ssrc))
	if srcElem == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to find track source element for pad name: %s", pname))
		return
	}
	src, ok := gst.SubclassFromElement[*livekittracks.SrcTrack](srcElem)
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to cast track source element to SrcTrack for pad name: %s", pname))
		return
	}
	sid := src.Pub.SID()

	class := gst.ToElementClass(self.Class())

	gpname := fmt.Sprintf("recv_rtp_src_%d_%d_%d", session, ssrc, pt)
	gpad := gst.NewGhostPadFromTemplate(gpname, pad, class.GetPadTemplate("recv_rtp_src_%u_%u_%u"))
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating ghost pad for pad name %s", pname))
		return
	}
	gpad.SetQData(livekittracks.QDataSrcTrackSource, sid)

	srcPad := srcElem.GetStaticPad("src")

	wpad := glib.WeakRefInit(pad)
	srcPad.AddProbe(gst.PadProbeTypeEventDownstream, PadProbeForwardTrackSourceInfo(wpad))
	srcPad.StickyEventsForEach(func(_ *gst.Pad, event *gst.Event) bool {
		if event.Type() == gst.EventTypeCustomDownstreamSticky && event.HasName(livekittracks.EventTrackSourceInfo) {
			dest := gst.ToPad(wpad.Get())
			if dest != nil {
				dest.PushEvent(event.Copy())
			}
			return false
		}
		return true
	})

	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding ghost pad for pad name %s", pname))
		return
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error activating ghost pad for pad name %s", pname))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("new track with pad name: %s", pname))
}
