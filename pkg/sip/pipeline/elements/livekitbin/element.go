package livekitbin

import (
	"fmt"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/tracks"
)

var CAT = gst.NewDebugCategory(
	"livekitbin",
	gst.DebugColorFgGreen,
	"livekitbin Element",
)

func init() {
	tracks.CAT = CAT
}

type config struct {
	wsURL                        string
	token                        string
	defaultParticipantIdentity   string
	defaultParticipantName       string
	defaultParticipantAttributes map[string]string
}

type LivekitBin struct {
	self                 *glib.WeakRef
	RtpBin               *gst.Element
	RtcpFunnel           *gst.Element
	RtcpSink             *gst.Element
	MicrophoneRtpFunnel  *gst.Element
	MicrophoneRtcpFunnel *gst.Element
	CameraRtpFunnel      *gst.Element
	CameraRtcpFunnel     *gst.Element
	// TODO: support screenshare

	state
	config
	room *lksdk.Room
	// callbackMu sync.Mutex

	encodingPT map[uint8]string
	encodingMu sync.RWMutex
}

func (e *LivekitBin) New() glib.GoObjectSubclass {
	return &LivekitBin{}
}

// ClassInit implements [glib.GoObjectSubclass].
func (e *LivekitBin) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"LiveKit Room",
		"Source/Sink",
		"Element to connect to a LiveKit room",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	// signals
	gst.SignalNew(
		class.Type(),
		"closed",
		gst.SignalRunLast,
		glib.TYPE_NONE,
	)

	gst.SignalNew(
		class.Type(),
		"connected",
		gst.SignalRunLast,
		glib.TYPE_NONE,
	)

	// action signals
	gst.SignalNew(
		class.Type(),
		"connect",
		gst.SignalRunLast,
		glib.TYPE_NONE,
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"recv_rtp_src_%u_%u_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewCapsFromString("application/x-rtp"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"send_rtp_sink_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewCapsFromString("application/x-rtp"),
	))

	class.InstallProperties(properties)
}

