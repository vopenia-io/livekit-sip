package h264rtppaybin

import (
	"fmt"
	"sync/atomic"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"h264rtppaybin",
	gst.DebugColorFgCyan,
	"H264 to RTP payloader bin with profile-level-id aware negotiation",
)

const (
	sinkCapsStr = "video/x-h264"
	srcCapsStr  = "application/x-rtp, media=(string)video, encoding-name=(string)H264"
)

// H264RtpPayBin wraps h264parse → profile_capsfilter → rtph264pay →
// h264rtpplidpatch. The plid patcher observes the downstream RTP caps
// during negotiation and emits a `plid-resolved` signal with the
// profile-level-id. The bin listens to that signal and programs
// profile_capsfilter with the matching `video/x-h264, profile=..., level=...`
// so the encoder upstream is caps-constrained to produce the bitstream
// the SDP advertises — independent of which H.264 encoder is used.
//
// Doing this off the plid patcher rather than in ChangeState is essential
// for factorybin-hosted pipelines: factorybin's SyncStateWithParent
// transitions the new element to its target state BEFORE setting the
// ghost pad targets, so a state-change peer-query would see ANY caps.
// plidPatch.TransformCaps on the other hand fires during caps
// negotiation, which runs only once the chain is actually wired.
//
// The bin also re-emits max-resolution(int, int) derived from the same
// plid, used by videoh264 to clamp its raw-video ScaleFilter.
type H264RtpPayBin struct {
	H264Parse         *gst.Element
	ProfileCapsFilter *gst.Element
	RtpH264Pay        *gst.Element
	PlidPatch         *gst.Element

	profileApplied atomic.Bool
}

func (e *H264RtpPayBin) New() glib.GoObjectSubclass {
	return &H264RtpPayBin{}
}

func (e *H264RtpPayBin) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"H264 RTP Payloader Bin",
		"Codec/Payloader/Network/RTP",
		"H264 to RTP packetizer with profile-level-id aware caps negotiation",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString(sinkCapsStr),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString(srcCapsStr),
	))

	gst.SignalNew(
		class.Type(),
		"max-resolution",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_INT, glib.TYPE_INT,
	)

	gst.SignalNew(
		class.Type(),
		"caps-changed",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_STRING,
	)
}

func (e *H264RtpPayBin) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.H264Parse, err = gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(-1),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264parse: %v", err))
		self.Error("Failed to create h264parse", err)
		return
	}

	e.ProfileCapsFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create profile capsfilter: %v", err))
		self.Error("Failed to create profile capsfilter", err)
		return
	}

	e.RtpH264Pay, err = gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"mtu":             int(1200),
		"config-interval": int(-1),
		"aggregate-mode":  int(1),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtph264pay: %v", err))
		self.Error("Failed to create rtph264pay", err)
		return
	}

	e.PlidPatch, err = gst.NewElementWithProperties("h264rtpplidpatch", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264rtpplidpatch: %v", err))
		self.Error("Failed to create h264rtpplidpatch", err)
		return
	}

	wself := glib.WeakRefInit(self)
	wProfileCapsFilter := glib.WeakRefInit(e.ProfileCapsFilter)
	if _, err := e.PlidPatch.Connect("plid-resolved", func(_ *gst.Element, plid string, maxWidth, maxHeight int) {
		self := gst.ToGstBin(wself.Get())
		profileCapsFilter := gst.ToElement(wProfileCapsFilter.Get())
		if self == nil || profileCapsFilter == nil {
			return
		}
		onPlidResolved(self, profileCapsFilter, plid, maxWidth, maxHeight)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect plid-resolved: %v", err))
		self.Error("Failed to connect plid-resolved", err)
		return
	}

	if err := self.AddMany(e.H264Parse, e.ProfileCapsFilter, e.RtpH264Pay, e.PlidPatch); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(e.H264Parse, e.ProfileCapsFilter, e.RtpH264Pay, e.PlidPatch); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.H264Parse.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.PlidPatch.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func onPlidResolved(self *gst.Bin, profileCapsFilter *gst.Element, plid string, maxWidth, maxHeight int) {
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Profile-level-id resolved: %s (max resolution: %dx%d)", plid, maxWidth, maxHeight))

	parsed, err := parseProfileLevelID(plid)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse profile-level-id %q: %v", plid, err))
		return
	}

	caps := gst.NewCapsFromString(h264CapsStringForPLID(parsed))
	if err := profileCapsFilter.SetProperty("caps", caps); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set caps on profile capsfilter: %v", err))
	}

	if _, err := self.Emit("max-resolution", maxWidth, maxHeight); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit max-resolution: %v", err))
	}
}

func (e *H264RtpPayBin) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing H264RtpPayBin")

	e.H264Parse = nil
	e.ProfileCapsFilter = nil
	e.RtpH264Pay = nil
	e.PlidPatch = nil
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
