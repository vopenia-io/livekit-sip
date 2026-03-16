package livekitcompositor

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var properties = []*glib.ParamSpec{
	glib.NewBoxedParam(
		"current-layout",
		"Current Layout",
		"The currently active layout",
		glib.TYPE_STRV,
		glib.ParameterReadable,
	),
}

func (e *LivekitCompositor) GetProperty(instance *glib.Object, id uint) *glib.Value {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "current-layout":
		e.mu.Lock()
		defer e.mu.Unlock()
		value, err := glib.GValue(glib.NewStrv(e.currentLayout))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting current-layout property value: %v", err))
			return nil
		}
		return value
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property %s", param.Name()))
		return nil
	}
}
