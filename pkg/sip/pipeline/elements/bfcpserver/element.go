package bfcpserver

import (
	"fmt"
	"net"
	"strconv"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/vopenia-io/bfcp"
)

var CAT = gst.NewDebugCategory(
	"bfcpserver",
	gst.DebugColorBgGreen,
	"bfcpserver Element",
)

type BFCPServer struct {
	props
	bfcpServer *bfcp.Server
	bfcpConfig *bfcp.ServerConfig
	started    bool
}

func (e *BFCPServer) New() glib.GoObjectSubclass {
	return &BFCPServer{}
}

func (e *BFCPServer) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"BFCPServer",
		"Generic",
		"BFCPServer Element",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	gst.SignalNew(
		class.Type(),
		"on-floor-granted",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_INT,
		glib.TYPE_INT,
		glib.TYPE_INT,
	)

	gst.SignalNew(
		class.Type(),
		"on-floor-released",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_INT,
		glib.TYPE_INT,
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"noop",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewAnyCaps(),
	))

	class.InstallProperties(properties)
}

func (e *BFCPServer) InstanceInit(instance *glib.Object) {
	self := gst.ToElement(instance)

	e.props.floorID = 1

	class := gst.ToElementClass(self.Class())

	self.AddPad(gst.NewGhostPadNoTargetFromTemplate("noop", class.GetPadTemplate("noop")).Pad)
}

func (e *BFCPServer) Constructed(instance *glib.Object) {
	self := gst.ToElement(instance)

	addr := ":0"
	if e.bindIP != nil {
		addr = e.bindIP.String() + addr
	}

	config := bfcp.DefaultServerConfig(addr, 1)
	config.AutoGrant = true
	if e.portStart != 0 {
		config.PortMin = int(e.portStart)
	}
	if e.portEnd != 0 {
		config.PortMax = int(e.portEnd)
	}

	e.bfcpConfig = config
	e.bfcpServer = bfcp.NewServer(config)

	if err := e.bfcpServer.Listen(); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to start BFCP server: %v", err))
		self.Error("Failed to start BFCP server", err)
		return
	}

	host, portStr, err := net.SplitHostPort(e.bfcpServer.Addr().String())
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to split host and port: %v", err))
		self.Error("Failed to start BFCP server", err)
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse port: %v", err))
		self.Error("Failed to start BFCP server", err)
		return
	}

	if port <= 0 || port > 0xFFFF {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid port number: %d", port))
		self.Error("Failed to start BFCP server", fmt.Errorf("invalid port number: %d", port))
		return
	}

	e.port = uint16(port)

	e.SetupSignals(self)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("BFCP server started on %s:%d", host, port))
}

func (e *BFCPServer) ChangeState(self *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	if transition == gst.StateChangeReadyToPaused && !e.started {
		e.bfcpServer.Serve()
		e.started = true
	}

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeNullToReady {
		e.bfcpServer.CreateFloor(e.floorID)
	}

	if transition == gst.StateChangeReadyToNull {
		if err := e.bfcpServer.Close(); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to close BFCP server: %v", err))
		}
	}
	return ret
}
