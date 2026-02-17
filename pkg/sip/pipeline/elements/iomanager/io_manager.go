package iomanager

import (
	"github.com/go-gst/go-gst/gst"
)

type SessionKind int

const (
	SessionKindMicrophone SessionKind = iota
	SessionKindCamera
	SessionKindScreenShare
	SessionKindScreenShareAudio
)

func (s SessionKind) String() string {
	switch s {
	case SessionKindMicrophone:
		return "microphone"
	case SessionKindCamera:
		return "camera"
	case SessionKindScreenShare:
		return "screenshare"
	case SessionKindScreenShareAudio:
		return "screenshare-audio"
	default:
		return "unknown"
	}
}

var CAT = gst.NewDebugCategory(
	"io_manager",
	gst.DebugColorNone,
	"livekit SIP pipeline IO elements",
)

// type IoManager struct {
// 	self *gst.Bin

// 	IoSip     *gst.Element
// 	IoLivekit *gst.Element
// }

// func (e *IoManager) New() glib.GoObjectSubclass {
// 	return &IoManager{}
// }

// func (e *IoManager) ClassInit(klass *glib.ObjectClass) {
// 	class := gst.ToElementClass(klass)
// 	class.SetMetadata(
// 		"io_manager",
// 		"Audio/Video/Converter",
// 		"Manages the input and output of the SIP pipeline",
// 		"Maxime SENARD <senard.maxime@gmail.com>",
// 	)

// 	class.AddPadTemplate(gst.NewPadTemplate(
// 		"livekit_recv_rtp_sink_%u_%u_%u",
// 		gst.PadDirectionSink,
// 		gst.PadPresenceRequest,
// 		gst.NewCapsFromString("application/x-rtp"),
// 	))

// 	class.AddPadTemplate(gst.NewPadTemplate(
// 		"livekit_send_rtp_src_%u",
// 		gst.PadDirectionSource,
// 		gst.PadPresenceSometimes,
// 		gst.NewCapsFromString("application/x-rtp"),
// 	))

// 	class.AddPadTemplate(gst.NewPadTemplate(
// 		"sip_recv_rtp_sink_%u_%u_%u",
// 		gst.PadDirectionSink,
// 		gst.PadPresenceRequest,
// 		gst.NewCapsFromString("application/x-rtp"),
// 	))

// 	class.AddPadTemplate(gst.NewPadTemplate(
// 		"sip_send_rtp_src_%u",
// 		gst.PadDirectionSource,
// 		gst.PadPresenceSometimes,
// 		gst.NewCapsFromString("application/x-rtp"),
// 	))
// }

// func (e *IoManager) sipPadAdded(self *gst.Bin, pad *gst.Pad) {
// 	pname := pad.GetName()
// 	if pname == "" || !strings.HasPrefix(pname, "send_rtp_src_") {
// 		return
// 	}

// 	pname = "sip_" + pname

// 	if _, err := fmt.Sscanf(pname, "sip_send_rtp_src_%d", new(int)); err != nil {
// 		self.Error("Failed to parse pad name", err)
// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse pad name: %s", pname))
// 		return
// 	}

// 	class := gst.ToElementClass(self.Class())

// 	gpad := gst.NewGhostPadFromTemplate(pname, pad, class.GetPadTemplate("sip_send_rtp_src_%u"))
// 	if !self.AddPad(gpad.Pad) {
// 		self.Error("Failed to add ghost pad", nil)
// 		self.Log(CAT, gst.LevelError, "Failed to add ghost pad")
// 		return
// 	}

// 	if !gpad.Pad.SetActive(true) {
// 		self.Error("Failed to activate ghost pad", nil)
// 		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad")
// 		return
// 	}
// }

// func (e *IoManager) livekitPadAdded(self *gst.Bin, pad *gst.Pad) {
// 	pname := pad.GetName()
// 	if pname == "" || !strings.HasPrefix(pname, "send_rtp_src_") {
// 		return
// 	}

// 	pname = "livekit_" + pname

// 	if _, err := fmt.Sscanf(pname, "livekit_send_rtp_src_%d", new(int)); err != nil {
// 		self.Error("Failed to parse pad name", err)
// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse pad name: %s", pname))
// 		return
// 	}

// 	class := gst.ToElementClass(self.Class())

