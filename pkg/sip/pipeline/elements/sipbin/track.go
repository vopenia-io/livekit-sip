package sipbin

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/gstsdp"
	"github.com/livekit/protocol/livekit"
)

type BfcpTrack struct {
	initialized bool
	Idx         int
	Proto       string
	BfcpServer  *gst.Element
	BfcpVersion int
	ConfID      uint32
	UserID      uint16
	FloorID     uint16
}

type SipTrack struct {
	initialized bool
	Idx         int
	Kind        livekit.TrackSource
	Proto       string
	Label       string
	Caps        *gst.Caps
	rtpConn     *net.UDPConn
	rtcpConn    *net.UDPConn
	RtpSrc      *gst.Element
	RtcpSrc     *gst.Element
	RtpSink     *gst.Element
	RtcpSink    *gst.Element
	RtpFilter   *gst.Element
}

func (e *SipBin) NewTrack(self *gst.Bin, idx int, kind livekit.TrackSource, proto string) (*SipTrack, error) {
	ip := e.bindIP
	if ip == nil {
		ip = e.ip
	}
	if ip == nil {
		return nil, fmt.Errorf("no IP address configured for SIP media")
	}

	if proto == "" {
		proto = "RTP/AVP"
	}

	rtpConn, rtcpConn, err := NewUDPConnPair(e.portStart, e.portEnd, ip)
	if err != nil {
		var fallbackErr error
		rtpConn, rtcpConn, fallbackErr = NewUDPConnPair(e.portStart, e.portEnd, net.IPv4zero)
		if fallbackErr != nil {
			return nil, fmt.Errorf("failed to create UDP connections for SIP media: %w", err)
		}
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to create UDP connections for SIP media: %v, but fallback succeeded: %v", err, fallbackErr))
		ip = net.IPv4zero
	}

	grtpSocket, err := GSocketFromUDPConn(rtpConn)
	if err != nil {
		return nil, fmt.Errorf("failed to create GSocket from RTP UDP connection: %w", err)
	}
	grtcpSocket, err := GSocketFromUDPConn(rtcpConn)
	if err != nil {
		return nil, fmt.Errorf("failed to create GSocket from RTCP UDP connection: %w", err)
	}

	rtpSrc, err := gst.NewElementWithProperties("udpsrc", map[string]interface{}{
		"socket":       grtpSocket,
		"close-socket": false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create RTP source element: %w", err)
	}

	rtcpSrc, err := gst.NewElementWithProperties("udpsrc", map[string]interface{}{
		"socket":       grtcpSocket,
		"close-socket": false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create RTCP source element: %w", err)
	}

	rtpSink, err := gst.NewElementWithProperties("udpsink", map[string]interface{}{
		"socket":       grtpSocket,
		"close-socket": false,
		"clients":      "",
		"async":        false,
		"sync":         false,
		"qos":          false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create RTP sink element: %w", err)
	}

	rtcpSink, err := gst.NewElementWithProperties("udpsink", map[string]interface{}{
		"socket":       grtcpSocket,
		"close-socket": false,
		"clients":      "",
		"async":        false,
		"sync":         false,
		"qos":          false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create RTCP sink element: %w", err)
	}

	rtpFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create RTP filter element: %w", err)
	}

	// rtpCut, err := gst.NewElementWithProperties("media-cut", map[string]interface{}{})
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create RTP cut element: %w", err)
	// }

	if err := self.AddMany(rtpSrc, rtcpSrc, rtpSink, rtcpSink, rtpFilter); err != nil {
		return nil, fmt.Errorf("failed to add track elements to bin: %w", err)
	}

	return &SipTrack{
		initialized: false,
		Idx:         idx,
		Kind:        kind,
		Proto:       proto,
		rtpConn:     rtpConn,
		rtcpConn:    rtcpConn,
		RtpSrc:      rtpSrc,
		RtcpSrc:     rtcpSrc,
		RtpSink:     rtpSink,
		RtcpSink:    rtcpSink,
		RtpFilter:   rtpFilter,
		// RtpCut:      rtpCut,
	}, nil
}

func (t *SipTrack) Init(e *SipBin, self *gst.Bin, media *gstsdp.Media, session *gstsdp.Message, caps *gst.Caps) error {
	if t.initialized {
		return nil
	}

	var conn *gstsdp.Connection
	if media.ConnectionsLen() > 0 {
		conn = media.GetConnection(0)
	} else {
		conn = session.GetConnection()
	}
	if conn == nil {
		return fmt.Errorf("no connection information found in SDP for media index %d", t.Idx)
	}

	if label := media.GetAttributeVal("label"); label != "" {
		t.Label = label
	}

	t.Caps = caps

	rtcpPort := media.GetPort() + 1
	rtcpAttr := media.GetAttributeVal("rtcp")
	if rtcpAttr != "" {
		if p, err := strconv.Atoi(rtcpAttr); err == nil {
			rtcpPort = uint(p)
		} else {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to parse RTCP port from media attribute: %v", err))
		}
	}

	if err := errors.Join(
		t.RtpSink.SetProperty("host", conn.Address()),
		t.RtpSink.SetProperty("port", int(media.GetPort())),
		t.RtcpSink.SetProperty("host", conn.Address()),
		t.RtcpSink.SetProperty("port", int(rtcpPort)),
		t.RtpFilter.SetProperty("caps", caps),
	); err != nil {
		return fmt.Errorf("failed to set properties on track elements: %w", err)
	}

	sendRtpSink := e.RtpBin.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d", t.Kind))
	if sendRtpSink == nil {
		return fmt.Errorf("failed to get request pad for RTP sink")
	}
	if ret := t.RtpSrc.GetStaticPad("src").Link(sendRtpSink); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link RTP source to RTP sink: %v", ret)
	}

	sendRtcpSink := e.RtpBin.GetRequestPad(fmt.Sprintf("recv_rtcp_sink_%d", t.Kind))
	if sendRtcpSink == nil {
		return fmt.Errorf("failed to get request pad for RTCP sink")
	}
	if ret := t.RtcpSrc.GetStaticPad("src").Link(sendRtcpSink); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link RTCP source to RTCP sink: %v", ret)
	}

	// recvRtpSrc := e.RtpBin.GetRequestPad(fmt.Sprintf("send_rtp_sink_%d", t.Kind))
	// if recvRtpSrc == nil {
	// 	return fmt.Errorf("failed to get request pad for RTP source")
	// }

	// if ret := t.RtpFilter.GetStaticPad("src").Link(recvRtpSrc); ret != gst.PadLinkOK {
	// 	return fmt.Errorf("failed to link RTP filter to RTP source: %v", ret)
	// }

	var errs []error
	for _, elem := range [](*gst.Element){t.RtpSrc, t.RtcpSrc, t.RtpSink, t.RtcpSink, t.RtpFilter} {
		if !elem.SyncStateWithParent() {
			errs = append(errs, fmt.Errorf("failed to sync state of element %s with parent", elem.GetName()))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to start track: %v", errs)
	}

	t.initialized = true

	return nil
}

func (e *SipBin) CleanupTrack(self *gst.Bin, track *SipTrack) error {
	var errs []error
	if track.initialized {
		for _, elem := range [](*gst.Element){track.RtpSrc, track.RtcpSrc, track.RtpSink, track.RtcpSink, track.RtpFilter} {
			if err := elem.SetState(gst.StateNull); err != nil {
				errs = append(errs, fmt.Errorf("failed to set state of element %s to null: %w", elem.GetName(), err))
			}

			if err := self.Remove(elem); err != nil {
				errs = append(errs, fmt.Errorf("failed to remove element %s from bin: %w", elem.GetName(), err))
			}
		}
		sendRtpSink := e.RtpBin.GetStaticPad(fmt.Sprintf("recv_rtp_sink_%d", track.Kind))
		if sendRtpSink != nil {
			e.RtpBin.ReleaseRequestPad(sendRtpSink)
		}
		sendRtcpSink := e.RtpBin.GetStaticPad(fmt.Sprintf("recv_rtcp_sink_%d", track.Kind))
		if sendRtcpSink != nil {
			e.RtpBin.ReleaseRequestPad(sendRtcpSink)
		}
		recvRtpSrc := e.RtpBin.GetStaticPad(fmt.Sprintf("send_rtp_sink_%d", track.Kind))
		if recvRtpSrc != nil {
			e.RtpBin.ReleaseRequestPad(recvRtpSrc)
		}
	}
	if track.rtpConn != nil {
		if err := track.rtpConn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close RTP UDP connection: %w", err))
		}
	}
	if track.rtcpConn != nil {
		if err := track.rtcpConn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close RTCP UDP connection: %w", err))
		}
	}

	e.Tracks[track.Kind] = nil
	e.PtMap[track.Kind] = make(map[uint8]*gst.Caps)

	if len(errs) > 0 {
		return fmt.Errorf("failed to cleanup track: %v", errs)
	}

	track.initialized = false

	return nil
}

