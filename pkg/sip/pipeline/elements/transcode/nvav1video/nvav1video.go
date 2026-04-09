package nvav1video

/*
#cgo pkg-config: gstreamer-1.0
#include <gst/gst.h>

extern void add_fix_pts_probe(GstElement *element);
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"nv-av1-video",
	gst.DebugColorNone,
	"nv-av1-video Element",
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

type NvAv1Video struct {
	videoWidth  uint
	videoHeight uint

	AV1Depay         *gst.Element
	AV1Parse         *gst.Element
	NvAV1Dec         *gst.Element
	CudaConvertScale *gst.Element
	Filter           *gst.Element
}

func (e *NvAv1Video) New() glib.GoObjectSubclass {
	return &NvAv1Video{}
}

func (e *NvAv1Video) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"AV1 to Video Decoder (NVIDIA)",
		"Video/Decoder",
		"Decodes AV1 RTP to GL memory video using NVIDIA hardware",
		"Roomkit <roomkit-visio@numerique.gouv.fr>>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)AV1"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("video/x-raw(memory:CUDAMemory)"),
	))

	class.InstallProperties(properties)
}

func (e *NvAv1Video) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
}

func (e *NvAv1Video) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.AV1Depay, err = gst.NewElementWithProperties("rtpav1depay", map[string]interface{}{
		"request-keyframe":  true,
		"wait-for-keyframe": false,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpav1depay element: %v", err))
		self.Error("Failed to create rtpav1depay element", err)
		return
	}

	e.AV1Parse, err = gst.NewElementWithProperties("av1parse", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create av1parse element: %v", err))
		self.Error("Failed to create av1parse element", err)
		return
	}

	C.add_fix_pts_probe((*C.GstElement)(unsafe.Pointer(e.AV1Parse.Instance())))

	e.NvAV1Dec, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"nvav1dec",
			"nv-av1-high-dec",
		}),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create nvav1dec element: %v", err))
		self.Error("Failed to create nvav1dec element", err)
		return
	}

	e.CudaConvertScale, err = gst.NewElementWithProperties("cudaconvertscale", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create cudaconvertscale element: %v", err))
		self.Error("Failed to create cudaconvertscale element", err)
		return
	}

	e.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw(memory:CUDAMemory),width=[1,%d],height=[1,%d],pixel-aspect-ratio=1/1", e.videoWidth, e.videoHeight)),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element: %v", err))
		self.Error("Failed to create capsfilter element", err)
		return
	}

	if err := self.AddMany(
		e.AV1Depay,
		e.AV1Parse,
		e.NvAV1Dec,
		e.CudaConvertScale,
		e.Filter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.AV1Depay,
		e.AV1Parse,
		e.NvAV1Dec,
		e.CudaConvertScale,
		e.Filter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.AV1Depay.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Filter.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *NvAv1Video) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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

func (e *NvAv1Video) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing NvAV1Video element")

	e.AV1Depay = nil
	e.AV1Parse = nil
	e.NvAV1Dec = nil
	e.CudaConvertScale = nil
	e.Filter = nil
}
