package vp8h264select

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

const SelectEventName = "vp8_h264_select-event-switch"
const QDataPadSwitching = "vp8_h264_select-pad-switching"

func (e *Vp8H264Select) OnSelectorEvent(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	event := info.GetEvent()
	if event == nil {
		return gst.PadProbeOK
	}
	if event.Type() != gst.EventTypeCustomDownstream || !event.HasName(SelectEventName) {
		return gst.PadProbeOK
	}

	self := gst.ToGstBin(e.InputSelector.GetParent())
	if self == nil {
		CAT.Log(gst.LevelError, "Failed to get parent bin")
		return gst.PadProbeOK
	}

	activeVal, err := pad.GetProperty("active")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get active property of pad %s: %v", pad.GetName(), err))
		return gst.PadProbeOK
	}
	active, ok := activeVal.(bool)
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Active property of pad %s is not a bool", pad.GetName()))
		return gst.PadProbeOK
	}
	if active {
		self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Pad %s is already active, skipping selector event", pad.GetName()))
		return gst.PadProbeOK
	}

	// isSwitching, ok := pad.GetQData(QDataPadSwitching).(bool)
	// if !ok {
	// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get pad switching state for pad %s", pad.GetName()))
	// 	return gst.PadProbeOK
	// }
	// if isSwitching {
	// 	self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Pad %s is already switching, skipping selector event", pad.GetName()))
	// 	return gst.PadProbeOK
	// }

	// pad.SetQData(QDataPadSwitching, true)
	// pad.AddProbe(gst.PadProbeTypeBuffer, PadProbeWaitForKeyUnit)

	if err := e.InputSelector.SetProperty("active-pad", pad); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set active pad: %v", err))
		return gst.PadProbeOK
	}

	// evtPad := e.InputSelector.GetStaticPad("src")
	// if !evtPad.SendEvent(video.NewEventUpstreamForceKeyUnit(gst.ClockTimeNone, true, 0)) {
	// 	self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to push ForceKeyUnit event on pad %s for sink %s", evtPad.GetName(), pad.GetName()))
	// 	return gst.PadProbeOK
	// }
	// self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Pushed ForceKeyUnit event on pad %s for sink %s", evtPad.GetName(), pad.GetName()))

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Switched active pad to %s", pad.GetName()))

	return gst.PadProbeOK
}

// func PadProbeWaitForKeyUnit(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
// 	buffer := info.GetBuffer()
// 	if buffer == nil {
// 		return gst.PadProbeDrop
// 	}
// 	if buffer.HasFlags(gst.BufferFlagDeltaUnit) {
// 		return gst.PadProbeDrop
// 	}
// 	CAT.Log(gst.LevelDebug, fmt.Sprintf("Received key unit buffer on pad %s, activating pad", pad.GetName()))
// 	pad.SetQData(QDataPadSwitching, false)
// 	return gst.PadProbeRemove
// }
