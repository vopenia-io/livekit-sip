package rtph264capsintersect

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

var CAT = gst.NewDebugCategory(
	"rtph264capsintersect",
	gst.DebugColorNone,
	"RTP H264 profile-level-id caps intersection",
)

const padCapsStr = "application/x-rtp, media=(string)video, encoding-name=(string)H264"

type RtpH264CapsIntersect struct{}

func (e *RtpH264CapsIntersect) New() glib.GoObjectSubclass {
	return &RtpH264CapsIntersect{}
}

func (e *RtpH264CapsIntersect) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"RTP H264 Caps Intersect",
		"Filter/Network/Video",
		"Performs semantic H.264 profile-level-id intersection on application/x-rtp caps",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	caps := gst.NewCapsFromString(padCapsStr)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		caps,
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		caps,
	))
}

func (e *RtpH264CapsIntersect) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseTransform(instance)
	self.SetPassthrough(true)
	self.SetTransformIPOnPassthrough(false)
}

func (e *RtpH264CapsIntersect) TransformCaps(self *base.GstBaseTransform, direction gst.PadDirection, caps, filter *gst.Caps) *gst.Caps {
	if direction == gst.PadDirectionSource {
		return e.transformCapsUpstream(self, caps, filter)
	}
	return e.transformCapsDownstream(self, caps, filter)
}

// transformCapsUpstream handles downstream→upstream caps queries.
// Removes profile-level-id so rtph264pay isn't constrained by an exact string.
func (e *RtpH264CapsIntersect) transformCapsUpstream(self *base.GstBaseTransform, caps, filter *gst.Caps) *gst.Caps {
	result := caps.Copy()
	for i := 0; i < result.GetSize(); i++ {
		result.GetStructureAt(i).RemoveValue("profile-level-id")
	}
	if filter != nil {
		result = result.Intersect(filter)
	}
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("transform_caps upstream: %s", result))
	return result.Ref()
}

// transformCapsDownstream handles upstream→downstream caps transformation.
// Computes semantic profile-level-id intersection between upstream and downstream.
func (e *RtpH264CapsIntersect) transformCapsDownstream(self *base.GstBaseTransform, caps, filter *gst.Caps) *gst.Caps {
	peerCaps := self.SrcPad().PeerQueryCaps(nil)
	if peerCaps == nil || peerCaps.IsEmpty() || peerCaps.IsAny() {
		// No downstream constraint — pass through
		result := caps.Copy()
		if filter != nil {
			result = result.Intersect(filter)
		}
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("transform_caps downstream (no peer): %s", result))
		return result.Ref()
	}

	result := caps.Copy()
	downstreamPLID := getProfileLevelID(peerCaps.GetStructureAt(0))

	for i := 0; i < result.GetSize(); i++ {
		st := result.GetStructureAt(i)
		upstreamPLID := getProfileLevelID(st)

		if upstreamPLID == "" {
			upstreamPLID = defaultProfileLevelID
		}

		if downstreamPLID == "" {
			// No downstream profile-level-id — pass through upstream value
			continue
		}

		intersected, ok := intersectProfileLevelID(upstreamPLID, downstreamPLID)
		if !ok {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf(
				"incompatible profiles: upstream=%s downstream=%s", upstreamPLID, downstreamPLID))
			return gst.NewEmptyCaps().Ref()
		}

		if err := st.SetValue("profile-level-id", intersected); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to set profile-level-id: %v", err))
		}
	}

	if filter != nil {
		result = result.Intersect(filter)
	}
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("transform_caps downstream: %s", result))
	return result.Ref()
}

func (e *RtpH264CapsIntersect) TransformIP(self *base.GstBaseTransform, buf *gst.Buffer) gst.FlowReturn {
	return gst.FlowOK
}

func getProfileLevelID(st *gst.Structure) string {
	v, err := st.GetValue("profile-level-id")
	if err != nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
