package sourcereader

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
	"sourcereader",
	gst.DebugColorFgYellow,
	"sourcereader Element",
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
	reader io.ReadCloser
	closed atomic.Bool
}

// This is another private struct where we hold the parameter values set on our
// element.
type settings struct {
	caps *gst.Caps
}

type SourceReader struct {
	self *base.GstBaseSrc
	// The settings for the element
	settings *settings
	// The current state of the element
	state *state
}

func (*SourceReader) New() glib.GoObjectSubclass {
	CAT.Log(gst.LevelInfo, "Creating new sourcereader instance")
	sr := &SourceReader{}
	// runtime.AddCleanup(sr, func(_ string) {
	// 	println("Cleaning up sourcereader")
	// }, "")
	return sr
}

func (*SourceReader) ClassInit(klass *glib.ObjectClass) {
	CAT.Log(gst.LevelDebug, "Initializing class")
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Custom Reader Source",
		"Source",
		"Reads from a Go io.Reader",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	// Src pad template: ANY caps because we don't know what the reader contains
	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewAnyCaps(),
	))

	class.InstallProperties(properties)
}

func (s *SourceReader) Constructed(self *glib.Object) {
	CAT.Log(gst.LevelDebug, "Constructing")

	s.self = base.ToGstBaseSrc(self)

	s.settings = &settings{
		caps: gst.NewAnyCaps(),
	}
	s.state = &state{
		reader: nil,
	}

	s.self.SetFormat(gst.FormatTime)
	s.self.SetLive(true)
}

func (s *SourceReader) SetProperty(self *glib.Object, id uint, value *glib.Value) {
	param := properties[id]
	switch param.Name() {
	case "handle":
		gv, _ := value.GoValue()
		val, _ := gv.(uint64)
		h := cgo.Handle(val)
		if h > 0 {
			obj := h.Value()
			if r, ok := obj.(io.ReadCloser); ok {
				s.state.reader = r
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
			s.self.GetStaticPad("src").MarkReconfigure()
		}
	}
}

func (s *SourceReader) GetProperty(self *glib.Object, id uint) *glib.Value {
	CAT.Log(gst.LevelInfo, "GetProperty called")
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
		hasHandle := s.state.reader != nil
		v, _ := glib.GValue(hasHandle)
		return v
	}
	return nil
}

func (s *SourceReader) SetCaps(self *base.GstBaseSrc, caps *gst.Caps) bool {
	CAT.Log(gst.LevelDebug, fmt.Sprintf("caps not set to: %s, keepping existing: %s", caps.String(), s.settings.caps.String()))
	return true
}

func (s *SourceReader) GetCaps(self *base.GstBaseSrc, filter *gst.Caps) *gst.Caps {
	caps := s.settings.caps
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		CAT.Log(gst.LevelDebug, fmt.Sprintf("caps get filter: %s", filter.String()))
		caps = s.settings.caps.Intersect(filter)
	}
	CAT.Log(gst.LevelDebug, fmt.Sprintf("caps get: %s", caps.String()))
	return caps.Ref()
}

func (s *SourceReader) Start(self *base.GstBaseSrc) bool {
	// if s.state.reader == nil {
	// 	self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "io.Reader is not set", "")
	// 	return false
	// }

	// self.StartComplete(gst.FlowOK)

	self.Log(CAT, gst.LevelInfo, "started")
	return true
}

func (s *SourceReader) Stop(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelInfo, "stopped")
	if err := self.SetState(gst.StateNull); err != nil {
		CAT.Log(gst.LevelError, fmt.Sprintf("Failed to set state to NULL: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Failed to set state to NULL", err.Error())
		return false
	}
	if s.state.closed.Swap(true) {
		if s.state.reader != nil {
			s.state.reader.Close()
		}
		s.state.reader = nil
	}
	// s.state.closed.Store(true)
	// s.state.reader = nil

	return true
}

func (s *SourceReader) Fill(self *base.GstBaseSrc, offset uint64, length uint, buffer *gst.Buffer) gst.FlowReturn {
	CAT.Log(gst.LevelTrace, fmt.Sprintf("Fill called: offset=%d, length=%d", offset, length))

	if s.state.closed.Load() {
		CAT.Log(gst.LevelError, "io.Reader is already closed")
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorRead, "io.Reader is already closed", "")
		return gst.FlowEOS
	}

	if s.state.reader == nil {
		CAT.Log(gst.LevelError, "io.Reader is not set")
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "io.Reader is not set", "")
		return gst.FlowError
	}

	data := make([]byte, length)
	n, err := s.state.reader.Read(data)
	if err != nil {
		if err == io.EOF {
			CAT.Log(gst.LevelInfo, "reached EOF")
			return gst.FlowEOS
		}
		CAT.Log(gst.LevelError, fmt.Sprintf("Error reading from io.Reader: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorRead, "Error reading from io.Reader", err.Error())
		return gst.FlowError
	}

	buffer.FillBytes(0, data[:n])
	if uint(n) < length {
		buffer.SetSize(int64(n))
	}
	CAT.Log(gst.LevelTrace, fmt.Sprintf("filled buffer with %d bytes", n))

	return gst.FlowOK
}

func (s *SourceReader) Unlock(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelInfo, "unlocked")

	if s.state.closed.Swap(true) {
		CAT.Log(gst.LevelDebug, "io.Reader already closed")
		return true
	}

	if err := s.state.reader.Close(); err != nil {
		CAT.Log(gst.LevelError, fmt.Sprintf("Error closing io.Reader: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorRead, "Error closing io.Reader", err.Error())
		return true // TODO: should return false here but true until we handle errors
	}
	return true
}
