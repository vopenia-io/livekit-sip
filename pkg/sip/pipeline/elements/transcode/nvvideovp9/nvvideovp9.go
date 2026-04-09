package nvvideovp9

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"nv-video-vp9",
	gst.DebugColorNone,
	"nv-video-vp9 Element",
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

type NvVideoVp9 struct {
	videoWidth  uint
	videoHeight uint

	CudaConvertScale *gst.Element
	Filter           *gst.Element
	CudaDownload     *gst.Element
	Vp9Enc           *gst.Element
	Vp9Pay           *gst.Element
}

func (e *NvVideoVp9) New() glib.GoObjectSubclass {
	return &NvVideoVp9{}
}

func (e *NvVideoVp9) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Video to VP9 Encoder (CUDA input)",
		"Video/Encoder",
		"Encodes CUDA memory video to VP9 RTP (downloads to CPU; NVENC has no VP9)",
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
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)VP9"),
	))

	class.InstallProperties(properties)
}

func (e *NvVideoVp9) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
}

func (e *NvVideoVp9) Constructed(instance *glib.Object) {
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

	e.Vp9Enc, err = gst.NewElementWithProperties("vp9enc", map[string]interface{}{
		// vp9enc realtime preset: deadline=1 + cpu-used=8 keeps the
		// encoder fast enough to avoid back-pressure; lag-in-frames=0
		// disables the 25-frame lookahead buffer that otherwise adds
		// ~1s of latency on the first frames of a stream.
		"deadline":      int(1),
		"cpu-used":      int(8),
		"lag-in-frames": int(0),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp9enc element: %v", err))
		self.Error("Failed to create vp9enc element", err)
		return
	}

	e.Vp9Pay, err = gst.NewElementWithProperties("rtpvp9pay", map[string]interface{}{
		"mtu": int(1200),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpvp9pay element: %v", err))
		self.Error("Failed to create rtpvp9pay element", err)
		return
	}

	if err := self.AddMany(
		e.CudaConvertScale,
		e.Filter,
		e.CudaDownload,
		e.Vp9Enc,
		e.Vp9Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.CudaConvertScale,
		e.Filter,
		e.CudaDownload,
		e.Vp9Enc,
		e.Vp9Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.CudaConvertScale.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Vp9Pay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *NvVideoVp9) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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

func (e *NvVideoVp9) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing NvVideoVp9 element")

	e.CudaConvertScale = nil
	e.Filter = nil
	e.CudaDownload = nil
	e.Vp9Enc = nil
	e.Vp9Pay = nil
}
