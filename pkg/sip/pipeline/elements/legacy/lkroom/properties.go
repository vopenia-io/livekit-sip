package lkroom

import (
	"fmt"
	"math"
	"runtime/cgo"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

var properties = []*glib.ParamSpec{
	glib.NewUint64Param(
		"callbacks",
		"Callbacks Handle",
		"Handle for the LiveKit room callbacks",
		0,
		math.MaxUint64,
		0,
		glib.ParameterWritable,
	),
	glib.NewUint64Param(
		"room",
		"Room Handle",
		"Handle for the LiveKit room",
		0,
		math.MaxUint64,
		0,
		glib.ParameterReadable,
	),
	glib.NewBoolParam(
		"auto-join",
		"Auto Join",
		"Automatically join the room on state change to PLAYING (default: true, must emit a 'room-joined' signal after joining if false)",
		true,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"token",
		"Token",
		"LiveKit access token",
		nil,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"ws-url",
		"WebSocket URL",
		"LiveKit WebSocket URL",
		nil,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewUint64Param(
		"connect-options",
		"Options Handle",
		"Cgo Handle for the LiveKit room options",
		0,
		math.MaxUint64,
		0,
		glib.ParameterWritable,
	),
}

func (s *lkroom) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "callbacks":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting callbacks property value: %v", err))
			return
		}
		val, ok := gv.(uint64)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for callbacks property")
			return
		}
		h := cgo.Handle(uintptr(val))
		if h == 0 {
			self.Log(CAT, gst.LevelError, "Invalid handle provided for callbacks")
			return
		}
		obj := h.Value()
		cb, ok := obj.(*lksdk.RoomCallback)
		if !ok {
			self.Log(CAT, gst.LevelError, "Handle does not contain a RoomCallback")
			return
		}
		s.callbacks.Merge(cb)
	case "auto-join":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting auto-join property value: %v", err))
			return
		}
		s.AutoJoin = gv.(bool)
	case "token":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting token property value: %v", err))
			return
		}
		s.Token = gv.(string)
	case "ws-url":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting ws-url property value: %v", err))
			return
		}
		s.WsURL = gv.(string)
	case "connect-options":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting connect-options property value: %v", err))
			return
		}
		val, ok := gv.(uint64)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for connect-options property")
			return
		}
		h := cgo.Handle(uintptr(val))
		if h == 0 {
			self.Log(CAT, gst.LevelError, "Invalid handle provided for connect-options")
			return
		}
		obj := h.Value()
		opt, ok := obj.([]lksdk.ConnectOption)
		if !ok {
			self.Log(CAT, gst.LevelError, "Handle does not contain a ConnectOption")
			return
		}
		s.Opt = opt
	}
}

func (s *lkroom) GetProperty(instance *glib.Object, id uint) *glib.Value {
	param := properties[id]
	switch param.Name() {
	case "room":
		h := cgo.NewHandle(s.room)
		val := uint64(uintptr(h))
		gv, _ := glib.GValue(val)
		return gv
	case "auto-join":
		gv, _ := glib.GValue(s.AutoJoin)
		return gv
	case "token":
		gv, _ := glib.GValue(s.Token)
		return gv
	case "ws-url":
		gv, _ := glib.GValue(s.WsURL)
		return gv
	}
	return nil
}
