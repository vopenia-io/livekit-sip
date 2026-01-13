package sipconn

import (
	"fmt"
	"math"
	"net"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"sipconn",
	gst.DebugColorFgGreen,
	"sipconn Element",
)

var properties = []*glib.ParamSpec{
	glib.NewUintParam(
		"port-start",
		"Port Start",
		"Starting port number for the SIP connection",
		1,
		math.MaxUint16,
		1024,
		glib.ParameterWritable,
	),

	glib.NewUintParam(
		"port-end",
		"Port End",
		"Ending port number for the SIP connection",
		2,
		math.MaxUint16,
		math.MaxUint16,
		glib.ParameterWritable,
	),

	glib.NewStringParam(
		"ip",
		"Local IP",
		"Local IP address for the SIP connection",
		&[]string{"0.0.0.0"}[0],
		glib.ParameterReadWrite,
	),

	glib.NewIntParam(
		"rtp-port",
		"RTP Port",
		"RTP port number for the SIP connection (0 mean not ready)",
		0,
		math.MaxUint16,
		0,
		glib.ParameterReadable,
	),

	glib.NewIntParam(
		"rtcp-port",
		"RTCP Port",
		"RTCP port number for the SIP connection (0 mean not ready)",
		0,
		math.MaxUint16,
		0,
		glib.ParameterReadable,
	),

	glib.NewStringParam(
		"remote-ip",
		"Remote IP",
		"Remote IP address for the SIP connection",
		nil,
		glib.ParameterReadWrite,
	),

	glib.NewIntParam(
		"remote-rtp-port",
		"Remote RTP Port",
		"Remote RTP port number for the SIP connection",
		0,
		math.MaxUint16,
		0,
		glib.ParameterReadWrite,
	),

	glib.NewIntParam(
		"remote-rtcp-port",
		"Remote RTCP Port",
		"Remote RTCP port number for the SIP connection",
		0,
		math.MaxUint16,
		0,
		glib.ParameterReadWrite,
	),

	glib.NewBoxedParam(
		"caps",
		"Caps",
		"The caps of the source stream",
		gst.TypeCaps,
		glib.ParameterReadWrite,
	),
}

type Addr struct {
	IP   net.IP
	RTP  uint16
	RTCP uint16
}

type props struct {
	portStart uint16
	portEnd   uint16

	local  Addr
	remote Addr

	caps *gst.Caps
}

type sipconn struct {
	err error // irrecoverable error during initialization

	props props

	rtpconn  *net.UDPConn
	rtcpconn *net.UDPConn

	RtpSrc  *gst.Element
	RtcpSrc *gst.Element

	RtpFilter  *gst.Element
	RtpSink    *gst.Element
	RtcpFilter *gst.Element
	RtcpSink   *gst.Element
}

func (*sipconn) New() glib.GoObjectSubclass {
	sr := &sipconn{
		props: props{
			portStart: 1024,
			portEnd:   math.MaxUint16,
			local: Addr{
				IP:   net.IPv4zero,
				RTP:  0,
				RTCP: 0,
			},
			remote: Addr{
				IP:   net.IPv4zero,
				RTP:  0,
				RTCP: 0,
			},
			caps: gst.NewCapsFromString("application/x-rtp"),
		},
	}
	return sr
}

func (*sipconn) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"SIP Connection Element",
		"Source/Sink",
		"Wrapper element to handle SIP RTP/RTCP connections",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	// Src pad template: ANY caps because we don't know what the reader contains
	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp")))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp")))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_rtcp",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtcp")))

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_rtcp",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtcp")))

	CAT.Log(gst.LevelDebug, "Installing properties")
	class.InstallProperties(properties)
}

