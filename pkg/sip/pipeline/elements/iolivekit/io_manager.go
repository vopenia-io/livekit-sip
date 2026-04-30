package iolivekit

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"io_manager_livekit",
	gst.DebugColorNone,
	"livekit SIP pipeline LiveKit IO element",
)

var properties = []*glib.ParamSpec{
	glib.NewUintParam(
		"video-width",
		"Video Width",
		"The width of the video frames",
		1,
		8192,
		1280,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewUintParam(
		"video-height",
		"Video Height",
		"The height of the video frames",
		1,
		8192,
		720,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoolParam(
		"nvidia",
		"NVIDIA Hardware Acceleration",
		"Whether to use NVIDIA hardware acceleration for video processing (crash if enabled but not available)",
		false,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoolParam(
		"microphone",
		"Microphone",
		"Whether to subscribe to microphone tracks",
		false,
		glib.ParameterWritable,
	),
	glib.NewBoolParam(
		"camera",
		"Camera",
		"Whether to subscribe to camera tracks",
		false,
		glib.ParameterWritable,
	),
	glib.NewBoolParam(
		"screenshare",
		"Screen Share",
		"Whether to subscribe to screenshare tracks",
		false,
		glib.ParameterWritable,
	),
	glib.NewBoolParam(
		"screenshare-audio",
		"Screen Share Audio",
		"Whether to subscribe to screenshare audio tracks",
		false,
		glib.ParameterWritable,
	),
}

func (e *IoManagerLivekit) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "video-width":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting video-width property value: %v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for video-width property")
			return
		}
		if val > 0xFFFF {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for video-width property: %d", val))
			return
		}
		e.videoWidth = val
	case "video-height":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting video-height property value: %v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for video-height property")
			return
		}
		if val > 0xFFFF {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for video-height property: %d", val))
			return
		}
		e.videoHeight = val
	case "nvidia":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting nvidia property value: %v", err))
			return
		}
		val, ok := gv.(bool)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for nvidia property")
			return
		}
		e.nvidia = val
	case "microphone":
		self.Log(CAT, gst.LevelDebug, "Setting microphone property")
		if err := e.Fallback.SetPropertyValue("microphone", value); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting microphone property on fallback element: %v", err))
		}
		self.Log(CAT, gst.LevelDebug, "Finished setting microphone property")
	case "camera":
		self.Log(CAT, gst.LevelDebug, "Setting camera property")
		if err := e.Fallback.SetPropertyValue("camera", value); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting camera property on fallback element: %v", err))
		}
		self.Log(CAT, gst.LevelDebug, "Finished setting camera property")
	case "screenshare":
		self.Log(CAT, gst.LevelDebug, "Setting screenshare property")
		if err := e.Fallback.SetPropertyValue("screenshare", value); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting screenshare property on fallback element: %v", err))
		}
		self.Log(CAT, gst.LevelDebug, "Finished setting screenshare property")
	case "screenshare-audio":
		self.Log(CAT, gst.LevelDebug, "Setting screenshare-audio property")
		if err := e.Fallback.SetPropertyValue("screenshare-audio", value); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting screenshare-audio property on fallback element: %v", err))
		}
		self.Log(CAT, gst.LevelDebug, "Finished setting screenshare-audio property")
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property ID %d", id))
	}
}
