package nvvideoav1

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"nv-video-av1",
	gst.DebugColorNone,
	"nv-video-av1 Element",
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
}

type NvVideoAV1 struct {
	videoWidth  uint
	videoHeight uint

	CudaConvertScale *gst.Element
	Filter           *gst.Element
	NvAV1Enc         *gst.Element
	AV1Parse         *gst.Element
	RtpAV1Pay        *gst.Element
}

func (e *NvVideoAV1) New() glib.GoObjectSubclass {
	return &NvVideoAV1{}
}

func (e *NvVideoAV1) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Video to AV1 Encoder",
		"Video/Encoder",
		"Encodes raw video to AV1 RTP",
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
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)AV1"),
	))

	class.InstallProperties(properties)
}

func (e *NvVideoAV1) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
}

func (e *NvVideoAV1) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.CudaConvertScale, err = gst.NewElementWithProperties("cudaconvertscale", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create cudaconvertscale element: %v", err))
		self.Error("Failed to create cudaconvertscale element", err)
		return
	}

	e.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw(memory:CUDAMemory), width=[1,%d], height=[1,%d], pixel-aspect-ratio=1/1", e.videoWidth, e.videoHeight)),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create scale capsfilter: %v", err))
		self.Error("Failed to create scale capsfilter", err)
		return
	}

	e.CudaConvertScale, err = gst.NewElementWithProperties("cudaconvertscale", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create cudaconvertscale element: %v", err))
		self.Error("Failed to create cudaconvertscale element", err)
		return
	}

	e.NvAV1Enc, err = gst.NewElementWithProperties("nvav1enc", map[string]interface{}{
		// "zerolatency":  true,
		// "preset":       int(5), // low-latency-hp
		// "rc-mode":      int(5), // cbr-ld-hq
		// "rc-lookahead": uint(0),
		// "bframes":      uint(0),
		// "gop-size":     int(-1), // infinite
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create nvav1enc element: %v", err))
		self.Error("Failed to create nvav1enc element", err)
		return
	}

	e.AV1Parse, err = gst.NewElementWithProperties("av1parse", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create av1parse element: %v", err))
		self.Error("Failed to create av1parse element", err)
		return
	}

	e.RtpAV1Pay, err = gst.NewElementWithProperties("rtpav1pay", map[string]interface{}{
		"mtu":             int(1200),
		"config-interval": int(1),
		"aggregate-mode":  int(1),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpav1pay element: %v", err))
		self.Error("Failed to create rtpav1pay element", err)
		return
	}

	if err := self.AddMany(
		e.CudaConvertScale,
		e.Filter,
		e.NvAV1Enc,
		e.AV1Parse,
		e.RtpAV1Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.CudaConvertScale,
		e.Filter,
		e.NvAV1Enc,
		e.AV1Parse,
		e.RtpAV1Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.CudaConvertScale.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpAV1Pay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *NvVideoAV1) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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

func (e *NvVideoAV1) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing NvVideoAV1 element")

	e.CudaConvertScale = nil
	e.Filter = nil
	e.NvAV1Enc = nil
	e.AV1Parse = nil
	e.RtpAV1Pay = nil
}
