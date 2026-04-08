package nvav1video

/*
#cgo pkg-config: gstreamer-1.0
#include <gst/gst.h>

extern void add_fix_pts_probe(GstElement *element);
*/
import "C"

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

type NvAv1HighDec struct {
	Av1Dec     *gst.Element
	CudoUpload *gst.Element
}

func (e *NvAv1HighDec) New() glib.GoObjectSubclass {
	return &NvAv1HighDec{}
}

func (e *NvAv1HighDec) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"AV1 High Profile Decoder on Cuda memory (software decode, use only as fallback)",
		"Video/Decoder",
		"Decodes AV1 High Profile video to raw frames in Cuda memory (use only as fallback)",
		"Roomkit <roomkit-visio@numerique.gouv.fr>>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("video/x-av1"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("video/x-raw(memory:CUDAMemory)"),
	))

	class.InstallProperties(properties)
}

func (e *NvAv1HighDec) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	self.Log(CAT, gst.LevelWarning, "Using software AV1 decoder fallback (nv-av1-high-dec).")

	e.Av1Dec, err = gst.NewElementWithProperties("dav1ddec", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpav1depay element: %v", err))
		self.Error("Failed to create rtpav1depay element", err)
		return
	}

	e.CudoUpload, err = gst.NewElementWithProperties("cudaupload", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create cudaupload element: %v", err))
		self.Error("Failed to create cudaupload element", err)
		return
	}

	if err := self.AddMany(
		e.Av1Dec,
		e.CudoUpload,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.Av1Dec,
		e.CudoUpload,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.Av1Dec.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.CudoUpload.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *NvAv1HighDec) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing NvAV1Video element")

	e.Av1Dec = nil
	e.CudoUpload = nil
}
