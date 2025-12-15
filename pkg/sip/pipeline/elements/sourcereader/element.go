package sourcereader

import (
	"fmt"
	"io"
	"math"
	"runtime/cgo"

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
}

// Here we declare a private struct to hold our internal state.
type state struct {
	reader io.Reader
}

// This is another private struct where we hold the parameter values set on our
// element.
type settings struct {
	caps *gst.Caps
}

type sourceReader struct {
	// The settings for the element
	settings *settings
	// The current state of the element
	state *state
}

func (*sourceReader) New() glib.GoObjectSubclass {
	return &sourceReader{}
}

func (*sourceReader) ClassInit(klass *glib.ObjectClass) {
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

func (s *sourceReader) SetProperty(self *glib.Object, id uint, value *glib.Value) {
	param := properties[id]
	switch param.Name() {
	case "handle":
		gv, _ := value.GoValue()
		val, _ := gv.(uint64)
		h := cgo.Handle(uintptr(val))
		if h > 0 {
			obj := h.Value()
			if r, ok := obj.(io.Reader); ok {
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
	}
}

func (s *sourceReader) GetProperty(self *glib.Object, id uint) *glib.Value {
	param := properties[id]
	switch param.Name() {
	case "caps":
		if s.settings.caps != nil {
			v, _ := glib.GValue(s.settings.caps.String())
			return v
		}
		v, _ := glib.GValue(gst.NewAnyCaps().String())
		return v
	}
	return nil
}

func (s *sourceReader) Constructed(self *glib.Object) {
	CAT.Log(gst.LevelDebug, "Constructing")

	s.settings = &settings{
		caps: gst.NewAnyCaps(),
	}
	s.state = &state{
		reader: nil,
	}

	baseSrc := base.ToGstBaseSrc(self)
	baseSrc.SetFormat(gst.FormatTime)
	baseSrc.SetLive(true)
}

func (s *sourceReader) SetCaps(self *base.GstBaseSrc, caps *gst.Caps) bool {
	CAT.Log(gst.LevelDebug, fmt.Sprintf("caps not set to: %s, keepping existing: %s", caps.String(), s.settings.caps.String()))
	return true
}

func (s *sourceReader) GetCaps(self *base.GstBaseSrc, filter *gst.Caps) *gst.Caps {
	caps := s.settings.caps
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		CAT.Log(gst.LevelDebug, fmt.Sprintf("caps get filter: %s", filter.String()))
		caps = s.settings.caps.Intersect(filter)
	}
	CAT.Log(gst.LevelDebug, fmt.Sprintf("caps get: %s", caps.String()))
	return caps.Ref()
}

func (s *sourceReader) Start(self *base.GstBaseSrc) bool {
	if s.state.reader == nil {
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "io.Reader is not set", "")
		return false
	}

	// self.StartComplete(gst.FlowOK)

	self.Log(CAT, gst.LevelInfo, "started")
	return true
}

func (s *sourceReader) Stop(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelInfo, "stopped")
	return true
}

func (s *sourceReader) Fill(self *base.GstBaseSrc, offset uint64, length uint, buffer *gst.Buffer) gst.FlowReturn {
	CAT.Log(gst.LevelTrace, fmt.Sprintf("Fill called: offset=%d, length=%d", offset, length))

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
	CAT.Log(gst.LevelDebug, fmt.Sprintf("filled buffer with %d bytes", n))

	return gst.FlowOK
}
