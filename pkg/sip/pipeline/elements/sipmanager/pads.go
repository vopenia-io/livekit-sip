package sipmanager

import (
	"fmt"
	"strings"

	"github.com/go-gst/go-gst/gst"
	"github.com/vopenia-io/go-pjmedia/pj"
)

func (s *SipManager) mediaGhostPadAddSrc(self *gst.Bin, sipMedia *GstSipMedia, id uint) error {
	var kind string
	switch sipMedia.Type {
	case pj.PJMEDIA_TYPE_AUDIO:
		kind = "audio"
	case pj.PJMEDIA_TYPE_VIDEO:
		kind = "video"
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported media type for ghost pad removal: %d", sipMedia.Type))
		return fmt.Errorf("unsupported media type for ghost pad removal: %d", sipMedia.Type)
	}

	// src pads
	srcRtpPadName := fmt.Sprintf("src_%s_%d", kind, id)
	srcRtcpPadName := fmt.Sprintf("src_rtcp_%s_%d", kind, id)

	srcRtpPad := sipMedia.Element.GetStaticPad("src")
	if srcRtpPad == nil {
		self.Log(CAT, gst.LevelError, "Failed to get RTP src pad")
		return fmt.Errorf("failed to get RTP src pad")
	}
	srcRtpGhostPad := gst.NewGhostPad(srcRtpPadName, srcRtpPad)
	if srcRtpGhostPad == nil {
		self.Log(CAT, gst.LevelError, "Failed to create RTP ghost pad")
		return fmt.Errorf("failed to create RTP ghost pad")
	}
	if !srcRtpGhostPad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate RTP ghost pad")
		return fmt.Errorf("failed to activate RTP ghost pad")
	}
	if !self.AddPad(srcRtpGhostPad.Pad) {
		self.Log(CAT, gst.LevelError, "Failed to add RTP ghost pad to bin")
		return fmt.Errorf("failed to add RTP ghost pad to bin")
	}

	srcRtcpPad := sipMedia.Element.GetStaticPad("src_rtcp")
	if srcRtcpPad == nil {
		self.Log(CAT, gst.LevelError, "Failed to get RTCP src pad")
		return fmt.Errorf("failed to get RTCP src pad")
	}
	srcRtcpGhostPad := gst.NewGhostPad(srcRtcpPadName, srcRtcpPad)
	if srcRtcpGhostPad == nil {
		self.Log(CAT, gst.LevelError, "Failed to create RTCP ghost pad")
		return fmt.Errorf("failed to create RTCP ghost pad")
	}
	if !srcRtcpGhostPad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate RTCP ghost pad")
		return fmt.Errorf("failed to activate RTCP ghost pad")
	}
	if !self.AddPad(srcRtcpGhostPad.Pad) {
		self.Log(CAT, gst.LevelError, "Failed to add RTCP ghost pad to bin")
		return fmt.Errorf("failed to add RTCP ghost pad to bin")
	}

	// // sink pads
	// sinkRtpPadName := fmt.Sprintf("sink_%s_%d", kind, id)
	// sinkRtcpPadName := fmt.Sprintf("sink_rtcp_%s_%d", kind, id)

	// sinkRtpPad := sipMedia.Element.GetStaticPad("sink")
	// if sinkRtpPad == nil {
	// 	self.Log(CAT, gst.LevelError, "Failed to get RTP sink pad")
	// 	return fmt.Errorf("failed to get RTP sink pad")
	// }
	// sinkRtpGhostPad := gst.NewGhostPad(sinkRtpPadName, sinkRtpPad)
	// if sinkRtpGhostPad == nil {
	// 	self.Log(CAT, gst.LevelError, "Failed to create RTP sink ghost pad")
	// 	return fmt.Errorf("failed to create RTP sink ghost pad")
	// }
	// if !sinkRtpGhostPad.SetActive(true) {
	// 	self.Log(CAT, gst.LevelError, "Failed to activate RTP sink ghost pad")
	// 	return fmt.Errorf("failed to activate RTP sink ghost pad")
	// }
	// if !self.AddPad(sinkRtpGhostPad.Pad) {
	// 	self.Log(CAT, gst.LevelError, "Failed to add RTP sink ghost pad to bin")
	// 	return fmt.Errorf("failed to add RTP sink ghost pad to bin")
	// }

	// sinkRtcpPad := sipMedia.Element.GetStaticPad("sink_rtcp")
	// if sinkRtcpPad == nil {
	// 	self.Log(CAT, gst.LevelError, "Failed to get RTCP sink pad")
	// 	return fmt.Errorf("failed to get RTCP sink pad")
	// }
	// sinkRtcpGhostPad := gst.NewGhostPad(sinkRtcpPadName, sinkRtcpPad)
	// if sinkRtcpGhostPad == nil {
	// 	self.Log(CAT, gst.LevelError, "Failed to create RTCP sink ghost pad")
	// 	return fmt.Errorf("failed to create RTCP sink ghost pad")
	// }
	// if !sinkRtcpGhostPad.SetActive(true) {
	// 	self.Log(CAT, gst.LevelError, "Failed to activate RTCP sink ghost pad")
	// 	return fmt.Errorf("failed to activate RTCP sink ghost pad")
	// }
	// if !self.AddPad(sinkRtcpGhostPad.Pad) {
	// 	self.Log(CAT, gst.LevelError, "Failed to add RTCP sink ghost pad to bin")
	// 	return fmt.Errorf("failed to add RTCP sink ghost pad to bin")
	// }

	return nil
}

