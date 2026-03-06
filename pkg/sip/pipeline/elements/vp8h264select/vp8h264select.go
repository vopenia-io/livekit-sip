package vp8h264select

import (
	"fmt"
	"sync"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"vp8_h264_select",
	gst.DebugColorNone,
	"vp8-h264 Element",
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

type Branch struct {
	Depay        *gst.Element
	Vp8Dec       *gst.Element
	VideoConvert *gst.Element
	Queue        *gst.Element
}

type Vp8H264Select struct {
	mu            sync.Mutex
	Branches      map[string]Branch
	InputSelector *gst.Element
	VideoConvert  *gst.Element // Is it required?
	VideoScale    *gst.Element
	VideoRate     *gst.Element
	Filter        *gst.Element
	X264Enc       *gst.Element
	H264Parse     *gst.Element
	RtpH264Pay    *gst.Element
}

func (e *Vp8H264Select) New() glib.GoObjectSubclass {
	return &Vp8H264Select{}
}

func (e *Vp8H264Select) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"VP8 to H264 Transcoder/Selector",
		"Video/Converter",
		"Decodes VP8 video and re-encodes it as H264. Can be used to select between VP8 streams in a pipeline.",
		"Your Name <you@example.com>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)VP8"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)H264"),
	))

	class.InstallProperties(properties)
}

func (e *Vp8H264Select) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.Branches = make(map[string]Branch)

	e.InputSelector, err = gst.NewElementWithProperties("input-selector", map[string]interface{}{})
	if err != nil {
		self.Error("Failed to create input-selector element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create input-selector element: %v", err))
		return
	}

	e.VideoConvert, err = gst.NewElementWithProperties("videoconvert", map[string]interface{}{})
	if err != nil {
		self.Error("Failed to create videoconvert element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoconvert element: %v", err))
		return
	}

	e.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		self.Error("Failed to create videoscale element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoscale element: %v", err))
		return
	}

	e.VideoRate, err = gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true,
	})
	if err != nil {
		self.Error("Failed to create videorate element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videorate element: %v", err))
		return
	}

	e.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1,framerate=24/1"),
	})
	if err != nil {
		self.Error("Failed to create capsfilter element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element: %v", err))
		return
	}

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

	// Add shared elements to the bin
	self.AddMany(
		e.InputSelector,
		e.VideoConvert,
		e.VideoScale,
		e.VideoRate,
		e.Filter,
		e.X264Enc,
		e.H264Parse,
		e.RtpH264Pay,
	)

	// Link the shared elements together
	if err := gst.ElementLinkMany(
		e.InputSelector,
		e.VideoConvert,
		e.VideoScale,
		e.VideoRate,
		e.Filter,
		e.X264Enc,
		e.H264Parse,
		e.RtpH264Pay,
	); err != nil {
		self.Error("Failed to link elements", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpH264Pay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *Vp8H264Select) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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

func (e *Vp8H264Select) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.Branches = nil
		e.InputSelector = nil
		e.VideoConvert = nil
		e.VideoScale = nil
		e.VideoRate = nil
		e.Filter = nil
		e.X264Enc = nil
		e.H264Parse = nil
		e.RtpH264Pay = nil
	}

	return ret
}

