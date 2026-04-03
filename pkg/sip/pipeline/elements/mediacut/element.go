package mediacut

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"mediacutout",
	gst.DebugColorNone,
	"mediacutout Element",
)

type MediaCut struct {
	Valve *gst.Element
	Src   *gst.GhostPad
}

func (e *MediaCut) New() glib.GoObjectSubclass {
	return &MediaCut{}
}

func (e *MediaCut) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Media Cut",
		"Generic",
		"Cut out media streams while not playing",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewAnyCaps(),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewAnyCaps(),
	))
}

func (e *MediaCut) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.Valve, err = gst.NewElementWithProperties("valve", map[string]interface{}{
		"drop": true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create valve element: %v", err))
		self.Error("Failed to create valve element", err)
		return
	}

	if err := self.AddMany(
		e.Valve,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add valve element: %v", err))
		self.Error("Failed to add valve element", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.Valve.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadNoTargetFromTemplate("src", elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
	e.Src = ghostSrc
}

func (e *MediaCut) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	if transition == gst.StateChangePausedToPlaying {
		if !e.Src.SetTarget(e.Valve.GetStaticPad("src")) {
			self.Log(CAT, gst.LevelError, "Failed to set target of ghost pad to valve src pad")
			self.Error("Failed to set target of ghost pad to valve src pad", fmt.Errorf("failed to set ghost pad target"))
		} else {
			if err := e.Valve.SetProperty("drop", false); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set valve drop property to false: %v", err))
				self.Error("Failed to set valve drop property to false", err)
			} else {
				self.Log(CAT, gst.LevelInfo, "MediaCutout valve opened")
			}
		}
	}

	if transition == gst.StateChangePlayingToPaused {
		if err := e.Valve.SetProperty("drop", true); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set valve drop property to true: %v", err))
			self.Error("Failed to set valve drop property to true", err)
		} else {
			if !e.Src.SetTarget(nil) {
				self.Log(CAT, gst.LevelError, "Failed to set target of ghost pad to nil")
				self.Error("Failed to set target of ghost pad to nil", fmt.Errorf("failed to set ghost pad target"))
			} else {
				self.Log(CAT, gst.LevelInfo, "MediaCutout valve closed")
			}
		}
	}

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.Valve = nil
		e.Src = nil
	}
	return ret
}
