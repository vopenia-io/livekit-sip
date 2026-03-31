package sipbin

import (
	"fmt"
	"strconv"

	"github.com/go-gst/go-gst/gst/gstsdp"
	"github.com/livekit/protocol/livekit"
)

func (e *SipBin) makeBfcpMedia(bfcp *BfcpTrack) (*gstsdp.Media, error) {
	media, err := gstsdp.NewMedia()
	if err != nil {
		return nil, fmt.Errorf("failed to create SDP media: %w", err)
	}

	if ret := media.SetMedia("application"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set media type on BFCP media: %v", ret)
	}

	portVal, err := bfcp.BfcpServer.GetProperty("port")
	if err != nil {
		return nil, fmt.Errorf("failed to get port property from BFCP server: %w", err)
	}
	port, ok := portVal.(uint)
	if !ok {
		return nil, fmt.Errorf("invalid type for port property from BFCP server")
	}

	// floorIDVal, err := bfcp.BfcpServer.GetProperty("floor-id")
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to get floor-id property from BFCP server: %w", err)
	// }
	// floorID, ok := floorIDVal.(uint)
	// if !ok {
	// 	return nil, fmt.Errorf("invalid type for floor-id property from BFCP server")
	// }

	if ret := media.SetPortInfo(port, 1); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set port info on BFCP media: %v", ret)
	}
	if ret := media.SetProto(bfcp.Proto); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set proto on BFCP media: %v", ret)
	}
	if ret := media.AddFormat("*"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add format to BFCP media: %v", ret)
	}

	if ret := media.AddAttribute("floorctrl", "s-only"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add floorctrl attribute to BFCP media: %v", ret)
	}
	if ret := media.AddAttribute("bfcpver", strconv.Itoa(bfcp.BfcpVersion)); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add bfcpver attribute to BFCP media: %v", ret)
	}
	if ret := media.AddAttribute("confid", strconv.FormatUint(uint64(bfcp.ConfID), 10)); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add confid attribute to BFCP media: %v", ret)
	}
	if ret := media.AddAttribute("userid", strconv.FormatUint(uint64(bfcp.UserID), 10)); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add userid attribute to BFCP media: %v", ret)
	}

	if ret := media.AddAttribute("setup", "actpass"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add setup attribute to BFCP media: %v", ret)
	}
	if ret := media.AddAttribute("connection", "new"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add connection attribute to BFCP media: %v", ret)
	}

	return media, nil
}

func (e *SipBin) bfcpMediaAddStreams(medias []*gstsdp.Media) error {
	if e.Bfcp == nil || e.Bfcp.Idx >= len(medias) || medias[e.Bfcp.Idx] == nil {
		return nil
	}

	screenshare := e.Tracks[livekit.TrackSource_SCREEN_SHARE]
	if screenshare != nil && screenshare.Idx < len(medias) && medias[screenshare.Idx] != nil {
		label := strconv.Itoa(screenshare.Idx)
		if screenshare.Label != "" {
			label = screenshare.Label
		}
		if err := e.mediaAddBfcpLabel(medias[e.Bfcp.Idx], medias[screenshare.Idx], label); err != nil {
			return fmt.Errorf("failed to add BFCP label for screenshare: %w", err)
		}
	}

	return nil
}

func (e *SipBin) mediaAddBfcpLabel(bfcpMedia *gstsdp.Media, media *gstsdp.Media, label string) error {
	floorID := fmt.Sprintf("%d mstrm:%s", e.Bfcp.FloorID, label)
	if ret := bfcpMedia.AddAttribute("floorid", floorID); ret != gstsdp.SDPResultOk {
		return fmt.Errorf("failed to add floorid attribute to BFCP media: %v", ret)
	}
	if ret := media.AddAttribute("label", label); ret != gstsdp.SDPResultOk {
		return fmt.Errorf("failed to add label attribute to media: %v", ret)
	}
	return nil
}
