package sipbin

import (
	"fmt"
	"net"
	"regexp"
	"strconv"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/gstsdp"
	"github.com/go-gst/go-gst/gst/rtp"
	"github.com/livekit/protocol/livekit"
)

func (e *SipBin) makeOfferMedia(self *gst.Bin, kind livekit.TrackSource, idx int, proto string) (*gstsdp.Media, error) {
	var targetMedia string
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE:
		targetMedia = "video"
	case livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
		targetMedia = "audio"
	default:
		return nil, fmt.Errorf("unsupported track source: %d", kind)
	}

	media, err := gstsdp.NewMedia()
	if err != nil {
		return nil, fmt.Errorf("failed to create SDP media: %w", err)
	}

	targetCaps := make([]*gst.Caps, 0, len(e.formats))
	for _, caps := range e.formats {
		for i := range caps.GetSize() {
			structure := caps.GetStructureAt(i)
			mediaVal, err := structure.GetValue("media")
			if err != nil {
				continue
			}
			mediaStr, ok := mediaVal.(string)
			if !ok {
				continue
			}
			if mediaStr == targetMedia {
				targetCaps = append(targetCaps, caps.Copy())
				break
			}
		}
	}

	offerCaps := make([]*gst.Caps, 0, len(targetCaps))
	dynamicPt := uint8(96)
	for _, caps := range targetCaps {
		for i := range caps.GetSize() {
			structure := caps.GetStructureAt(i)
			encodingVal, err := structure.GetValue("encoding-name")
			if err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get encoding-name value from caps structure: %v", err))
				continue
			}
			encoding, ok := encodingVal.(string)
			if !ok {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid encoding-name value in caps structure: %v", encodingVal))
				continue
			}
			mediaVal, err := structure.GetValue("media")
			if err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get media value from caps structure: %v", err))
				continue
			}
			mediaStr, ok := mediaVal.(string)
			if !ok {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid media value in caps structure: %v", mediaVal))
				continue
			}

			info := rtp.PayloadInfoForName(mediaStr, encoding)
			if info == nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No payload info found for media %s and encoding %s", mediaStr, encoding))
				continue
			}
			pt := info.PayloadType()
			if pt >= 96 {
				pt = dynamicPt
				dynamicPt++
			}
			if err := structure.SetValue("payload", int(pt)); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set payload type on caps structure: %v", err))
				continue
			}
			offerCaps = append(offerCaps, caps.Copy().Fixate())
		}
	}

	if len(offerCaps) == 0 {
		return nil, fmt.Errorf("no offer caps found for track source %d", kind)
	}

	if ret := gstsdp.MediaSetFromCaps(offerCaps[0], media); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set media from caps: %v", ret)
	}
	for i := range offerCaps[0].GetSize() - 1 {
		structure := offerCaps[0].GetStructureAt(i + 1)
		if ret := gstsdp.MediaAddMediaFromStructure(structure, media); ret != gstsdp.SDPResultOk {
			return nil, fmt.Errorf("failed to add media from structure: %v", ret)
		}
	}

	offerCaps = offerCaps[1:]
	for _, caps := range offerCaps {
		for i := range caps.GetSize() {
			structure := caps.GetStructureAt(i)
			if ret := gstsdp.MediaAddMediaFromStructure(structure, media); ret != gstsdp.SDPResultOk {
				return nil, fmt.Errorf("failed to add media from structure: %v", ret)
			}
		}
	}

	if proto == "" {
		proto = "RTP/AVP"
	}
	if ret := media.SetProto(proto); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set proto on media: %v", ret)
	}

	if ret := media.AddAttribute("rtcp-fb", "* nack pli"); ret != gstsdp.SDPResultOk {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add rtcp-fb attribute to media: %v", ret))
	}
	if ret := media.AddAttribute("rtcp-fb", "* ccm fir"); ret != gstsdp.SDPResultOk {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add rtcp-fb attribute to media: %v", ret))
	}

	switch kind {
	case livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
		if ret := media.AddAttribute("content", "slides"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add content attribute to media: %v", ret))
		}
	case livekit.TrackSource_CAMERA:
		if ret := media.AddAttribute("content", "main"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add content attribute to media: %v", ret))
		}
	}

	track, err := e.NewTrack(self, idx, kind, proto)
	if err != nil {
		return nil, fmt.Errorf("failed to create track for media %d: %w", idx, err)
	}
	e.Tracks[kind] = track

	port := uint(track.rtpConn.LocalAddr().(*net.UDPAddr).Port)
	if ret := media.SetPortInfo(port, 1); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set port info on media: %v", ret)
	}

	return media, nil
}

var bareFmtpName = regexp.MustCompile(`^\d+(?:[,-]\d+)*$`)

