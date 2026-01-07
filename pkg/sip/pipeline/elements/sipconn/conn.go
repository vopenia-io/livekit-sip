package sipconn

import (
	"errors"
	"math/rand"
	"net"
	"syscall"
	"unsafe"

	"github.com/go-gst/go-glib/glib"
)

/*
#cgo pkg-config: gio-2.0
#include <gio/gio.h>

// Helper to get the GType for GSocket
static GType get_socket_type() {
    return g_socket_get_type();
}

static GSocket* create_gsocket_from_fd(int fd) {
    GError *err = NULL;
    // g_socket_new_from_fd takes ownership of the fd
    GSocket *sock = g_socket_new_from_fd(fd, &err);

    if (err != NULL) {
        g_error_free(err);
        return NULL;
    }
    return sock;
}
*/
import "C"

var ErrListenFailed = errors.New("failed to listen on udp port")

func NewUDPConnPair(portMin, portMax uint16, ip net.IP) (*net.UDPConn, *net.UDPConn, error) {
	if portMin == 0 && portMax == 0 {
		portMin = 1024
		portMax = 0xFFFF
	}

	i := portMin
	if i == 0 {
		i = 1
	}
	// Ensure we start on an even port
	if i%2 != 0 {
		i++
	}

	j := portMax
	if j == 0 {
		j = 0xFFFF
	}

	if i > j {
		return nil, nil, ErrListenFailed
	}

	// Start from a random even port
	portRange := (j - i) / 2
	if portRange <= 0 {
		portRange = 1
	}
	portStart := uint16((rand.Intn(int(portRange)) * 2)) + i
	if portStart%2 != 0 {
		portStart++
	}

	portCurrent := portStart

	for {
		// Try to allocate RTP on even port
		rtpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: int(portCurrent)})
		if err == nil {
			// Try to allocate RTCP on next port (RTP+1)
			rtcpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: int(portCurrent + 1)})
			if err == nil {
				return rtpConn, rtcpConn, nil
			}
			// Failed to allocate RTCP, close RTP and try next pair
			rtpConn.Close()
		}

		portCurrent += 2 // Move to next even port
		if portCurrent > j {
			portCurrent = i
			if portCurrent%2 != 0 {
				portCurrent++
			}
		}
		if portCurrent == portStart {
			break
		}
	}
	return nil, nil, ErrListenFailed
}

// GSocketWrapper wraps the GSocket GObject.
type GSocketWrapper struct {
	*glib.Object
}

// ToGValue implements the glib.ValueTransformer interface.
// This is called automatically by SetProperty to marshal the object.
func (s *GSocketWrapper) ToGValue() (*glib.Value, error) {
	// 1. Initialize a GValue with the specific G_TYPE_SOCKET
	// This prevents the "invalid type GObject" and "invalid type gpointer" errors.
	socketType := glib.Type(C.get_socket_type())
	val, err := glib.ValueInit(socketType)
	if err != nil {
		return nil, err
	}

	// 2. Set the object into the GValue
	val.SetInstance(s.Object.Unsafe())
	return val, nil
}

func GSocketFromUDPConn(conn *net.UDPConn) (*GSocketWrapper, error) {
	file, err := conn.File()
	if err != nil {
		return nil, err
	}
	defer file.Close()

	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		return nil, err
	}

	cSocket := C.create_gsocket_from_fd(C.int(fd))
	if cSocket == nil {
		syscall.Close(fd) // Clean up if creation failed
		return nil, errors.New("failed to create GSocket from UDPConn")
	}

	obj := glib.TransferFull(unsafe.Pointer(cSocket))
	return &GSocketWrapper{Object: obj}, nil
}
