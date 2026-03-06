package livekitbin

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/tracks"
)

func PadProbeForwardTrackSourceInfo(wpad *glib.WeakRef) func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	return func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		event := info.GetEvent()
		if event == nil {
			return gst.PadProbePass
		}
		if event.Type() == gst.EventTypeCustomDownstreamSticky && event.HasName(tracks.EventTrackSourceInfo) {
			dest := gst.ToPad(wpad.Get())
			if dest != nil {
				dest.PushEvent(event.Copy())
			}
			return gst.PadProbePass
		}

		return gst.PadProbePass
	}
}

func PadProbeDropTrackSourceInfo(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	event := info.GetEvent()
	if event == nil {
		return gst.PadProbePass
	}
	if event.Type() == gst.EventTypeCustomDownstreamSticky && event.HasName(tracks.EventTrackSourceInfo) {
		return gst.PadProbeDrop
	}

	return gst.PadProbePass
}

func (e *LivekitBin) ProbeOnTrackEOS(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	event := info.GetEvent()
	if event == nil || event.Type() != gst.EventTypeEOS {
		return gst.PadProbePass
	}

	pad.RemoveProbe(uint64(info.ID()))

	self := gst.ToGstBin(pad.GetParent())
	if self == nil || self.Instance() == nil {
		return gst.PadProbeDrop
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track pad %s received EOS event", pad.GetName()))

	src := pad.GetParentElement()
	if src == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No parent element found for pad %s", pad.GetName()))
		return gst.PadProbeDrop
	}

	e.RemoveTrack(src)
	return gst.PadProbeDrop
}

func (e *LivekitBin) TrackSourceFromSessionSSRC(session, ssrc uint) *gst.Element {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return nil
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_MICROPHONE:
	case livekit.TrackSource_CAMERA:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source for session %d and SSRC %d", session, ssrc))
		return nil
	}

	var (
		rp    *lksdk.RemoteTrackPublication
		found bool
	)
	participants := e.room.GetRemoteParticipants()
	for _, participant := range participants {
		track, ok := participant.GetTrackPublication(kind).(*lksdk.RemoteTrackPublication)
		if track == nil || !ok {
			continue
		}
		remote := track.TrackRemote()
		if remote == nil {
			continue
		}
		if uint(remote.SSRC()) == ssrc {
			rp = track
			found = true
			break
		}
	}
	if !found {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No track found for session %d and SSRC %d", session, ssrc))
		return nil
	}

	element, err := self.GetElementByName(tracks.SrcTrackName(rp.SID()))
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No element found for track with SID %s: %v", rp.SID(), err))
		return nil
	}
	return element
}
