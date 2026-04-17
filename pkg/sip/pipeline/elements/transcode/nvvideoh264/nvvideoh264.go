package nvvideoh264

import (
	"fmt"
	"strconv"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"nv-video-h264",
	gst.DebugColorNone,
	"nv-video-h264 Element",
)

var properties = []*glib.ParamSpec{
	glib.NewUintParam(
		"video-width",
		"Video Width",
		"Maximum width of the encoded video frames",
		1,
		8192,
		1280,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewUintParam(
		"video-height",
		"Video Height",
		"Maximum height of the encoded video frames",
		1,
		8192,
		720,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

type NvVideoH264 struct {
	videoWidth  uint
	videoHeight uint

	CudaConvertScale *gst.Element
	ScaleFilter      *gst.Element
	NvH264Enc        *gst.Element
	H264RtpPayBin    *gst.Element
	RtpCodecFilter   *gst.Element
}

func (e *NvVideoH264) New() glib.GoObjectSubclass {
	return &NvVideoH264{}
}

func (e *NvVideoH264) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Video to H264 Encoder",
		"Video/Encoder",
		"Encodes raw video to H264 RTP",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("video/x-raw(memory:CUDAMemory)"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)H264"),
	))

	class.InstallProperties(properties)
}

func (e *NvVideoH264) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
}

func (e *NvVideoH264) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	wself := glib.WeakRefInit(self)
	eweak := weak.Make(e)

	e.CudaConvertScale, err = gst.NewElementWithProperties("cudaconvertscale", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create cudaconvertscale element: %v", err))
		self.Error("Failed to create cudaconvertscale element", err)
		return
	}

	e.ScaleFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw(memory:CUDAMemory),width=[1,%d],height=[1,%d]", e.videoWidth, e.videoHeight)),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create scale capsfilter: %v", err))
		self.Error("Failed to create scale capsfilter", err)
		return
	}

	defaultBitrate := uint(2048)
	if e.videoHeight*e.videoWidth >= 1920*1080 {
		defaultBitrate = 8192
	} else if e.videoHeight*e.videoWidth >= 1280*720 {
		defaultBitrate = 4096
	}

	e.NvH264Enc, err = gst.NewElementWithProperties("nvh264enc", map[string]interface{}{
		"zerolatency":  true,
		"preset":       int(5), // low-latency-hp
		"rc-mode":      int(5), // cbr-ld-hq
		"rc-lookahead": uint(0),
		"bframes":      uint(0),
		"gop-size":     int(-1), // infinite
		"bitrate":      uint(defaultBitrate),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create nvh264enc element: %v", err))
		self.Error("Failed to create nvh264enc element", err)
		return
	}

	e.H264RtpPayBin, err = gst.NewElementWithProperties("h264rtppaybin", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264rtppaybin element: %v", err))
		self.Error("Failed to create h264rtppaybin element", err)
		return
	}
	if _, err := e.H264RtpPayBin.Connect("max-resolution", func(_ *gst.Element, w, h int) {
		self := gst.ToGstBin(wself.Get())
		if self == nil {
			return
		}
		e := eweak.Value()
		if e == nil {
			return
		}
		w = max(1, min(w, int(e.videoWidth)))
		h = max(1, min(h, int(e.videoHeight)))
		if err := e.ScaleFilter.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf("video/x-raw(memory:CUDAMemory),width=[1,%d],height=[1,%d]", w, h))); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set scale filter caps: %v", err))
			self.Error("Failed to set scale filter caps", err)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect max-resolution signal: %v", err))
		self.Error("Failed to connect max-resolution signal", err)
	}

	e.RtpCodecFilter, err = gst.NewElementWithProperties("rtpcapscodecfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)H264"),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create RTP codec filter element: %v", err))
		self.Error("Failed to create RTP codec filter element", err)
		return
	}
	if _, err := e.RtpCodecFilter.GetStaticPad("sink").Connect("notify::caps", func(pad *gst.Pad, _ *glib.ParamSpec) {
		self := gst.ToGstBin(wself.Get())
		if self == nil {
			return
		}
		e := eweak.Value()
		if e == nil {
			return
		}
		caps := pad.CurrentCaps()
		if caps == nil || caps.IsEmpty() {
			self.Log(CAT, gst.LevelWarning, "RTP codec filter sink caps are empty")
			return
		}
		bitrateStr, err := caps.GetStructureAt(0).GetString("max-br")
		if err != nil {
			self.Log(CAT, gst.LevelWarning, "RTP codec filter sink caps missing max-br")
			return
		}
		bitrate, err := strconv.Atoi(bitrateStr)
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid max-br value in RTP codec filter sink caps: %v", err))
			return
		}
		if bitrate > 0 {
			if err := e.NvH264Enc.SetProperty("bitrate", uint(bitrate)); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set nvh264enc bitrate: %v", err))
				self.Error("Failed to set nvh264enc bitrate", err)
			} else {
				self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Updated nvh264enc bitrate to %d", bitrate))
			}
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect notify::caps signal: %v", err))
		self.Error("Failed to connect notify::caps signal", err)
	}

	if err := self.AddMany(
		e.CudaConvertScale,
		e.ScaleFilter,
		e.NvH264Enc,
		e.H264RtpPayBin,
		e.RtpCodecFilter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.CudaConvertScale,
		e.ScaleFilter,
		e.NvH264Enc,
		e.H264RtpPayBin,
		e.RtpCodecFilter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.CudaConvertScale.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpCodecFilter.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *NvVideoH264) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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
	}
}

func (e *NvVideoH264) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing NvVideoH264 element")

	e.CudaConvertScale = nil
	e.ScaleFilter = nil
	e.NvH264Enc = nil
	e.H264RtpPayBin = nil
	e.RtpCodecFilter = nil
}