// 	gpad := gst.NewGhostPadFromTemplate(pname, pad, class.GetPadTemplate("livekit_send_rtp_src_%u"))
// 	if !self.AddPad(gpad.Pad) {
// 		self.Error("Failed to add ghost pad", nil)
// 		self.Log(CAT, gst.LevelError, "Failed to add ghost pad")
// 		return
// 	}

// 	if !gpad.Pad.SetActive(true) {
// 		self.Error("Failed to activate ghost pad", nil)
// 		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad")
// 		return
// 	}
// }

// func (e *IoManager) InstanceInit(instance *glib.Object) {
// 	self := gst.ToGstBin(instance)
// 	self.Log(CAT, gst.LevelInfo, "Initializing io_manager element")

// 	e.self = self

// 	we := weak.Make(e)

// 	var err error
// 	e.IoSip, err = gst.NewElement("io_manager_sip")
// 	if err != nil {
// 		self.Error("Failed to create io_manager_sip element", err)
// 		self.Log(CAT, gst.LevelError, "Failed to create io_manager_sip element")
// 		return
// 	}
// 	if err := self.Add(e.IoSip); err != nil {
// 		self.Error("Failed to add io_manager_sip element to bin", err)
// 		self.Log(CAT, gst.LevelError, "Failed to add io_manager_sip element to bin")
// 		return
// 	}
// 	e.IoSip.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
// 		e := we.Value()
// 		if e == nil || e.self == nil {
// 			CAT.Log(gst.LevelError, "io_manager instance is nil or not initialized")
// 			return
// 		}

// 		e.sipPadAdded(e.self, pad)
// 	})

// 	e.IoLivekit, err = gst.NewElement("io_manager_livekit")
// 	if err != nil {
// 		self.Error("Failed to create io_manager_livekit element", err)
// 		self.Log(CAT, gst.LevelError, "Failed to create io_manager_livekit element")
// 		return
// 	}
// 	if err := self.Add(e.IoLivekit); err != nil {
// 		self.Error("Failed to add io_manager_livekit element to bin", err)
// 		self.Log(CAT, gst.LevelError, "Failed to add io_manager_livekit element to bin")
// 		return
// 	}
// 	e.IoLivekit.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
// 		e := we.Value()
// 		if e == nil || e.self == nil {
// 			CAT.Log(gst.LevelError, "io_manager instance is nil or not initialized")
// 			return
// 		}

// 		e.livekitPadAdded(e.self, pad)
// 	})

// }

// func (e *IoManager) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
// 	self := gst.ToGstBin(instance)

// 	ret := self.ParentChangeState(transition)
// 	if ret != gst.StateChangeSuccess {
// 		return ret
// 	}

// 	if transition == gst.StateChangeReadyToNull {
// 		e.self = nil
// 		e.IoSip = nil
// 		e.IoLivekit = nil
// 	}
// 	return ret
// }

// func (e *IoManager) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
// 	self := gst.ToGstBin(instance)

// 	if name == "" {
// 		self.Log(CAT, gst.LevelError, "Requested pad with empty name")
// 		return nil
// 	}

// 	if strings.HasPrefix(name, "sip_") {
// 		childName := strings.TrimPrefix(name, "sip_")
// 		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Requesting SIP pad: %s", childName))
// 		childPad := e.IoSip.GetRequestPad(childName)
// 		if childPad == nil {
// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad %s from child", childName))
// 			return nil
// 		}

// 		gpad := gst.NewGhostPadFromTemplate(name, childPad, templ)
// 		if !self.AddPad(gpad.Pad) {
// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad %s", name))
// 			return nil
// 		}

// 		if !gpad.SetActive(true) {
// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad %s", name))
// 			return nil
// 		}

// 		return gpad.Pad
// 	}

// 	if strings.HasPrefix(name, "livekit_") {
// 		childName := strings.TrimPrefix(name, "livekit_")
// 		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Requesting Livekit pad: %s", childName))
// 		childPad := e.IoLivekit.GetRequestPad(childName)
// 		if childPad == nil {
// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad %s from child", childName))
// 			return nil
// 		}

// 		gpad := gst.NewGhostPadFromTemplate(name, childPad, templ)
// 		if !self.AddPad(gpad.Pad) {
// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad %s", name))
// 			return nil
// 		}

// 		if !gpad.SetActive(true) {
// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad %s", name))
// 			return nil
// 		}

// 		return gpad.Pad
// 	}
// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Requested pad with unknown prefix: %s", name))
// 	return nil
// }
