package lkroom

import (
	"errors"
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

func (s *lkroom) SometimesTrackAdded(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if !s.state.WaitJoined() {
		s.self.Log(CAT, gst.LevelError, "Failed to join room - cannot add track (should not happen)")
		return
	}

	kind := uint(pub.Source())
	if kind == uint(livekit.TrackSource_UNKNOWN) {
		s.self.Log(CAT, gst.LevelWarning, "SometimesTrackAdded called with unknown track source")
		return
	}

	// if kind == uint(livekit.TrackSource_MICROPHONE) {
	// 	s.self.Log(CAT, gst.LevelInfo, "SometimesTrackAdded called for audio track - ignoring")
	// 	return
	// }

	ssrc := track.SSRC()
	padname := fmt.Sprintf("src_%d_%d", kind, ssrc)
	s.self.Log(CAT, gst.LevelInfo, "Creating sometimes pad "+padname)

	srcTrack, err := gst.NewElement("lkroom_srctrack")
	if err != nil {
		s.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create srcTrack element: %v", err))
		s.self.Error("Failed to create srcTrack element", err)
		return
	}

	if obj, ok := gst.SubclassFromElement[*SrcTrack](srcTrack); ok {
		obj.track = track
		obj.pub = pub
		obj.rp = rp
		obj.parent = s
	} else {
		s.self.Log(CAT, gst.LevelError, "Failed to cast srcTrack to SrcTrack subclass")
		s.self.Error("Failed to cast srcTrack to SrcTrack subclass", errors.New("type assertion failed"))
		return
	}

	if err := s.self.Add(srcTrack); err != nil {
		s.self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add srcTrack element: %v", err))
		s.self.Error("Failed to add srcTrack element", err)
		return
	}

	pad := srcTrack.GetStaticPad("src")
	if pad == nil {
		s.self.Log(CAT, gst.LevelError, "Failed to get src pad from srcTrack element")
		s.self.Error("Failed to get src pad from srcTrack element", errors.New("pad is nil"))
		return
	}

	gpad := gst.NewGhostPad(padname, pad)
	if gpad == nil {
		s.self.Log(CAT, gst.LevelError, "Failed to create ghost pad for srcTrack element")
		s.self.Error("Failed to create ghost pad for srcTrack element", errors.New("ghost pad is nil"))
		return
	}

	if !gpad.SetActive(true) {
		s.self.Log(CAT, gst.LevelError, "Failed to activate ghost pad for srcTrack element")
		s.self.Error("Failed to activate ghost pad for srcTrack element", errors.New("failed to activate ghost pad"))
		return
	}

	if !s.self.AddPad(gpad.Pad) {
		s.self.Log(CAT, gst.LevelError, "Failed to add ghost pad to lkroom element")
		s.self.Error("Failed to add ghost pad to lkroom element", errors.New("failed to add ghost pad"))
		return
	}

	rtcpPad := srcTrack.GetStaticPad("src_rtcp")
	if rtcpPad == nil {
		s.self.Log(CAT, gst.LevelError, "Failed to get rtcp src pad from srcTrack element")
		s.self.Error("Failed to get rtcp src pad from srcTrack element", errors.New("pad is nil"))
		return
	}

	rtcpGPad := gst.NewGhostPad(padname+"_rtcp", rtcpPad)
	if rtcpGPad == nil {
		s.self.Log(CAT, gst.LevelError, "Failed to create ghost rtcp pad for srcTrack element")
		s.self.Error("Failed to create ghost rtcp pad for srcTrack element", errors.New("ghost pad is nil"))
		return
	}

	if !rtcpGPad.SetActive(true) {
		s.self.Log(CAT, gst.LevelError, "Failed to activate ghost rtcp pad for srcTrack element")
		s.self.Error("Failed to activate ghost rtcp pad for srcTrack element", errors.New("failed to activate ghost pad"))
		return
	}

	if !s.self.AddPad(rtcpGPad.Pad) {
		s.self.Log(CAT, gst.LevelError, "Failed to add ghost rtcp pad to lkroom element")
		s.self.Error("Failed to add ghost rtcp pad to lkroom element", errors.New("failed to add ghost pad"))
		return
	}

	if !srcTrack.SyncStateWithParent() {
		s.self.Log(CAT, gst.LevelError, "Failed to sync state of srcTrack element")
		s.self.Error("Failed to sync state of srcTrack element", errors.New("failed to sync state"))
		return
	}

	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Sometimes pad %s created and linked", padname))

}