func (s *sipconn) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	class := gst.ToElementClass(self.Class())
	self.Log(CAT, gst.LevelDebug, "InstanceInit")

	var err error
	defer func() { s.err = err }()
	s.RtpSrc, err = gst.NewElementWithProperties("udpsrc", map[string]interface{}{
		"address": s.props.local.IP.String(),
		"caps":    s.props.caps.Copy(),
		// "format":  gst.FormatTime,
	})
	if err != nil {
		self.Error("Failed to create RTP udpsrc element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create RTP udpsrc element: %v", err))
		return
	}
	s.RtcpSrc, err = gst.NewElementWithProperties("udpsrc", map[string]interface{}{
		"address": s.props.local.IP.String(),
		"caps":    gst.NewCapsFromString("application/x-rtcp"),
		// "format":  gst.FormatTime,
	})
	if err != nil {
		self.Error("Failed to create RTCP udpsrc element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create RTCP udpsrc element: %v", err))
		return
	}
	s.RtpFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": s.props.caps.Copy(),
	})
	if err != nil {
		self.Error("Failed to create RTP capsfilter element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create RTP capsfilter element: %v", err))
		return
	}
	s.RtpSink, err = gst.NewElementWithProperties("udpsink", map[string]interface{}{
		"async": false,
		"sync":  false,
		// "caps":  s.props.caps.Ref(),
	})
	if err != nil {
		self.Error("Failed to create RTP udpsink element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create RTP udpsink element: %v", err))
		return
	}
	s.RtcpFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		self.Error("Failed to create RTCP capsfilter element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create RTCP capsfilter element: %v", err))
		return
	}
	s.RtcpSink, err = gst.NewElementWithProperties("udpsink", map[string]interface{}{
		"async": false,
		"sync":  false,
		// "caps":  gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		self.Error("Failed to create RTCP udpsink element", err)
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create RTCP udpsink element: %v", err))
		return
	}

	if err := self.AddMany(s.RtpSrc, s.RtcpSrc, s.RtpFilter, s.RtpSink, s.RtcpFilter, s.RtcpSink); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding elements to bin: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error adding elements to bin", err.Error())
		return
	}

	if err := s.RtpFilter.Link(s.RtpSink); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking RTP elements: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error linking RTP elements", err.Error())
		return
	}
	if err := s.RtcpFilter.Link(s.RtcpSink); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking RTCP elements: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error linking RTCP elements", err.Error())
		return
	}

	gsrc := gst.NewGhostPadFromTemplate("src", s.RtpSrc.GetStaticPad("src"), class.GetPadTemplate("src"))
	self.AddPad(gsrc.Pad)

	gsrcRtcp := gst.NewGhostPadFromTemplate("src_rtcp", s.RtcpSrc.GetStaticPad("src"), class.GetPadTemplate("src_rtcp"))
	self.AddPad(gsrcRtcp.Pad)

	gsink := gst.NewGhostPadFromTemplate("sink", s.RtpFilter.GetStaticPad("sink"), class.GetPadTemplate("sink"))
	self.AddPad(gsink.Pad)

	gsinkRtcp := gst.NewGhostPadFromTemplate("sink_rtcp", s.RtcpFilter.GetStaticPad("sink"), class.GetPadTemplate("sink_rtcp"))
	self.AddPad(gsinkRtcp.Pad)
}

func (s *sipconn) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "port-start":
		gv, _ := value.GoValue()
		val, _ := gv.(uint)
		s.props.portStart = uint16(val)
	case "port-end":
		gv, _ := value.GoValue()
		val, _ := gv.(uint)
		s.props.portEnd = uint16(val)
	case "ip":
		gv, _ := value.GoValue()
		val, _ := gv.(string)
		ip := net.ParseIP(val)
		if ip == nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid IP address: %s", val))
			return
		}
		s.props.local.IP = ip
		if err := s.RtpSrc.SetProperty("address", s.props.local.IP.String()); err != nil {
			self.Error("Error setting RTP source address", err)
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting RTP source address: %v", err))
			return
		}
		if err := s.RtcpSrc.SetProperty("address", s.props.local.IP.String()); err != nil {
			self.Error("Error setting RTCP source address", err)
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting RTCP source address: %v", err))
			return
		}
	case "remote-ip":
		gv, _ := value.GoValue()
		val, _ := gv.(string)
		if val == "" {
			s.props.remote = Addr{}
			return
		}
		ip := net.ParseIP(val)
		if ip == nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid remote IP address: %s", val))
			return
		}
		s.props.remote.IP = ip
		if err := s.RtpSink.SetProperty("host", s.props.remote.IP.String()); err != nil {
			self.Error("Error setting RTP sink host", err)
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting RTP sink host: %v", err))
			return
		}
		if err := s.RtcpSink.SetProperty("host", s.props.remote.IP.String()); err != nil {
			self.Error("Error setting RTCP sink host", err)
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting RTCP sink host: %v", err))
			return
		}
	case "remote-rtp-port":
		gv, _ := value.GoValue()
		val, _ := gv.(int)
		s.props.remote.RTP = uint16(val)
		if err := s.RtpSink.SetProperty("port", int(s.props.remote.RTP)); err != nil {
			self.Error("Error setting RTP sink port", err)
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting RTP sink port: %v", err))
			return
		}
	case "remote-rtcp-port":
		gv, _ := value.GoValue()
		val, _ := gv.(int)
		s.props.remote.RTCP = uint16(val)
		if err := s.RtcpSink.SetProperty("port", int(s.props.remote.RTCP)); err != nil {
			self.Error("Error setting RTCP sink port", err)
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting RTCP sink port: %v", err))
			return
		}
	case "caps":
		val, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting caps property value: %v", err))
			return
		}
		caps, ok := val.(*gst.Caps)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for caps property")
			return
		}
		if caps == nil {
			self.Log(CAT, gst.LevelError, "Nil caps provided")
			return
		}
		s.props.caps = caps
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Element caps set to: %v", caps))
		s.RtpSrc.SetProperty("caps", s.props.caps.Copy())
		s.RtpFilter.SetProperty("caps", s.props.caps.Copy())
	}
}

