package nvvideovp8

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"nv-video-vp8",
	gst.DebugColorNone,
	"nv-video-vp8 Element",
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

type NvVideoVp8 struct {
	videoWidth  uint
	videoHeight uint

	CudaConvertScale *gst.Element
	Filter           *gst.Element
	CudaDownload     *gst.Element
	Vp8Enc           *gst.Element
	Vp8Pay           *gst.Element
}

func (e *NvVideoVp8) New() glib.GoObjectSubclass {
	return &NvVideoVp8{}
}

func (e *NvVideoVp8) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Video to VP8 Encoder (GL input)",
		"Video/Encoder",
		"Encodes GL memory video to VP8 RTP",
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
		gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)VP8"),
	))

	class.InstallProperties(properties)

}

func (e *NvVideoVp8) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
}

func (e *NvVideoVp8) Constructed(instance *glib.Object) {
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

	e.CudaDownload, err = gst.NewElementWithProperties("cudadownload", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create cudadownload element: %v", err))
		self.Error("Failed to create cudadownload element", err)
		return
	}

	e.Vp8Enc, err = gst.NewElementWithProperties("vp8enc", map[string]interface{}{
		"deadline":            int(1), // realtime
		"cpu-used":            int(8),
		"keyframe-max-dist":   int(12),
		"lag-in-frames":       int(0),
		"threads":             int(2),
		"token-partitions":    int(2),
		"buffer-initial-size": int(200),
		"buffer-optimal-size": int(300),
		"buffer-size":         int(500),
		"min-quantizer":       int(4),
		"max-quantizer":       int(32),
		"cq-level":            int(10),
		"error-resilient":     int(1),
		"end-usage":           int(1), // CBR
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp8enc element: %v", err))
		self.Error("Failed to create vp8enc element", err)
		return
	}

	e.Vp8Pay, err = gst.NewElementWithProperties("rtpvp8pay", map[string]interface{}{
		"mtu":             int(1200),
		"picture-id-mode": int(2),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpvp8pay element: %v", err))
		self.Error("Failed to create rtpvp8pay element", err)
		return
	}

	if err := self.AddMany(
		e.CudaConvertScale,
		e.Filter,
		e.CudaDownload,
		e.Vp8Enc,
		e.Vp8Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.CudaConvertScale,
		e.Filter,
		e.CudaDownload,
		e.Vp8Enc,
		e.Vp8Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.CudaConvertScale.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Vp8Pay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *NvVideoVp8) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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

func (e *NvVideoVp8) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing NvVideoVp8 element")

	e.CudaConvertScale = nil
	e.Filter = nil
	e.CudaDownload = nil
	e.Vp8Enc = nil
	e.Vp8Pay = nil
}
