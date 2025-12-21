package sinkwriter

import (
	"fmt"
	"io"
	"math"
	"runtime/cgo"
	"sync/atomic"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

var CAT = gst.NewDebugCategory(
	"sinkwriter",
	gst.DebugColorFgYellow,
	"sinkwriter Element",
)

var properties = []*glib.ParamSpec{
	glib.NewUint64Param(
		"handle",
		"Handle",
		"cgo.Handle (uintptr) to a Go object",
		0,
		math.MaxUint64,
		0,
		glib.ParameterWritable,
	),
	glib.NewBoxedParam(
		"caps",
		"Caps",
		"The caps of the source stream",
		gst.TypeCaps,
		glib.ParameterReadWrite,
	),
	glib.NewBoolParam(
		"has-handle",
		"has-handle",
		"check if a handle has been set",
		false,
		glib.ParameterReadable,
	),
}

// Here we declare a private struct to hold our internal state.
type state struct {
	writer io.WriteCloser
	closed atomic.Bool
}

// This is another private struct where we hold the parameter values set on our
// element.
type settings struct {
	caps *gst.Caps
}

type sinkWriter struct {
	self *base.GstBaseSink
	// The settings for the element
	settings *settings
	// The current state of the element
	state *state
}

func (*sinkWriter) New() glib.GoObjectSubclass {
	s := &sinkWriter{}
	return s
}

func (*sinkWriter) ClassInit(klass *glib.ObjectClass) {
	CAT.Log(gst.LevelDebug, "Initializing class")
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Custom Writer Sink",
		"Sink",
		"Writes to a Go io.Writer",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	// Src pad template: ANY caps because we don't know what the reader contains
	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewAnyCaps(),
	))

	class.InstallProperties(properties)
}

func (s *sinkWriter) Constructed(self *glib.Object) {
	CAT.Log(gst.LevelDebug, "Constructing")

	s.self = base.ToGstBaseSink(self)

	s.settings = &settings{
		caps: gst.NewAnyCaps(),
	}
	s.state = &state{
		writer: nil,
	}

	s.self.SetSync(false)
}

func (s *sinkWriter) SetProperty(self *glib.Object, id uint, value *glib.Value) {
	param := properties[id]
	switch param.Name() {
	case "handle":
		gv, _ := value.GoValue()
		val, _ := gv.(uint64)
		h := cgo.Handle(uintptr(val))
		if h > 0 {
			obj := h.Value()
			if w, ok := obj.(io.WriteCloser); ok {
				s.state.writer = w
			}
		}
	case "caps":
		val, err := value.GoValue()
		if err != nil {
			CAT.Log(gst.LevelError, fmt.Sprintf("Error getting caps property value: %v", err))
			return
		}
		caps, ok := val.(*gst.Caps)
		if !ok {
			CAT.Log(gst.LevelError, "Invalid type for caps property")
			return
		}
		if caps == nil {
			CAT.Log(gst.LevelError, "Nil caps provided")
			return
		}
		s.settings.caps = caps.Copy()
		CAT.Log(gst.LevelInfo, fmt.Sprintf("Element caps set to: %v", caps))
		if s.self != nil {
			s.self.GetStaticPad("sink").MarkReconfigure()
		}
	}
}

func (s *sinkWriter) GetProperty(self *glib.Object, id uint) *glib.Value {
	param := properties[id]
	switch param.Name() {
	case "caps":
		if s.settings.caps != nil {
			v, _ := glib.GValue(s.settings.caps)
			return v
		}
		v, _ := glib.GValue(gst.NewAnyCaps())
		return v
	case "has-handle":
		hasHandle := s.state.writer != nil
		v, _ := glib.GValue(hasHandle)
		return v
	}
	return nil
}

func (s *sinkWriter) SetCaps(self *base.GstBaseSink, caps *gst.Caps) bool {
	CAT.Log(gst.LevelDebug, fmt.Sprintf("caps not set to: %s, keepping existing: %s", caps.String(), s.settings.caps.String()))
	return true
}

func (s *sinkWriter) GetCaps(self *base.GstBaseSink, filter *gst.Caps) *gst.Caps {
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		CAT.Log(gst.LevelDebug, fmt.Sprintf("caps get filter: %s", filter.String()))
		if intersect := s.settings.caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	CAT.Log(gst.LevelDebug, fmt.Sprintf("caps get: %s", s.settings.caps.String()))
	return s.settings.caps.Ref()
}

func (s *sinkWriter) Start(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelInfo, "started")
	return true
}

func (s *sinkWriter) Stop(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelInfo, "stopped")
	if err := self.SetState(gst.StateNull); err != nil {
		CAT.Log(gst.LevelError, fmt.Sprintf("Failed to set state to NULL: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Failed to set state to NULL", err.Error())
		return false
	}
	if s.state.closed.Swap(true) {
		if s.state.writer != nil {
			s.state.writer.Close()
		}
		s.state.writer = nil
	}
	return true
}

func (s *sinkWriter) Render(self *base.GstBaseSink, buffer *gst.Buffer) gst.FlowReturn {
	CAT.Log(gst.LevelTrace, fmt.Sprintf("Render called: buffer size=%d", buffer.GetSize()))
	if s.state.closed.Load() {
		CAT.Log(gst.LevelError, "io.Writer is already closed")
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorWrite, "io.Writer is already closed", "")
		return gst.FlowEOS
	}

	if s.state.writer == nil {
		CAT.Log(gst.LevelError, "io.Writer is not set")
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "io.Writer is not set", "")
		return gst.FlowError
	}
	n, err := s.state.writer.Write(buffer.Bytes())
	if err != nil {
		if err == io.EOF {
			CAT.Log(gst.LevelInfo, "reached EOF on write")
			return gst.FlowEOS
		}
		CAT.Log(gst.LevelError, fmt.Sprintf("Error writing to io.Writer: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorWrite, "Error writing to io.Writer", err.Error())
		return gst.FlowError
	}
	if n < int(buffer.GetSize()) {
		CAT.Log(gst.LevelWarning, fmt.Sprintf("Partial write to io.Writer: wrote %d of %d bytes", n, buffer.GetSize()))
	}

	CAT.Log(gst.LevelTrace, fmt.Sprintf("wrote %d bytes to io.Writer", n))
	return gst.FlowOK
}

func (s *sinkWriter) Unlock(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelInfo, "unlocked")
	if s.state.closed.Swap(true) {
		CAT.Log(gst.LevelDebug, "io.Writer already closed")
		return true
	}
	if err := s.state.writer.Close(); err != nil {
		CAT.Log(gst.LevelError, fmt.Sprintf("Error closing io.Writer: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorWrite, "Error closing io.Writer", err.Error())
		return true // TODO: should return false here but true until we handle errors
	}
	return true
}
