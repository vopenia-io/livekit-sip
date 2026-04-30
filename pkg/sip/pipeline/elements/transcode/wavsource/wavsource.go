package wavsource

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"wavsource",
	gst.DebugColorNone,
	"wavsource Element",
)

var properties = []*glib.ParamSpec{
	glib.NewIntParam(
		"fd",
		"FD",
		"File descriptor to read the WAV bytes from. The bin closes this fd in Finalize.",
		-1,
		0x7FFFFFFF,
		-1,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

type WavSource struct {
	fd int

	FdSrc         *gst.Element
	Queue         *gst.Element
	WavParse      *gst.Element
	ClockSync     *gst.Element
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	AudioRate     *gst.Element
}

func (e *WavSource) New() glib.GoObjectSubclass {
	return &WavSource{fd: -1}
}

func (e *WavSource) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"WAV Source",
		"Audio/Source",
		"Reads WAV bytes from a file descriptor and produces raw audio",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("audio/x-raw"),
	))

	class.InstallProperties(properties)
}

func (e *WavSource) InstanceInit(instance *glib.Object) {
	e.fd = -1
}

func (e *WavSource) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	if e.fd < 0 {
		self.Log(CAT, gst.LevelError, "Invalid fd property: must be set to a non-negative integer")
		self.Error("Invalid fd property: must be set to a non-negative integer", nil)
		return
	}

	e.FdSrc, err = gst.NewElementWithProperties("fdsrc", map[string]interface{}{
		"fd":           e.fd,
		"is-live":      true,
		"do-timestamp": true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create fdsrc element: %v", err))
		self.Error("Failed to create fdsrc element", err)
		return
	}

	e.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element: %v", err))
		self.Error("Failed to create queue element", err)
		return
	}

	e.WavParse, err = gst.NewElement("wavparse")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create wavparse element: %v", err))
		self.Error("Failed to create wavparse element", err)
		return
	}

	e.ClockSync, err = gst.NewElementWithProperties("clocksync", map[string]interface{}{
		"sync-to-first": true,
	})

	e.ClockSync.GetStaticPad("src").AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		evt := info.GetEvent()
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("wavsource fdsrc pad probe got event of type %s in state %s", evt.Type().String(), self.GetCurrentState().String()))
		return gst.PadProbeOK
	})
	e.ClockSync.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("wavsource fdsrc pad probe got buffer with PTS %d and size %d in state %s", buf.PresentationTimestamp(), buf.GetSize(), self.GetCurrentState().String()))
		return gst.PadProbeOK
	})

	e.AudioConvert, err = gst.NewElement("audioconvert")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioconvert element: %v", err))
		self.Error("Failed to create audioconvert element", err)
		return
	}

	e.AudioResample, err = gst.NewElement("audioresample")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioresample element: %v", err))
		self.Error("Failed to create audioresample element", err)
		return
	}

	e.AudioRate, err = gst.NewElement("audiorate")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audiorate element: %v", err))
		self.Error("Failed to create audiorate element", err)
		return
	}

	if err := self.AddMany(
		e.FdSrc,
		e.Queue,
		e.WavParse,
		e.ClockSync,
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.FdSrc,
		e.Queue,
		e.WavParse,
		e.ClockSync,
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())
	ghostSrc := gst.NewGhostPadFromTemplate("src", e.AudioRate.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *WavSource) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "fd":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting fd property value: %v", err))
			return
		}
		val, ok := gv.(int)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for fd property")
			return
		}
		e.fd = val
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property %s", param.Name()))
	}
}

func (e *WavSource) GetProperty(instance *glib.Object, id uint) *glib.Value {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "fd":
		value, err := glib.GValue(e.fd)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting fd property value: %v", err))
			return nil
		}
		return value
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property %s", param.Name()))
		return nil
	}
}

func (e *WavSource) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing WavSource element")

	// if e.fd >= 0 {
	// 	if err := unix.Close(e.fd); err != nil {
	// 		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to close fd %d: %v", e.fd, err))
	// 	}
	// 	e.fd = -1
	// }

	e.FdSrc = nil
	e.Queue = nil
	e.WavParse = nil
	e.ClockSync = nil
	e.AudioConvert = nil
	e.AudioResample = nil
	e.AudioRate = nil
}
