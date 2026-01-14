package lkroom

import (
	"fmt"
	"runtime/cgo"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

func (s *lkroom) SometimesTrackAdded(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	trackinfo := &RemoteTrackInfo{
		track: track,
		pub:   pub,
		rp:    rp,
	}

	kind := uint(pub.Source())
	if kind == uint(livekit.TrackSource_UNKNOWN) {
		s.self.Log(CAT, gst.LevelWarning, "SometimesTrackAdded called with unknown track source")
		return
	}

	if kind == uint(livekit.TrackSource_MICROPHONE) {
		s.self.Log(CAT, gst.LevelInfo, "SometimesTrackAdded called for audio track - ignoring")
		return
	}

	ssrc := track.SSRC()
	padname := fmt.Sprintf("src_%d_%d", kind, ssrc)
	s.self.Log(CAT, gst.LevelInfo, "Creating sometimes pad "+padname)

	sHnd := cgo.NewHandle(s)
	defer sHnd.Delete()
	tHnd := cgo.NewHandle(trackinfo)
	defer tHnd.Delete()

	srcTrack, err := gst.NewElementWithProperties("lkroom_srctrack", map[string]interface{}{
		"parent": uint64(sHnd),
		"track":  uint64(tHnd),
	})
	if err != nil {
		s.self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating srcTrack element: %v", err))
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error creating srcTrack element", err.Error())
		return
	}

	if err := s.self.Add(srcTrack); err != nil {
		s.self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding srcTrack element: %v", err))
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error adding srcTrack element", err.Error())
		return
	}

	pad := srcTrack.GetStaticPad("src")
	if pad == nil {
		s.self.Log(CAT, gst.LevelError, "Error getting src pad from srcTrack element")
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error getting src pad from srcTrack element", "pad is nil")
		return
	}

	gpad := gst.NewGhostPad(padname, pad)
	if gpad == nil {
		s.self.Log(CAT, gst.LevelError, "Error creating ghost pad for srcTrack element")
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error creating ghost pad for srcTrack element", "ghost pad is nil")
		return
	}

	if !gpad.SetActive(true) {
		s.self.Log(CAT, gst.LevelError, "Error activating ghost pad for srcTrack element")
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error activating ghost pad for srcTrack element", "failed to activate ghost pad")
		return
	}

	if !s.self.AddPad(gpad.Pad) {
		s.self.Log(CAT, gst.LevelError, "Error adding ghost pad to lkroom element")
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error adding ghost pad to lkroom element", "failed to add ghost pad")
		return
	}

	if !srcTrack.SyncStateWithParent() {
		s.self.Log(CAT, gst.LevelError, "Error syncing state of srcTrack element")
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error syncing state of srcTrack element", "failed to sync state")
		return
	}

	s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Sometimes pad %s created and linked", padname))

}
