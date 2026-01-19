package lkroom

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/activeselector"
)

func (s *lkroom) SelectActiveSpeaker(p []lksdk.Participant) {
	if len(p) == 0 {
		s.self.Log(CAT, gst.LevelInfo, "no active speakers found")
		return
	}
	var pub *lksdk.RemoteTrackPublication = nil
	var ok bool
	for _, t := range p {
		pub, ok = t.GetTrackPublication(livekit.TrackSource_CAMERA).(*lksdk.RemoteTrackPublication)
		if pub != nil && ok {
			break
		} else {
			pub = nil
		}
	}

	if pub == nil {
		s.self.Log(CAT, gst.LevelInfo, "no active camera track found among active speakers")
		return
	}

	ssrc := pub.TrackRemote().SSRC()

	pname := fmt.Sprintf("src_%d_%d", uint(livekit.TrackSource_CAMERA), ssrc)

	pad := s.self.GetStaticPad(pname)

	if pad == nil {
		s.self.Log(CAT, gst.LevelWarning, fmt.Sprintf("no pad found for active speaker track with ssrc %d", ssrc))
		return
	}

	event, err := (&activeselector.ActiveTrackEvent{
		TrackSSRC: uint32(ssrc),
		TrackKind: uint32(livekit.TrackSource_CAMERA),
	}).MarshalEvent()
	if err != nil {
		s.self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to marshal active-track event: %v", err))
		return
	}
	if !pad.PushEvent(event) {
		s.self.Log(CAT, gst.LevelWarning, fmt.Sprintf("failed to push active-track event to pad %s", pname))
		return
	}
}
