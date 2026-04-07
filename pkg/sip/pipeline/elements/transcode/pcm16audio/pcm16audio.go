package pcm16audio

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"pcm16-audio",
	gst.DebugColorNone,
	"pcm16-audio Element",
)

type PCM16Audio struct {
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	AudioRate     *gst.Element
}

func (e *PCM16Audio) New() glib.GoObjectSubclass {
	return &PCM16Audio{}
}

func (e *PCM16Audio) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"PCM16 to Audio Decoder",
		"Audio/Decoder",
		"Decodes PCM16 to raw audio",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("audio/x-raw, format=S16LE"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("audio/x-raw"),
	))
}

func (e *PCM16Audio) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.AudioConvert, err = gst.NewElement("audioconvert")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioconvert element: %v", err))
		self.Error("Failed to create audioconvert element", err)
		return
	}

	e.AudioResample, err = gst.NewElement("audioresample")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioresample element: %v", err))
		self.Error("Failed to create audioresample element", err)
		return
	}

	e.AudioRate, err = gst.NewElement("audiorate")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audiorate element: %v", err))
		self.Error("Failed to create audiorate element", err)
		return
	}

	if err := self.AddMany(
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.AudioConvert.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.AudioRate.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *PCM16Audio) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing PCM16Audio element")

	e.AudioConvert = nil
	e.AudioResample = nil
	e.AudioRate = nil
}