func (e *SipBin) NewBfcpTrack(self *gst.Bin, idx int, proto string) (*BfcpTrack, error) {
	ip := e.bindIP
	if ip == nil {
		ip = e.ip
	}
	if ip == nil {
		return nil, fmt.Errorf("no IP address configured for BFCP media")
	}
	props := map[string]interface{}{
		"bind-ip": ip.String(),
	}
	if e.portStart != 0 {
		props["port-start"] = uint(e.portStart)
	}
	if e.portEnd != 0 {
		props["port-end"] = uint(e.portEnd)
	}
	bfcpServer, err := gst.NewElementWithProperties("bfcpserver", props)
	if err != nil {
		return nil, fmt.Errorf("failed to create BFCP server element: %w", err)
	}

	wself := glib.WeakRefInit(self)
	eweak := weak.Make(e)
	if _, err := bfcpServer.Connect("on-floor-released", func(instance *gst.Element, floorID, userID int) {
		self := gst.ToGstBin(wself.Get())
		e := eweak.Value()
		if self == nil || self.Instance() == nil || e == nil {
			return
		}
		e.bfcpClearScreenshare(self)
	}); err != nil {
		return nil, fmt.Errorf("failed to connect on-floor-released signal: %w", err)
	}

	return &BfcpTrack{
		Idx:         idx,
		Proto:       proto,
		BfcpServer:  bfcpServer,
		BfcpVersion: 2,
		ConfID:      1,
		UserID:      1,
		FloorID:     1,
	}, nil
}

func (b *BfcpTrack) Init(e *SipBin, self *gst.Bin, media *gstsdp.Media, session *gstsdp.Message) error {
	if b.initialized {
		return nil
	}

	// if err := b.BfcpServer.SetProperty("floor-id", uint(b.FloorID)); err != nil {
	// 	return fmt.Errorf("failed to set floor-id property on BFCP server: %w", err)
	// }

	if version := media.GetAttributeVal("bfcpver"); version != "" {
		if v, err := strconv.Atoi(version); err == nil {
			b.BfcpVersion = v
		} else {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to parse BFCP version from media attribute: %v", err))
		}
	}

	if err := self.Add(b.BfcpServer); err != nil {
		return fmt.Errorf("failed to add BFCP server element to bin: %w", err)
	}

	if !b.BfcpServer.SyncStateWithParent() {
		return fmt.Errorf("failed to sync state of BFCP server element with parent")
	}

	b.initialized = true
	return nil
}
