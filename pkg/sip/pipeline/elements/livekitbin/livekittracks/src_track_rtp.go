package livekittracks

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
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
	unblock atomic.Bool
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
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
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
	if s.parent == nil || s.parent.Track == nil {
		return gst.NewCapsFromString("application/x-rtp").Ref()
	}
	// codec := s.parent.Track.Codec()

	// media, enc, ok := strings.Cut(codec.MimeType, "/")
	// if !ok {
	// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid codec mime type: %s", codec.MimeType))
	// 	return nil
	// }

	capsStr := "application/x-rtp"
	// capsStr += fmt.Sprintf(", media=(string)%s", strings.ToLower(media))
	// capsStr += fmt.Sprintf(", encoding-name=(string)%s", strings.ToUpper(enc))
	// capsStr += fmt.Sprintf(", payload=(int)%d", codec.PayloadType)
	// capsStr += fmt.Sprintf(", clock-rate=(int)%d", codec.ClockRate)
	// if codec.Channels > 0 {
	// 	capsStr += fmt.Sprintf(", channels=(int)%d", codec.Channels)
	// }

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

	if err := s.parent.SendSourceInfo(); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to send source info: %v", err))
		self.Error("Failed to send source info", err)
		return false
	}

	s.unblock.Store(false)
	if err := s.parent.Track.SetReadDeadline(time.Time{}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to reset read deadline on track: %v", err))
		self.Error("Failed to reset read deadline on track", err)
		return false
	}

	return true
}

func (s *SrcTrackRtp) Stop(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")

	return true
}

func (s *SrcTrackRtp) Fill(self *base.GstBaseSrc, offset uint64, length uint, buffer *gst.Buffer) gst.FlowReturn {
	if s.unblock.Load() {
		self.Log(CAT, gst.LevelInfo, "Fill called but unblock is set, returning EOS")
		return gst.FlowFlushing
	}

	mapInfo := buffer.Map(gst.MapWrite)
	defer buffer.Unmap()

	ptr := mapInfo.Data()
	data := unsafe.Slice((*byte)(ptr), length)

	n, _, err := s.parent.Track.Read(data)
	if s.unblock.Load() {
		self.Log(CAT, gst.LevelInfo, "Fill unblocked, returning Flushing")
		return gst.FlowFlushing
	}
	if err != nil {
		if err == io.EOF {
			return gst.FlowEOS
		}
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to read from io.Reader: %T: %v", err, err))
		self.Error("Failed to read from io.Reader", err)
		return gst.FlowError
	}

	if uint(n) < length {
		buffer.SetSize(int64(n))
	}
	return gst.FlowOK
}

func (s *SrcTrackRtp) Unlock(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelInfo, "SrcTrackRtp Unlock called, unblocking Fill and sending EOS")

	s.unblock.Store(true)

	if err := s.parent.Track.SetReadDeadline(time.Now()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set read deadline on track: %v", err))
		self.Error("Failed to set read deadline on track", err)
		return false
	}

	return true
}

func (s *SrcTrackRtp) UnlockStop(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelInfo, "SrcTrackRtp UnlockStop called")
	s.unblock.Store(false)
	if err := s.parent.Track.SetReadDeadline(time.Time{}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to reset read deadline on track: %v", err))
		self.Error("Failed to reset read deadline on track", err)
		return false
	}
	return true
}
