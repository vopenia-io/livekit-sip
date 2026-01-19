package activeselector

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"active-selector",
	gst.DebugColorFgMagenta,
	"active-selector Element",
)

type ActiveSelector struct {
	InputSelector *gst.Element
}

func (*ActiveSelector) New() glib.GoObjectSubclass {
	return &ActiveSelector{}
}

func (*ActiveSelector) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"active-selector",
		"src/sink",
		"A smart input selector for active source selection",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewAnyCaps()))
}

func (s *ActiveSelector) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	class := gst.ToElementClass(self.Class())

	var err error

	s.InputSelector, err = gst.NewElement("input-selector")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating input-selector: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error creating input-selector", err.Error())
		return
	}

	if err := self.Add(s.InputSelector); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding input-selector: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error adding input-selector", err.Error())
		return
	}

	gsrcPad := gst.NewGhostPadFromTemplate("src", s.InputSelector.GetStaticPad("src"), class.GetPadTemplate("src"))
	self.AddPad(gsrcPad.Pad)
}

func (s *ActiveSelector) HandleTrackSelect(self *gst.Bin, pad *gst.Pad, curPad *gst.Pad, event *gst.Event) bool {
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("HandleTrackSelect called for pad %s", pad.GetName()))

	var trackEvent ActiveTrackEvent
	if err := trackEvent.Unmarshal(event.GetStructure()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to unmarshal active-track event: %v", err))
		return true
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Setting active pad to %s", pad.GetName()))
	if err := s.InputSelector.SetProperty("active-pad", pad); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set active pad: %v", err))
	}

	peer := pad.GetPeer()
	if peer == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Pad %s has no peer, cannot request keyframe", pad.GetName()))
		return true
	}

	if err := s.RequestTrackKeyframe(self, peer, trackEvent.TrackSSRC); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to request track keyframe: %v", err))
	}

	return false
}

func (s *ActiveSelector) RequestTrackKeyframe(self *gst.Bin, pad *gst.Pad, ssrc uint32) error {
	// self.Log(CAT, gst.LevelInfo, fmt.Sprintf("RequestTrackKeyframe called for ssrc %d", ssrc))

	// fkuStruct := gst.NewStructure("GstForceKeyUnit")
	// runtime.SetFinalizer(fkuStruct, nil)
	// fkuStruct.SetValue("ssrc", ssrc)
	// fkuStruct.SetValue("running-time", gst.ClockTimeNone)
	// fkuStruct.SetValue("all-headers", false)
	// fkuStruct.SetValue("count", uint(0))

	// fkuEvent := gst.NewCustomEvent(gst.EventTypeCustomUpstream, fkuStruct)

	// if !pad.SendEvent(fkuEvent.Ref()) {
	// 	self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to send GstForceKeyUnit event upstream for ssrc %d", ssrc))
	// 	return fmt.Errorf("failed to send GstForceKeyUnit event upstream for ssrc %d", ssrc)
	// }
	// self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Sent GstForceKeyUnit event upstream for ssrc %d", ssrc))

	return nil
}

func (s *ActiveSelector) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, _ string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	pad := s.InputSelector.GetRequestPad("sink_%u")
	if pad == nil {
		self.Log(CAT, gst.LevelError, "Failed to get request pad from input-selector")
		return nil
	}

	s.InputSelector.SetProperty("active-pad", pad)

	class := gst.ToElementClass(self.Class())

	gsink := gst.NewGhostPadFromTemplate(pad.GetName(), pad, class.GetPadTemplate("sink_%u"))

	gsink.SetEventFunction(func(curPad *gst.Pad, parent *gst.Object, event *gst.Event) bool {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Pad %s received event: %s", curPad.GetName(), event.Type().String()))
		if event.Type() == gst.EventTypeCustomDownstream && event.HasName(ACTIVE_TRACK_EVENT_NAME) {
			if !s.HandleTrackSelect(self, pad, curPad, event) {
				return true
			}
		}
		return pad.SendEvent(event.Ref())
	})

	self.AddPad(gsink.Pad)

	return gsink.Pad
}

func (s *ActiveSelector) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)
	name := pad.GetName()

	gpad := pad.AsGhostPad()
	if gpad == nil {
		return
	}

	if !self.RemovePad(pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("ReleasePad: Failed to remove pad %s from bin", name))
	}
}