func (e *Vp8H264Select) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	e.mu.Lock()
	defer e.mu.Unlock()

	self := gst.ToGstBin(instance)
	if templ.Name() != "sink_%u" {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid pad template name: %s", templ.Name()))
		return nil
	}

	sink := e.InputSelector.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to get request pad")
		return nil
	}
	// sink.SetQData(QDataPadSwitching, false)

	// eweak := weak.Make(e)
	// sink.AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	// 	if ptr := eweak.Value(); ptr != nil {
	// 		return ptr.OnSelectorEvent(pad, info)
	// 	}
	// 	return gst.PadProbeRemove
	// })

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Creating new branch for pad: %s", sink.GetName()))

	depay, err := gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpvp8depay element: %v", err))
		return nil
	}

	vp8dec, err := gst.NewElementWithProperties("vp8dec", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp8dec element: %v", err))
		return nil
	}

	videoconvert, err := gst.NewElementWithProperties("videoconvert", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoconvert element: %v", err))
		return nil
	}

	queue, err := gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element: %v", err))
		return nil
	}

	if err := self.AddMany(depay, vp8dec, videoconvert, queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add branch elements to bin: %v", err))
		return nil
	}

	if err := gst.ElementLinkMany(depay, vp8dec, videoconvert, queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link branch elements: %v", err))
		return nil
	}

	if ret := queue.GetStaticPad("src").Link(sink); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue to input selector: %v", ret))
		return nil
	}

	for _, elem := range []*gst.Element{depay, vp8dec, videoconvert, queue} {
		if !elem.SyncStateWithParent() {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to sync element %s state with parent", elem.GetName()))
			return nil
		}
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(sink.GetName(), depay.GetStaticPad("sink"), class.GetPadTemplate("sink_%u"))
	if gpad == nil {
		self.Log(CAT, gst.LevelError, "Failed to create ghost pad")
		return nil
	}

	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad")
		return nil
	}

	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad: %s", gpad.GetName()))
		return nil
	}

	e.Branches[gpad.GetName()] = Branch{
		Depay:        depay,
		Vp8Dec:       vp8dec,
		VideoConvert: videoconvert,
		Queue:        queue,
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Added new pad: %s", gpad.GetName()))

	activeVal, err := e.InputSelector.GetProperty("active-pad")
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get active pad: %v", err))
	} else {
		activePad := activeVal.(*gst.Pad)
		if activePad == nil {
			if err := e.InputSelector.SetProperty("active-pad", sink); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set active pad: %v", err))
			}
		}
	}

	return gpad.Pad
}

func (e *Vp8H264Select) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	e.mu.Lock()
	defer e.mu.Unlock()

	self := gst.ToGstBin(instance)

	if pad.GetDirection() != gst.PadDirectionSink {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid pad direction: %s", pad.GetDirection()))
		return
	}

	gpad := pad.AsGhostPad()
	if gpad == nil {
		return
	}

	templ := gpad.Template()
	if templ == nil || templ.Name() != "sink_%u" {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid pad template for release: %s", templ.Name()))
		return
	}

	branch, ok := e.Branches[pad.GetName()]
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("No branch found for pad: %s", pad.GetName()))
		return
	}

	// Get the InputSelector sink pad before tearing down the branch
	selectorSink := branch.Queue.GetStaticPad("src").GetPeer()

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Releasing pad: %s", pad.GetName()))

	for _, elem := range []*gst.Element{branch.Depay, branch.Vp8Dec, branch.VideoConvert, branch.Queue} {
		if err := elem.SetState(gst.StateNull); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set element %s state to null: %v", elem.GetName(), err))
		}
		if err := self.Remove(elem); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove element %s from bin: %v", elem.GetName(), err))
		}
	}

	if selectorSink != nil {
		e.InputSelector.ReleaseRequestPad(selectorSink)
	}

	delete(e.Branches, pad.GetName())

	if !pad.SetActive(false) {
		self.Log(CAT, gst.LevelWarning, "Failed to deactivate ghost pad")
	}
	if !self.RemovePad(pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad from bin")
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released pad: %s", pad.GetName()))

	activeVal, err := e.InputSelector.GetProperty("active-pad")
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get active pad: %v", err))
	} else {
		activePad := activeVal.(*gst.Pad)
		if activePad == nil {
			for _, branch := range e.Branches {
				sink := branch.VideoConvert.GetStaticPad("src").GetPeer()
				if sink != nil {
					if err := e.InputSelector.SetProperty("active-pad", sink); err != nil {
						self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set active pad: %v", err))
					} else {
						break
					}
				}
			}
		}
	}
}