func (e *LivekitBin) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	eweak := weak.Make(e)

	e.state.cond = sync.NewCond(&e.state.mu)
	e.encodingPT = make(map[uint8]string)

	e.self = glib.WeakRefInit(self)

	var err error
	e.RtpBin, err = gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"rtp-profile": int(3), // GST_RTP_PROFILE_AVPF
		"autoremove":  true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating rtpbin: %v", err))
		self.Error("Error creating rtpbin", err)
		return
	}
	if _, err := e.RtpBin.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		ptr := eweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "LivekitBin instance is nil in rtpbin pad-added callback")
			return
		}
		ptr.OnRtpBinPadAdded(pad)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin pad-added signal: %v", err))
		self.Error("Error connecting to rtpbin pad-added signal", err)
		return
	}
	if _, err := e.RtpBin.Connect("pad-removed", func(_ *gst.Element, pad *gst.Pad) {
		ptr := eweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "LivekitBin instance is nil in rtpbin pad-removed callback")
			return
		}
		ptr.OnRtpBinPadRemoved(pad)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin pad-removed signal: %v", err))
		self.Error("Error connecting to rtpbin pad-removed signal", err)
		return
	}

	if _, err := e.RtpBin.Connect("request-pt-map", func(_ *gst.Element, session, pt uint) *gst.Caps {
		ptr := eweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "LivekitBin instance is nil in rtpbin request-pt-map callback")
			return nil
		}
		return ptr.OnRtpBinRequestPtMap(session, pt)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin request-pt-map signal: %v", err))
		self.Error("Error connecting to rtpbin request-pt-map signal", err)
		return
	}

	e.RtcpFunnel, err = gst.NewElementWithName("funnel", "livekitbin_rtcp_funnel")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating rtcp funnel: %v", err))
		self.Error("Error creating rtcp funnel", err)
		return
	}
	e.RtcpSink, err = gst.NewElement("livekitbin_sinkrtcp")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating rtcp sink element: %v", err))
		self.Error("Error creating rtcp sink element", err)
		return
	}

	e.MicrophoneRtpFunnel, err = gst.NewElementWithName("rtpfunnel", "livekitbin_microphone_rtpfunnel")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating microphone rtpfunnel: %v", err))
		self.Error("Error creating microphone rtpfunnel", err)
		return
	}
	e.MicrophoneRtcpFunnel, err = gst.NewElementWithName("funnel", "livekitbin_microphone_rtcp_funnel")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating microphone rtcp funnel: %v", err))
		self.Error("Error creating microphone rtcp funnel", err)
		return
	}
	// e.MicrophoneRtcpFunnel.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	// 	buffer := info.GetBuffer()
	// 	data := buffer.Bytes()
	// 	pkts, err := rtcp.Unmarshal(data)
	// 	if err != nil {
	// 		fmt.Printf("Failed to unmarshal RTCP packet in microphone rtcp funnel probe: %v => %x\n", err, data)
	// 		return gst.PadProbeOK
	// 	}
	// 	fmt.Printf("Received RTCP packet in microphone rtcp funnel probe: %d packets:\n", len(pkts))
	// 	for i, pkt := range pkts {
	// 		fmt.Printf("  Packet %d: %T => %+v\n", i, pkt, pkt)
	// 	}

	// 	return gst.PadProbeOK
	// })

	e.CameraRtpFunnel, err = gst.NewElementWithName("rtpfunnel", "livekitbin_camera_rtpfunnel")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating camera rtpfunnel: %v", err))
		self.Error("Error creating camera rtpfunnel", err)
		return
	}
	e.CameraRtcpFunnel, err = gst.NewElementWithName("funnel", "livekitbin_camera_rtcp_funnel")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating camera rtcp funnel: %v", err))
		self.Error("Error creating camera rtcp funnel", err)
		return
	}

	if err := self.AddMany(e.RtpBin, e.RtcpFunnel, e.RtcpSink, e.MicrophoneRtpFunnel, e.MicrophoneRtcpFunnel, e.CameraRtpFunnel, e.CameraRtcpFunnel); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding children to livekitbin: %v", err))
		self.Error("Error adding children to livekitbin", err)
		return
	}

	e.room = lksdk.NewRoom(e.callabcks())

	// action signals
	self.Connect("connect", func(instance *gst.Element) {
		ptr := eweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "LivekitBin instance is nil in connect signal callback")
			return
		}
		go ptr.OnConnectSignal(instance)
	})

	if err := e.RtcpFunnel.Link(e.RtcpSink); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking rtcp funnel to rtcp sink: %v", err))
		self.Error("Error linking rtcp funnel to rtcp sink", err)
		return
	}

	if ret := e.MicrophoneRtpFunnel.GetStaticPad("src").Link(e.RtpBin.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d", livekit.TrackSource_MICROPHONE))); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking microphone rtpfunnel to rtpbin: %v", ret))
		self.Error("Error linking microphone rtpfunnel to rtpbin", fmt.Errorf("link error: %v", ret))
		return
	}
	if ret := e.MicrophoneRtcpFunnel.GetStaticPad("src").Link(e.RtpBin.GetRequestPad(fmt.Sprintf("recv_rtcp_sink_%d", livekit.TrackSource_MICROPHONE))); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking microphone rtcpfunnel to rtpbin: %v", ret))
		self.Error("Error linking microphone rtcpfunnel to rtpbin", fmt.Errorf("link error: %v", ret))
		return
	}

	if ret := e.CameraRtpFunnel.GetStaticPad("src").Link(e.RtpBin.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d", livekit.TrackSource_CAMERA))); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking camera rtpfunnel to rtpbin: %v", ret))
		self.Error("Error linking camera rtpfunnel to rtpbin", fmt.Errorf("link error: %v", ret))
		return
	}
	if ret := e.CameraRtcpFunnel.GetStaticPad("src").Link(e.RtpBin.GetRequestPad(fmt.Sprintf("recv_rtcp_sink_%d", livekit.TrackSource_CAMERA))); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking camera rtcpfunnel to rtpbin: %v", ret))
		self.Error("Error linking camera rtcpfunnel to rtpbin", fmt.Errorf("link error: %v", ret))
		return
	}
}

func (e *LivekitBin) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)

	if transition == gst.StateChangeReadyToNull {
		e.room.Disconnect()
		e.Set(RoomStateClosed)
		e.RtpBin = nil
		e.RtcpFunnel = nil
		e.RtcpSink = nil
		e.MicrophoneRtpFunnel = nil
		e.MicrophoneRtcpFunnel = nil
		e.CameraRtpFunnel = nil
		e.CameraRtcpFunnel = nil
	}

	return ret
}

func (e *LivekitBin) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	switch templ.Name() {
	case "send_rtp_sink_%u":
		return e.ForwardPublishTrack(instance, templ, name, caps)
	}

	self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown pad template name: %s", templ.Name()))
	return nil
}