func mediaCapsFixBareFmtp(caps *gst.Caps) *gst.Caps {
	for i := range caps.GetSize() {
		structure := caps.GetStructureAt(i)
		toRemove := make([]string, 0)
		for key, value := range structure.Values() {
			str, ok := value.(string)
			if !ok || str != "1" {
				continue
			}
			if bareFmtpName.MatchString(key) {
				toRemove = append(toRemove, key)
			}
		}
		for _, key := range toRemove {
			structure.RemoveValue(key)
		}
	}
	return caps
}

func (e *SipBin) selectCapsForMedia(self *gst.Bin, media *gstsdp.Media, kind livekit.TrackSource) (*gst.Caps, error) {
	mediaCaps := make([]*gst.Caps, 0, media.FormatsLen())
	for _, format := range media.Formats() {
		pt, err := strconv.Atoi(format)
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid format %s for media %s: %v", format, media.GetMedia(), err))
			continue
		}

		caps, err := media.GetCaps(pt)
		if err != nil || caps.GetSize() == 0 {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get caps for format %s and payload type %d: %v", format, pt, err))
			continue
		}
		caps.GetStructureAt(0).SetName("application/x-rtp")
		// caps.GetStructureAt(0).RemoveValue("proto") // TODO: properly handle srtp if we want to support it

		caps = mediaCapsFixBareFmtp(caps)

		info := rtp.PayloadInfoForPt(uint8(pt))
		if info != nil && info.PayloadType() < 96 {
			encodingName, err := caps.GetStructureAt(0).GetString("encoding-name")
			if err != nil || encodingName != info.EncodingName() {
				if err := caps.GetStructureAt(0).SetString("encoding-name", info.EncodingName()); err != nil {
					self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set encoding-name attribute on caps: %v", err))
				}
			}
		}

		if existing, exist := e.PtMap[kind][uint8(pt)]; exist && existing != nil && !existing.IsEqual(caps) {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received duplicate caps for payload type %d: existing %s, new %s", pt, existing.String(), caps.String()))
		}
		e.PtMap[kind][uint8(pt)] = caps

		mediaCaps = append(mediaCaps, caps)
	}

	if len(mediaCaps) == 0 {
		return nil, fmt.Errorf("no caps found for media %s", media.GetMedia())
	}

	res := gst.NewEmptyCaps()
	for _, formatCaps := range e.formats {
		leftover := make([]*gst.Caps, 0, len(mediaCaps))
		for _, caps := range mediaCaps {
			self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Intersecting media caps %s with format caps %s", caps.String(), formatCaps.String()))
			icaps := caps.IntersectFull(formatCaps, gst.CapsIntersectFirst)
			if icaps != nil && !icaps.IsEmpty() {
				self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Found compatible caps for media %s: %s", kind.String(), icaps.String()))
				res.Append(caps.Copy())
			} else {
				leftover = append(leftover, caps)
			}
		}
		mediaCaps = leftover
	}

	if res.IsEmpty() {
		return nil, fmt.Errorf("no compatible caps found for media %s: %s", media.GetMedia(), media.AsText())
	}

	return res, nil
}

func (e *SipBin) makeTrackMedia(self *gst.Bin, track *SipTrack, caps *gst.Caps) (*gstsdp.Media, error) {
	if caps == nil {
		caps = track.Caps
	}
	if caps == nil {
		return nil, fmt.Errorf("no caps available for track media")
	}

	media, err := gstsdp.NewMedia()
	if err != nil {
		return nil, fmt.Errorf("failed to create SDP media: %w", err)
	}
	if ret := gstsdp.MediaSetFromCaps(caps, media); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set media from caps: %v", ret)
	}
	for i := range caps.GetSize() - 1 {
		structure := caps.GetStructureAt(i + 1)
		if ret := gstsdp.MediaAddMediaFromStructure(structure, media); ret != gstsdp.SDPResultOk {
			return nil, fmt.Errorf("failed to add media from structure: %v", ret)
		}
	}

	if ret := media.SetPortInfo(uint(track.rtpConn.LocalAddr().(*net.UDPAddr).Port), 1); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set port info on media: %v", ret)
	}
	if ret := media.SetProto(track.Proto); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set proto on media: %v", ret)
	}

	if track.Label != "" {
		if ret := media.AddAttribute("label", track.Label); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add label attribute to media: %v", ret))
		}
	}

	switch track.Kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE:
		if ret := media.AddAttribute("rtcp-fb", "* nack pli"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add rtcp-fb attribute to media: %v", ret))
		}
		if ret := media.AddAttribute("rtcp-fb", "* ccm fir"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add rtcp-fb attribute to media: %v", ret))
		}
	}

	switch track.Kind {
	case livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
		if ret := media.AddAttribute("content", "slides"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add content attribute to media: %v", ret))
		}
	case livekit.TrackSource_CAMERA:
		if ret := media.AddAttribute("content", "main"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add content attribute to media: %v", ret))
		}
	}

	return media, nil
}
