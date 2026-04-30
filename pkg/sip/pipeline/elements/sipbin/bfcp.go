package sipbin

import (
	"fmt"
	"strconv"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
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

func (e *SipBin) bfcpStartScreenshare(self *gst.Bin) {
	if e.Bfcp == nil {
		return
	}

	if _, err := e.Bfcp.BfcpServer.Emit("start-screenshare", int(e.Bfcp.FloorID)); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit start-screenshare signal: %v", err))
		self.Error("Failed to emit start-screenshare signal", err)
	} else {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Emitted start-screenshare signal for floor ID %d", e.Bfcp.FloorID))
	}
}

func (e *SipBin) bfcpStopScreenshare(self *gst.Bin) {
	if e.Bfcp == nil {
		return
	}

	if _, err := e.Bfcp.BfcpServer.Emit("stop-screenshare", int(e.Bfcp.FloorID)); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit stop-screenshare signal: %v", err))
		self.Error("Failed to emit stop-screenshare signal", err)
	} else {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Emitted stop-screenshare signal for floor ID %d", e.Bfcp.FloorID))
	}
}

func (e *SipBin) clearTrack(self *gst.Bin, kind livekit.TrackSource) {
	if e.Tracks[kind] == nil {
		return
	}

	time.Sleep(1 * time.Second)
	rtpSessionVal, err := e.RtpBin.Emit("get-internal-session", uint(kind))
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get internal session for track source %s: %v", kind, err))
		self.Error("Failed to get internal session for track source", err)
		return
	}
	rtpSession, ok := rtpSessionVal.(*glib.Object)
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert internal session to element for track source %s", kind))
		self.Error("Failed to convert internal session to element for track source", fmt.Errorf("invalid RTP session element"))
		return
	}

	sourcesVal, err := rtpSession.GetProperty("sources")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sources property from RTP session: %v", err))
		self.Error("Failed to get sources property from RTP session", err)
		return
	}
	sources, ok := sourcesVal.(*glib.ValueArray)
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert sources property to value array for track source %s", kind))
		self.Error("Failed to convert sources property to value array for track source", fmt.Errorf("invalid sources property"))
		return
	}
	ssrcs := make([]uint32, 0, sources.Len())
	nptk := make([]uint64, 0, sources.Len())
	for i := range sources.Len() {
		rtpSourceVal, err := sources.Index(i)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get source at index %d from sources array for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get source at index %d from sources array for track source %s", i, kind), err)
			continue
		}
		rtpSource, ok := rtpSourceVal.(*glib.Object)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert source at index %d to element for track source %s", i, kind))
			self.Error(fmt.Sprintf("Failed to convert source at index %d to element for track source %s", i, kind), fmt.Errorf("invalid RTP source element"))
			continue
		}

		statsVal, err := rtpSource.GetProperty("stats")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get stats property from RTP source at index %d for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get stats property from RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		stats, ok := statsVal.(*gst.Structure)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert stats property to structure for RTP source at index %d for track source %s", i, kind))
			self.Error(fmt.Sprintf("Failed to convert stats property to structure for RTP source at index %d for track source %s", i, kind), fmt.Errorf("invalid stats property"))
			continue
		}
		internal, err := stats.GetBool("internal")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get internal field from stats for RTP source at index %d for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get internal field from stats for RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		if internal {
			continue
		}
		validated, err := stats.GetBool("validated")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get validated field from stats for RTP source at index %d for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get validated field from stats for RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		if !validated {
			continue
		}

		ssrcVal, err := rtpSource.GetProperty("ssrc")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get ssrc property from RTP source at index %d for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get ssrc property from RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		ssrc, ok := ssrcVal.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert ssrc property to uint for RTP source at index %d for track source %s", i, kind))
			self.Error(fmt.Sprintf("Failed to convert ssrc property to uint for RTP source at index %d for track source %s", i, kind), fmt.Errorf("invalid ssrc property"))
			continue
		}

		packetsReceived, err := stats.GetUint64("packets-received")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get packets-received field from stats for RTP source at index %d for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get packets-received field from stats for RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		ssrcs = append(ssrcs, uint32(ssrc))
		nptk = append(nptk, packetsReceived)
	}
	time.Sleep(1 * time.Second)
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Clearing %d SSRCs from RTP session for track source %s: %v", len(ssrcs), kind, ssrcs))
	for i, ssrc := range ssrcs {
		rtpSourceVal, err := rtpSession.Emit("get-source-by-ssrc", uint(ssrc))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get source by SSRC %d from RTP session: %v", ssrc, err))
			self.Error(fmt.Sprintf("Failed to get source by SSRC %d from RTP session", ssrc), err)
			continue
		}
		rtpSource, ok := rtpSourceVal.(*glib.Object)
		if !ok || rtpSource == nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No source found for SSRC %d in RTP session", ssrc))
			continue
		}

		statsVal, err := rtpSource.GetProperty("stats")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get stats property from RTP source for SSRC %d: %v", ssrc, err))
			self.Error(fmt.Sprintf("Failed to get stats property from RTP source for SSRC %d", ssrc), err)
			continue
		}
		stats, ok := statsVal.(*gst.Structure)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert stats property to structure for RTP source for SSRC %d", ssrc))
			self.Error(fmt.Sprintf("Failed to convert stats property to structure for RTP source for SSRC %d", ssrc), fmt.Errorf("invalid stats property"))
			continue
		}
		packetsReceived, err := stats.GetUint64("packets-received")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get packets-received field from stats for RTP source for SSRC %d: %v", ssrc, err))
			self.Error(fmt.Sprintf("Failed to get packets-received field from stats for RTP source for SSRC %d", ssrc), err)
			continue
		}

		if packetsReceived > nptk[i] {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Source for SSRC %d is still receiving packets (received %d, previously received %d), skipping clear", ssrc, packetsReceived, nptk[i]))
			continue
		}

		if _, err := e.RtpBin.Emit("clear-ssrc", uint(kind), ssrc); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to clear ssrc %d from rtpbin for track source %s: %v", ssrc, kind, err))
			self.Error(fmt.Sprintf("Failed to clear ssrc %d from rtpbin for track source %s", ssrc, kind), err)
		}
	}
}