func (s *SipManager) mediaGhostPadRemove(self *gst.Bin, id uint) error {
	if int(id) >= len(s.medias) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Media %d does not exist, cannot remove ghost pads", id))
		return nil
	}

	media := s.medias[id]
	if media == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Media %d is nil, cannot determine type for ghost pad removal", id))
		return nil
	}

	var kind string
	switch media.Type {
	case pj.PJMEDIA_TYPE_AUDIO:
		kind = "audio"
	case pj.PJMEDIA_TYPE_VIDEO:
		kind = "video"
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported media type for ghost pad removal: %d", media.Type))
		return fmt.Errorf("unsupported media type for ghost pad removal: %d", media.Type)
	}

	srcRtpPadName := fmt.Sprintf("src_%s_%d", kind, id)
	srcRtcpPadName := fmt.Sprintf("src_rtcp_%s_%d", kind, id)
	srcRtpGhostPad := self.GetStaticPad(srcRtpPadName)
	if srcRtpGhostPad != nil {
		self.RemovePad(srcRtpGhostPad)
		srcRtpGhostPad.SetActive(false)
	}

	srcRtcpGhostPad := self.GetStaticPad(srcRtcpPadName)
	if srcRtcpGhostPad != nil {
		self.RemovePad(srcRtcpGhostPad)
		srcRtcpGhostPad.SetActive(false)
	}

	sinkRtpPadName := fmt.Sprintf("sink_%s_%d", kind, id)
	sinkRtcpPadName := fmt.Sprintf("sink_rtcp_%s_%d", kind, id)

	sinkRtpGhostPad := self.GetStaticPad(sinkRtpPadName)
	if sinkRtpGhostPad != nil {
		self.RemovePad(sinkRtpGhostPad)
		sinkRtpGhostPad.SetActive(false)
	}

	sinkRtcpGhostPad := self.GetStaticPad(sinkRtcpPadName)
	if sinkRtcpGhostPad != nil {
		self.RemovePad(sinkRtcpGhostPad)
		sinkRtcpGhostPad.SetActive(false)
	}

	return nil
}

func (s *SipManager) RequestNewPadSinkRtp(self *gst.Bin, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	if name != "" {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Static pad request not supported: %s", name))
		return nil
	}

	parts := strings.SplitN(templ.GetName(), "_", 3)
	if len(parts) != 3 {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid pad name format %s", templ.GetName()))
		return nil
	}
	kind := parts[1]

	var mediaType pj.PjMediaType
	switch kind {
	case "audio":
		mediaType = pj.PJMEDIA_TYPE_AUDIO
	case "video":
		mediaType = pj.PJMEDIA_TYPE_VIDEO
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported media kind for pad request: %s", kind))
		return nil
	}

	for id, media := range s.medias {
		if media == nil || media.Type != mediaType {
			continue
		}
		pname := fmt.Sprintf("sink_%s_%d", kind, id)
		if self.GetStaticPad(pname) != nil {
			continue
		}
		pad := media.Element.GetStaticPad("sink")
		if pad == nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sink pad from media element for pad request: %s", pname))
			return nil
		}
		ghostPad := gst.NewGhostPad(pname, pad)
		if ghostPad == nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad request: %s", pname))
			return nil
		}
		if !ghostPad.SetActive(true) {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad for pad request: %s", pname))
			return nil
		}
		if !self.AddPad(ghostPad.Pad) {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to bin for pad request: %s", pname))
			return nil
		}

		return ghostPad.Pad
	}

	// TODO: trigger media creation with a reinvite.
	// should we create the pad here or durring the answer?
	self.Log(CAT, gst.LevelWarning, "No existing media for requested pad, dynamic media creation not implemented yet")

	return nil
}