func (s *sipconn) GetProperty(instance *glib.Object, id uint) *glib.Value {
	// self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "ip":
		v, _ := glib.GValue(s.props.local.IP.String())
		return v
	case "rtp-port":
		v, _ := glib.GValue(int(s.props.local.RTP))
		return v
	case "rtcp-port":
		v, _ := glib.GValue(int(s.props.local.RTCP))
		return v
	case "remote-ip":
		v, _ := glib.GValue(s.props.remote.IP.String())
		return v
	case "remote-rtp-port":
		v, _ := glib.GValue(int(s.props.remote.RTP))
		return v
	case "remote-rtcp-port":
		v, _ := glib.GValue(int(s.props.remote.RTCP))
		return v
	case "caps":
		if s.props.caps != nil {
			v, _ := glib.GValue(s.props.caps.Copy())
			return v
		}
		v, _ := glib.GValue(gst.NewAnyCaps())
		return v
	}
	return nil
}

func (s *sipconn) open(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "open")

	var err error
	s.rtpconn, s.rtcpconn, err = NewUDPConnPair(s.props.portStart, s.props.portEnd, s.props.local.IP)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating UDP connection pair: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error creating UDP connection pair", err.Error())
		return gst.StateChangeFailure
	}

	rtpAddr := s.rtpconn.LocalAddr().(*net.UDPAddr)
	rtcpAddr := s.rtcpconn.LocalAddr().(*net.UDPAddr)

	s.props.local.RTP = uint16(rtpAddr.Port)
	s.props.local.RTCP = uint16(rtcpAddr.Port)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("RTP listening on %s", s.rtpconn.LocalAddr().String()))
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("RTCP listening on %s", s.rtcpconn.LocalAddr().String()))

	gRtpSock, err := GSocketFromUDPConn(s.rtpconn)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating GSocket from RTP UDPConn: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error creating GSocket from RTP UDPConn", err.Error())
		return gst.StateChangeFailure
	}

	gRtcpSock, err := GSocketFromUDPConn(s.rtcpconn)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating GSocket from RTCP UDPConn: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error creating GSocket from RTCP UDPConn", err.Error())
		return gst.StateChangeFailure
	}

	if err := s.RtpSrc.SetProperty("socket", gRtpSock); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting socket property on RTP source: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error setting socket property on RTP source", err.Error())
		return gst.StateChangeFailure
	}

	if err := s.RtcpSrc.SetProperty("socket", gRtcpSock); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting socket property on RTCP source: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error setting socket property on RTCP source", err.Error())
		return gst.StateChangeFailure
	}

	return gst.StateChangeSuccess
}

func (s *sipconn) close(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "close")

	if s.rtpconn != nil {
		if err := s.rtpconn.Close(); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error closing RTP UDP connection: %v", err))
			self.Error("Error closing RTP UDP connection", err)
		}
		s.rtpconn = nil
	}
	if s.rtcpconn != nil {
		if err := s.rtcpconn.Close(); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error closing RTCP UDP connection: %v", err))
			self.Error("Error closing RTCP UDP connection", err)
		}
		s.rtcpconn = nil
	}
	return gst.StateChangeSuccess
}

func (s *sipconn) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)
	if s.err != nil {
		if transition == gst.StateChangeReadyToNull {
			return self.ParentChangeState(transition)
		}
		return gst.StateChangeFailure
	}
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("ChangeState: %v", transition))

	switch transition {
	case gst.StateChangeNullToReady:
		return s.open(self)
		// if ret := s.open(self); ret != gst.StateChangeSuccess {
		// 	return ret
		// }
	}

	ret := self.ParentChangeState(transition)
	if ret == gst.StateChangeFailure {
		return ret
	}

	switch transition {
	case gst.StateChangeReadyToNull:
		return s.close(self)
	}

	return ret
}

// func (s *sipconn) close(self *gst.Element) {
// 	self.Log(CAT, gst.LevelDebug, "Closing UDP connections")

// 	if err := s.rtpconn.Close(); err != nil {
// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error closing RTP UDP connection: %v", err))
// 		self.Error("Error closing RTP UDP connection", err)
// 	}
// 	if err := s.rtcpconn.Close(); err != nil {
// 		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error closing RTCP UDP connection: %v", err))
// 		self.Error("Error closing RTCP UDP connection", err)
// 	}
// }
