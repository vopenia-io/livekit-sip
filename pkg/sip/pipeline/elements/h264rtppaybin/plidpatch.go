package h264rtppaybin

import (
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

// plidPatch is an internal passthrough BaseTransform used inside
// H264RtpPayBin after rtph264pay. It solves two asymmetric problems in
// RTP-side caps negotiation:
//
//  1. Upstream-flowing caps/queries: downstream typically pins a single
//     profile-level-id (e.g. 42e01f). rtph264pay's own output caps carry
//     the plid that falls out of the encoded bitstream, which may differ
//     (e.g. 42c01f) even if the bitstream is semantically compatible.
//     TransformCaps strips plid (and SDP-only fmtp fields) so rtph264pay
//     can negotiate without string-matching on that field.
//
//  2. Downstream-flowing CAPS event: rtph264pay will otherwise emit the
//     bitstream plid on its src event, which downstream rejects. SetCaps
//     blindly stamps downstream's plid onto outcaps so the CAPS event is
//     accepted. The H.264-domain constraint that actually decides the
//     bitstream lives in H264RtpPayBin's profile_capsfilter — this
//     element only massages the RTP-side strings around it.
type plidPatch struct {
	plidResolved atomic.Bool
	plid         profileLevelID
}

func (e *plidPatch) New() glib.GoObjectSubclass { return &plidPatch{} }

func (e *plidPatch) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"H264 RTP plid patcher",
		"Filter/Network/Video",
		"Strips profile-level-id on negotiation and stamps downstream's onto outcaps",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	caps := gst.NewCapsFromString(srcCapsStr)
	class.AddPadTemplate(gst.NewPadTemplate("sink", gst.PadDirectionSink, gst.PadPresenceAlways, caps))
	class.AddPadTemplate(gst.NewPadTemplate("src", gst.PadDirectionSource, gst.PadPresenceAlways, caps))

	// plid-resolved fires once per element lifetime, the first time a caps
	// negotiation reveals downstream's profile-level-id. H264RtpPayBin uses
	// this to program its profile_capsfilter and emit max-resolution at the
	// correct moment — caps negotiation, not state change, because in a
	// factorybin-driven pipeline state changes run before ghost pad targets
	// are set so peer-query returns ANY.
	gst.SignalNew(
		class.Type(),
		"plid-resolved",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_STRING,
		glib.TYPE_INT,
		glib.TYPE_INT,
	)
}

func (e *plidPatch) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseTransform(instance)
	self.SetPassthrough(true)
}

func (e *plidPatch) TransformCaps(self *base.GstBaseTransform, direction gst.PadDirection, caps, filter *gst.Caps) *gst.Caps {
	// On src→sink queries (downstream is asking "what can you accept?"),
	// peer-query the external downstream to learn its profile-level-id and
	// emit plid-resolved exactly once. This runs during caps negotiation,
	// which is the earliest moment ghost pad targets are guaranteed to be
	// wired through in a factorybin pipeline.
	if direction == gst.PadDirectionSource && !e.plidResolved.Load() {
		e.resolvePlid(self)
	}

	result := caps.Copy()
	for i := 0; i < result.GetSize(); i++ {
		st := result.GetStructureAt(i)
		st.RemoveValue("profile-level-id")
	}
	if filter != nil {
		result = result.Intersect(filter)
	}
	return result.Ref()
}

func (e *plidPatch) resolvePlid(self *base.GstBaseTransform) {
	downstream := self.SrcPad().PeerQueryCaps(nil)
	if downstream == nil || downstream.IsEmpty() || downstream.IsAny() || downstream.GetSize() == 0 {
		return
	}
	st := downstream.GetStructureAt(0)
	plid, err := st.GetString("profile-level-id")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("resolvePlid: downstream caps missing profile-level-id: %v", err))
		return
	}

	parsed, err := parseProfileLevelID(plid)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("resolvePlid: failed to parse profile-level-id %q: %v", plid, err))
		return
	}

	var maxFs, maxMbps int

	maxFsStr, err := st.GetString("max-fs")
	if err == nil {
		maxFs, err = strconv.Atoi(maxFsStr)
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid max-fs value in downstream caps: %v", err))
			maxFs = 0
		}
	}

	maxMbpsStr, err := st.GetString("max-mbps")
	if err == nil {
		maxMbps, err = strconv.Atoi(maxMbpsStr)
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid max-mbps value in downstream caps: %v", err))
			maxMbps = 0
		}
	}

	patched := patchProfileLevelID(parsed, maxFs, maxMbps)
	e.plid = patched

	w, h, ok := maxResolutionForLevel(patched, 24)
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown profile-level-id %q; cannot determine max resolution", patched))
		w = -1
		h = -1
	}

	if !e.plidResolved.CompareAndSwap(false, true) {
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Profile-level-id resolved: %s level %d (max resolution: %dx%d)", patched, patched.levelIDC, w, h))

	if _, err := self.Element.Emit("plid-resolved", patched.String(), w, h); err != nil {
		self.Log(CAT, gst.LevelError, "failed to emit plid-resolved: "+err.Error())
	}
}

// SetCaps stamps downstream's profile-level-id onto outcaps so the CAPS
// event flowing out survives downstream's intersect check. This is a
// blind rewrite — the H.264-domain profile_capsfilter upstream is what
// actually keeps the bitstream honest.
func (e *plidPatch) SetCaps(self *base.GstBaseTransform, incaps, outcaps *gst.Caps) bool {
	downstream := self.SrcPad().PeerQueryCaps(nil)
	if downstream == nil || downstream.IsEmpty() || downstream.IsAny() || downstream.GetSize() == 0 {
		return true
	}

	plid := getProfileLevelID(downstream.GetStructureAt(0))
	if plid == "" {
		return true
	}

	if outcaps.GetSize() > 0 {
		st := outcaps.GetStructureAt(0)
		_ = st.SetValue("profile-level-id", plid)
	}
	return true
}
