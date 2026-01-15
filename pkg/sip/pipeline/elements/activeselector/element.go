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

func (s *ActiveSelector) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, _ string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	pad := s.InputSelector.GetRequestPad("sink_%u")
	if pad == nil {
		self.Log(CAT, gst.LevelError, "Failed to get request pad from input-selector")
		return nil
	}

	class := gst.ToElementClass(self.Class())

	gsink := gst.NewGhostPadFromTemplate(pad.GetName(), pad, class.GetPadTemplate("sink_%u"))

	gsink.SetEventFunction(func(self *gst.Pad, parent *gst.Object, event *gst.Event) bool {
		fmt.Printf("Pad %s received event: %s\n", pad.GetName(), event.Type().String())
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Pad %s received event: %s", pad.GetName(), event.Type().String()))
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
