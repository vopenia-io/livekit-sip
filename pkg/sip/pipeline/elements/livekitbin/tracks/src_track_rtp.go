package tracks

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unsafe"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

func NewSrcTrackRtp(parent *SrcTrack) (*gst.Element, error) {
	element, err := gst.NewElement("livekitbin_srctrack_rtp")
	if err != nil {
		return nil, err
	}
	src, ok := gst.SubclassFromElement[*SrcTrackRtp](element)
	if !ok {
		return nil, fmt.Errorf("failed to cast element to SrcTrackRtp")
	}
	src.parent = parent

	return element, nil
}

type SrcTrackRtp struct {
	parent  *SrcTrack
	unblock bool
}

func (*SrcTrackRtp) New() glib.GoObjectSubclass {
	return &SrcTrackRtp{}
}

func (*SrcTrackRtp) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"lkroom_srctrack_rtp",
		"src",
		"Receives RTP packets from a WebRTC PeerConnection",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp")))
}

func (s *SrcTrackRtp) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseSrc(instance)

	self.SetLive(true)
	self.SetFormat(gst.FormatTime)
	self.SetAsync(false)
	self.SetDoTimestamp(true)
}

func (s *SrcTrackRtp) SetCaps(self *base.GstBaseSrc, caps *gst.Caps) bool {
	return true
}

func (s *SrcTrackRtp) GetCaps(self *base.GstBaseSrc, filter *gst.Caps) *gst.Caps {
	codec := s.parent.Track.Codec()

	// media, enc, ok := strings.Cut(codec.MimeType, "/")
	// if !ok {
	// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid codec mime type: %s", codec.MimeType))
	// 	return nil
	// }

	capsStr := "application/x-rtp"
	// capsStr += fmt.Sprintf(", media=(string)%s", strings.ToLower(media))
	// capsStr += fmt.Sprintf(", encoding-name=(string)%s", strings.ToUpper(enc))
	capsStr += fmt.Sprintf(", payload=(int)%d", codec.PayloadType)
	// capsStr += fmt.Sprintf(", clock-rate=(int)%d", codec.ClockRate)
	if codec.Channels > 0 {
		capsStr += fmt.Sprintf(", channels=(int)%d", codec.Channels)
	}

	caps := gst.NewCapsFromString(capsStr)
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		if intersect := caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	return caps.Copy().Ref()
}

func (s *SrcTrackRtp) Start(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelDebug, "Starting")
	if s.parent == nil {
		self.Log(CAT, gst.LevelError, "Parent SrcTrack element is not set")
		self.Error("Parent SrcTrack element is not set", errors.New("parent SrcTrack is nil"))
		return false
	}

	return true
}

func (s *SrcTrackRtp) Stop(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")

	if err := s.parent.Pub.SetSubscribed(false); err != nil {
		if strings.Contains(err.Error(), "transport is not connected") {
			self.Log(CAT, gst.LevelWarning, "Transport is not connected, skipping unsubscribe")
			return true
		}
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error unsubscribing from track: %T::%v", err, err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error unsubscribing from track", err.Error())
		return true // return true to avoid blocking shutdown
	}

	return true
}

func (s *SrcTrackRtp) Fill(self *base.GstBaseSrc, offset uint64, length uint, buffer *gst.Buffer) gst.FlowReturn {
	self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Fill called: offset=%d, length=%d", offset, length))

	mapInfo := buffer.Map(gst.MapWrite)
	defer buffer.Unmap()

	ptr := mapInfo.Data()
	data := unsafe.Slice((*byte)(ptr), length)

	n, _, err := s.parent.Track.Read(data)
	if s.unblock {
		return gst.FlowEOS
	}
	if err != nil {
		if err == io.EOF {
			self.Log(CAT, gst.LevelInfo, "reached EOF")
			return gst.FlowEOS
		}
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error reading from io.Reader: %T: %v", err, err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorRead, "Error reading from io.Reader", err.Error())
		return gst.FlowError
	}

	if uint(n) < length {
		buffer.SetSize(int64(n))
	}
	self.Log(CAT, gst.LevelTrace, fmt.Sprintf("filled buffer with %d bytes", n))

	return gst.FlowOK
}

func (s *SrcTrackRtp) Unlock(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelInfo, "unlocked")

	s.unblock = true
	if err := s.parent.Track.SetReadDeadline(time.Now()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting read deadline on track: %v", err))
		self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "Error setting read deadline on track", err.Error())
		return false
	}

	return true
}

// func (s *SrcTrackRtp) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
// 	self := base.ToGstBaseSrc(instance)
// 	ret := self.ParentChangeState(transition)

// 	if transition == gst.StateChangeReadyToNull {
// 		s.parent = nil
// 	}
// 	return ret
// }
