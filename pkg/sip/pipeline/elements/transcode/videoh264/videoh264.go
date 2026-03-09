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

var properties = []*glib.ParamSpec{
	glib.NewUintParam(
		"h264-pt",
		"H264 Payload Type",
		"The payload type of H264 RTP stream",
		0,
		127,
		96,
		glib.ParameterWritable,
	),
}

type VideoH264 struct {
	X264Enc    *gst.Element
	H264Parse  *gst.Element
	RtpH264Pay *gst.Element
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
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)H264"),
	))

	class.InstallProperties(properties)
}

func (e *VideoH264) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.X264Enc, err = gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"bitrate":          uint(2000),
		"speed-preset":     int(1),
		"tune":             uint(4),
		"key-int-max":      uint(12),
		"bframes":          uint(0),
		"vbv-buf-capacity": uint(2000),
	})
	if err != nil {
		self.Error("Failed to create x264enc element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create x264enc element: %v", err))
		return
	}

	e.H264Parse, err = gst.NewElementWithProperties("h264parse", map[string]interface{}{})
	if err != nil {
		self.Error("Failed to create h264parse element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264parse element: %v", err))
		return
	}

	e.RtpH264Pay, err = gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"mtu":             int(1200),
		"config-interval": int(1),
		"aggregate-mode":  int(1),
	})
	if err != nil {
		self.Error("Failed to create rtph264pay element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtph264pay element: %v", err))
		return
	}

	self.AddMany(
		e.X264Enc,
		e.H264Parse,
		e.RtpH264Pay,
	)

	if err := gst.ElementLinkMany(
		e.X264Enc,
		e.H264Parse,
		e.RtpH264Pay,
	); err != nil {
		self.Error("Failed to link elements", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.X264Enc.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpH264Pay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *VideoH264) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "h264-pt":
		gv, _ := value.GoValue()
		val, _ := gv.(uint)
		if val > 127 {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid H264 PT value: %d", val))
			return
		}
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Setting H264 PT to %d", val))
		if err := e.RtpH264Pay.SetProperty("pt", val); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set H264 PT: %v", err))
		}
	}
}

func (e *VideoH264) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.X264Enc = nil
		e.H264Parse = nil
		e.RtpH264Pay = nil
	}

	return ret
}
