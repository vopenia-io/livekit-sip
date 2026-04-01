package videoh264

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"video-h264",
	gst.DebugColorNone,
	"video-h264 Element",
)

// var properties = []*glib.ParamSpec{
// 	glib.NewUintParam(
// 		"pt",
// 		"H264 Payload Type",
// 		"The payload type of H264 RTP stream",
// 		0,
// 		127,
// 		96,
// 		glib.ParameterWritable|glib.ParameterReadable,
// 	),
// }

type VideoH264 struct {
	VideoScale           *gst.Element
	ScaleFilter          *gst.Element
	X264Enc              *gst.Element
	H264Parse            *gst.Element
	RtpH264Pay           *gst.Element
	RtpH264CapsIntersect *gst.Element
}

func (e *VideoH264) New() glib.GoObjectSubclass {
	return &VideoH264{}
}

func (e *VideoH264) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Video to H264 Encoder",
		"Video/Encoder",
		"Encodes raw video to H264 RTP",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("video/x-raw"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)H264"),
	))

	// class.InstallProperties(properties)
}

func (e *VideoH264) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoscale element: %v", err))
		self.Error("Failed to create videoscale element", err)
		return
	}

	e.ScaleFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create scale capsfilter: %v", err))
		self.Error("Failed to create scale capsfilter", err)
		return
	}

	e.X264Enc, err = gst.NewElementWithProperties("x264enc", map[string]interface{}{
		// "bitrate":          uint(2000),
		"speed-preset":     int(1),
		"tune":             uint(4),
		"key-int-max":      uint(12),
		"bframes":          uint(0),
		"vbv-buf-capacity": uint(2000),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create x264enc element: %v", err))
		self.Error("Failed to create x264enc element", err)
		return
	}

	e.H264Parse, err = gst.NewElementWithProperties("h264parse", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264parse element: %v", err))
		self.Error("Failed to create h264parse element", err)
		return
	}

	e.RtpH264Pay, err = gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"mtu":             int(1200),
		"config-interval": int(1),
		"aggregate-mode":  int(1),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtph264pay element: %v", err))
		self.Error("Failed to create rtph264pay element", err)
		return
	}

	e.RtpH264CapsIntersect, err = gst.NewElementWithProperties("rtph264capsintersect", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtph264capsintersect element: %v", err))
		self.Error("Failed to create rtph264capsintersect element", err)
		return
	}

	wscaleFilter := glib.WeakRefInit(e.ScaleFilter)
	wself := glib.WeakRefInit(self)
	if _, err := e.RtpH264CapsIntersect.Connect("max-resolution", func(_ *gst.Element, w, h int) {
		self := gst.ToGstBin(wself.Get())
		if self == nil {
			return
		}
		scaleFilter := gst.ToElement(wscaleFilter.Get())
		if scaleFilter == nil {
			return
		}
		if err := scaleFilter.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf("video/x-raw, width=[1,%d], height=[1,%d], pixel-aspect-ratio=1/1", w, h))); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set scale filter caps: %v", err))
			self.Error("Failed to set scale filter caps", err)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect max-resolution signal: %v", err))
		self.Error("Failed to connect max-resolution signal", err)
	}

	self.AddMany(
		e.VideoScale,
		e.ScaleFilter,
		e.X264Enc,
		e.H264Parse,
		e.RtpH264Pay,
		e.RtpH264CapsIntersect,
	)

	if err := gst.ElementLinkMany(
		e.VideoScale,
		e.ScaleFilter,
		e.X264Enc,
		e.H264Parse,
		e.RtpH264Pay,
		e.RtpH264CapsIntersect,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.VideoScale.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpH264CapsIntersect.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

// func (e *VideoH264) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
// 	self := gst.ToGstBin(instance)
// 	param := properties[id]
// 	switch param.Name() {
// 	case "pt":
// 		gv, _ := value.GoValue()
// 		val, _ := gv.(uint)
// 		if val > 127 {
// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid H264 PT value: %d", val))
// 			return
// 		}
// 		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Setting H264 PT to %d", val))
// 		if err := e.RtpH264Pay.SetProperty("pt", val); err != nil {
// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set H264 PT: %v", err))
// 		}
// 	}
// }

// func (e *VideoH264) GetProperty(instance *glib.Object, id uint) *glib.Value {
// 	self := gst.ToGstBin(instance)
// 	param := properties[id]
// 	switch param.Name() {
// 	case "pt":
// 		val, err := e.RtpH264Pay.GetProperty("pt")
// 		if err != nil {
// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get H264 PT: %v", err))
// 			return nil
// 		}
// 		gv, err := glib.GValue(val)
// 		if err != nil {
// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert H264 PT to GValue: %v", err))
// 			return nil
// 		}
// 		return gv
// 	default:
// 		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property ID: %d", id))
// 		return nil
// 	}
// }

func (e *VideoH264) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing VideoH264 element")

	e.VideoScale = nil
	e.ScaleFilter = nil
	e.X264Enc = nil
	e.H264Parse = nil
	e.RtpH264Pay = nil
	e.RtpH264CapsIntersect = nil
}