func (s *SipManager) RequestNewPadSinkRtcp(self *gst.Bin, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self.Log(CAT, gst.LevelWarning, "RTCP sink pad request handling not implemented yet")
	return nil
}

func (s *SipManager) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	pname := name
	if pname == "" {
		pname = templ.GetName()
	}

	if strings.HasPrefix(pname, "sink_rtcp_") {
		return s.RequestNewPadSinkRtcp(self, templ, name, caps)
	} else if strings.HasPrefix(pname, "sink_") {
		return s.RequestNewPadSinkRtp(self, templ, name, caps)
	} else {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported pad request: %s", name))
		return nil
	}

	// self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Requesting new pad: %s", name))

	// if name != "" {
	// 	var (
	// 		kind string
	// 		id   int
	// 	)
	// 	if _, err := fmt.Sscanf(name, "sink_rtcp_%s_%d", &kind, &id); err != nil {
	// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid RTCP pad name: %s", name))
	// 		return nil
	// 	}

	// 	pad := self.GetStaticPad(fmt.Sprintf("sink_rtcp_%s_%d", kind, id))
	// 	if pad == nil {
	// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Requested RTCP pad does not exist: %s", name))
	// 		return nil
	// 	}
	// 	return pad
	// }

	// var kind string
	// if _, err := fmt.Scanf(templ.GetName(), "sink_%s_", &kind); err != nil {
	// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid pad name format: %s", name))
	// 	return nil
	// }

	// var mediaType pj.PjMediaType
	// switch kind {
	// case "audio":
	// 	mediaType = pj.PJMEDIA_TYPE_AUDIO
	// case "video":
	// 	mediaType = pj.PJMEDIA_TYPE_VIDEO
	// default:
	// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported media kind for pad request: %s", kind))
	// 	return nil
	// }

	// for id, media := range s.medias {
	// 	if media == nil || media.Type != mediaType {
	// 		continue
	// 	}
	// 	pname := fmt.Sprintf("sink_%s_%d", kind, id)
	// 	if self.GetStaticPad(pname) != nil {
	// 		// pad already exists
	// 		continue
	// 	}
	// 	pad := media.Element.GetStaticPad("sink")
	// 	if pad == nil {
	// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sink pad from media element for pad request: %s", pname))
	// 		return nil
	// 	}
	// 	ghostPad := gst.NewGhostPad(pname, pad)
	// 	if ghostPad == nil {
	// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad request: %s", pname))
	// 		return nil
	// 	}
	// 	if !ghostPad.SetActive(true) {
	// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad for pad request: %s", pname))
	// 		return nil
	// 	}
	// 	if !self.AddPad(ghostPad.Pad) {
	// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to bin for pad request: %s", pname))
	// 		return nil
	// 	}

	// 	rtcpPad := media.Element.GetStaticPad("sink_rtcp")
	// 	if rtcpPad == nil {
	// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get RTCP sink pad from media element for pad request: %s", pname))
	// 		return nil
	// 	}
	// 	rtcpGhostPadName := fmt.Sprintf("sink_rtcp_%s_%d", kind, id)
	// 	rtcpGhostPad := gst.NewGhostPad(rtcpGhostPadName, rtcpPad)
	// 	if rtcpGhostPad == nil {
	// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create RTCP ghost pad for pad request: %s", rtcpGhostPadName))
	// 		return nil
	// 	}
	// 	if !rtcpGhostPad.SetActive(true) {
	// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate RTCP ghost pad for pad request: %s", rtcpGhostPadName))
	// 		return nil
	// 	}
	// 	if !self.AddPad(rtcpGhostPad.Pad) {
	// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add RTCP ghost pad to bin for pad request: %s", rtcpGhostPadName))
	// 		return nil
	// 	}

	// 	return ghostPad.Pad
	// }

	// return nil
}
