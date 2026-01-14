package lkroom

import (
	"errors"
	"fmt"
	"io"
	"math"
	"runtime/cgo"
	"time"
	"unsafe"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

type RemoteTrackInfo struct {
	track *webrtc.TrackRemote
	pub   *lksdk.RemoteTrackPublication
	rp    *lksdk.RemoteParticipant
}

type srcTrack struct {
	parent *lkroom

	*RemoteTrackInfo
}

var src_track_properties = []*glib.ParamSpec{
	glib.NewUint64Param(
		"parent",
		"Parent Handle",
		"cgo.Handle (uintptr) to a the lkroom parent element",
		0,
		math.MaxUint64,
		0,
		glib.ParameterWritable,
	),
	glib.NewUint64Param(
		"track",
		"Track Handle",
		"cgo.Handle (uintptr) to a RemoteTrackInfo struct",
		0,
		math.MaxUint64,
		0,
		glib.ParameterWritable,
	),
}

func (*srcTrack) New() glib.GoObjectSubclass {
	return &srcTrack{}
}

func (*srcTrack) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"sink_camera",
		"sink/video",
		"Sends video packets to a WebRTC PeerConnection",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)VP8, payload=(int)96")))

	CAT.Log(gst.LevelDebug, "Installing properties")
	class.InstallProperties(src_track_properties)
}

func (s *srcTrack) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseSrc(instance)

	self.SetLive(true)
	self.SetFormat(gst.FormatTime)
	self.SetAsync(true)
	// self.SetDoTimestamp(true)
}

func (s *srcTrack) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := base.ToGstBaseSrc(instance)
	param := src_track_properties[id]
	switch param.Name() {
	case "parent":
		gv, _ := value.GoValue()
		val, _ := gv.(uint64)
		h := cgo.Handle(uintptr(val))
		if h == cgo.Handle(0) {
			self.Log(CAT, gst.LevelError, "Invalid parent handle provided")
			return
		}
		roomInterface := h.Value()
		lkroom, ok := roomInterface.(*lkroom)
		if !ok {
			self.Log(CAT, gst.LevelError, "Parent handle does not contain a lkroom parent element")
			return
		}
		s.parent = lkroom
		self.Log(CAT, gst.LevelInfo, "Track set from handle")
	case "track":
		gv, _ := value.GoValue()
		val, _ := gv.(uint64)
		h := cgo.Handle(uintptr(val))
		if h == cgo.Handle(0) {
			self.Log(CAT, gst.LevelError, "Invalid track handle provided")
			return
		}
		trackInterface := h.Value()
		trackInfo, ok := trackInterface.(*RemoteTrackInfo)
		if !ok {
			self.Log(CAT, gst.LevelError, "Track handle does not contain a RemoteTrackInfo struct")
			return
		}
		s.RemoteTrackInfo = trackInfo
		self.Log(CAT, gst.LevelInfo, "Track set from handle")
	}
}

func (s *srcTrack) SetCaps(self *base.GstBaseSrc, caps *gst.Caps) bool {
	return true
}

func (s *srcTrack) GetCaps(self *base.GstBaseSrc, filter *gst.Caps) *gst.Caps {
	caps := gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)VP8, payload=(int)96")
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get filter: %s", filter.String()))
		if intersect := caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get: %s", caps.String()))
	return caps.Copy().Ref()
}

func (s *srcTrack) Start(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelDebug, "Starting")
	if s.parent == nil {
		self.Log(CAT, gst.LevelError, "Parent lkroom element is not set")
		self.Error("Parent lkroom element is not set", errors.New("parent lkroom is nil"))
		return false
	}

	if s.RemoteTrackInfo == nil {
		self.Log(CAT, gst.LevelError, "RemoteTrackInfo is not set")
		self.Error("RemoteTrackInfo is not set", errors.New("RemoteTrackInfo is nil"))
		return false
	}

	if !s.parent.state.IsJoined() {
		if !s.parent.state.WaitJoined() {
			self.Log(CAT, gst.LevelError, "Parent lkroom element failed to join room before starting src_track")
			self.Error("Parent lkroom element failed to join room", errors.New("parent lkroom not joined"))
			return false
		}
	}

	return true
}

func (s *srcTrack) Stop(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")

	if err := s.pub.SetSubscribed(false); err != nil {
		if err.Error() == "transport is not connected" {
			self.Log(CAT, gst.LevelWarning, "Transport is not connected, skipping unsubscribe")
			return true
		}
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error unsubscribing from track: %T::%v", err, err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error unsubscribing from track", err.Error())
		return true // return true to avoid blocking shutdown
	}

	return true
}

func (s *srcTrack) Fill(self *base.GstBaseSrc, offset uint64, length uint, buffer *gst.Buffer) gst.FlowReturn {
	self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Fill called: offset=%d, length=%d", offset, length))

	mapInfo := buffer.Map(gst.MapWrite)
	defer buffer.Unmap()

	ptr := mapInfo.Data()
	data := unsafe.Slice((*byte)(ptr), length)

	n, _, err := s.track.Read(data)
	if err != nil {
		if err == io.EOF {
			self.Log(CAT, gst.LevelInfo, "reached EOF")
			return gst.FlowEOS
		}
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error reading from io.Reader: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorRead, "Error reading from io.Reader", err.Error())
		return gst.FlowError
	}

	if uint(n) < length {
		buffer.SetSize(int64(n))
	}
	self.Log(CAT, gst.LevelTrace, fmt.Sprintf("filled buffer with %d bytes", n))

	return gst.FlowOK
}

func (s *srcTrack) Unlock(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelInfo, "unlocked")

	if err := s.track.SetReadDeadline(time.Now()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting read deadline on track: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error setting read deadline on track", err.Error())
		return false
	}

	return true
}

// func (s *srcTrack) startAsync(self *base.GstBaseSrc, transition gst.StateChange) gst.StateChangeReturn {
// 	if s.parent.state.IsJoined() {
// 		ret := self.ParentChangeState(transition)
// 		return ret
// 	}

// 	go func() {
// 		if !s.parent.state.WaitJoined() {
// 			self.Log(CAT, gst.LevelError, "Parent lkroom element failed to join room before starting sink_camera")
// 			self.ContinueState(gst.StateChangeFailure)
// 			return
// 		}
// 		self.Log(CAT, gst.LevelInfo, "Parent lkroom element joined room, continuing sink_camera state change")
// 		ret := s.startAsync(self, transition)
// 		self.ContinueState(ret)
// 	}()
// 	return gst.StateChangeAsync
// }

// func (s *srcTrack) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
// 	self := base.ToGstBaseSrc(instance)

// 	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Changing state: %s", transition.String()))

// 	if s.parent == nil {
// 		self.Log(CAT, gst.LevelError, "Parent lkroom element is not set in sink_camera")
// 		self.Error("Parent lkroom element is not set", errors.New("parent lkroom is nil"))
// 		return gst.StateChangeFailure
// 	}

// 	if transition == gst.StateChangeReadyToPaused {
// 		return s.startAsync(self, transition)
// 	}

// 	ret := self.ParentChangeState(transition)

// 	if transition == gst.StateChangeReadyToNull {
// 		s.parent = nil
// 	}
// 	return ret
// }
