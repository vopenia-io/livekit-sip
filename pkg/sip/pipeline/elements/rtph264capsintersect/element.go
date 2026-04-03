package rtph264capsintersect

import (
	"fmt"
	"sync/atomic"

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

type RtpH264CapsIntersect struct {
	maxResEmitted atomic.Bool
}

func (e *RtpH264CapsIntersect) New() glib.GoObjectSubclass {
	return &RtpH264CapsIntersect{}
}

func (e *RtpH264CapsIntersect) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"RTP H264 Caps Intersect",
		"Filter/Network/Video",
		"Performs semantic H.264 profile-level-id intersection on application/x-rtp caps",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
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

	gst.SignalNew(
		class.Type(),
		"max-resolution",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_INT, glib.TYPE_INT,
	)
}

func (e *RtpH264CapsIntersect) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseTransform(instance)
	self.SetInPlace(true)
}

// sdpFmtpFields are SDP-specific fmtp fields that have no meaning to upstream
// GStreamer elements (x264enc, rtph264pay) and should be stripped.
var sdpFmtpFields = []string{
	"max-fs", "max-mbps", "max-br", "max-dpb", "max-smbps", "max-fps",
	"packetization-mode",
}

func (e *RtpH264CapsIntersect) TransformCaps(self *base.GstBaseTransform, direction gst.PadDirection, caps, filter *gst.Caps) *gst.Caps {
	// On SRC→SINK calls with connected downstream, compute and emit max resolution once
	if direction == gst.PadDirectionSource && !e.maxResEmitted.Load() {
		e.emitMaxResolution(self)
	}

	// Strip profile-level-id and SDP fmtp fields from caps so that string
	// mismatches (e.g. 42c01f vs 42e01f) don't block negotiation.
	// Then intersect with the original filter — this re-adds the filter's plid
	// into the result, keeping it a proper subset (avoids BaseTransform's
	// "not a real subset" error). SetCaps stamps the final plid later.
	result := caps.Copy()
	for i := 0; i < result.GetSize(); i++ {
		st := result.GetStructureAt(i)
		st.RemoveValue("profile-level-id")
		for _, field := range sdpFmtpFields {
			st.RemoveValue(field)
		}
	}

	if filter != nil {
		result = result.Intersect(filter)
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("transform_caps dir=%d: %s", direction, result))
	return result.Ref()
}

// SetCaps stamps the downstream profile-level-id onto outcaps.
// This is a blind rewrite — no semantic intersection. Safe because:
// - rtph264pay already constrained x264enc to a compatible profile
// - The level in the SPS may differ from downstream, but plidtransform rewrites the string
func (e *RtpH264CapsIntersect) SetCaps(self *base.GstBaseTransform, incaps, outcaps *gst.Caps) bool {
	downstream := self.SrcPad().PeerQueryCaps(nil)
	if downstream == nil || downstream.IsEmpty() || downstream.IsAny() || downstream.GetSize() == 0 {
		self.Log(CAT, gst.LevelDebug, "SetCaps: no downstream constraint, passing through")
		return true
	}

	downPLID := getProfileLevelID(downstream.GetStructureAt(0))
	if downPLID == "" {
		self.Log(CAT, gst.LevelDebug, "SetCaps: downstream has no profile-level-id")
		return true
	}

	if outcaps.GetSize() > 0 {
		st := outcaps.GetStructureAt(0)
		if err := st.SetValue("profile-level-id", downPLID); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("SetCaps: failed to set profile-level-id: %v", err))
		}
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("SetCaps: stamped profile-level-id=%s onto outcaps", downPLID))
	return true
}

func (e *RtpH264CapsIntersect) emitMaxResolution(self *base.GstBaseTransform) {
	downstream := self.SrcPad().PeerQueryCaps(nil)
	if downstream == nil || downstream.IsEmpty() || downstream.IsAny() || downstream.GetSize() == 0 {
		return
	}

	downPLID := getProfileLevelID(downstream.GetStructureAt(0))
	if downPLID == "" {
		return
	}

	// Mark as emitted so we don't fire again
	if !e.maxResEmitted.CompareAndSwap(false, true) {
		return
	}

	maxW, maxH, ok := maxResolutionForLevel(downPLID, 30)
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("could not compute max resolution for plid=%s", downPLID))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("emitting max-resolution: %dx%d for plid=%s", maxW, maxH, downPLID))
	if _, err := self.Element.Emit("max-resolution", maxW, maxH); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to emit max-resolution signal: %v", err))
	}
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
