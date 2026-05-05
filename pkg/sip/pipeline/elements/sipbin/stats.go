package sipbin

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type RTPSessionStats struct {
	RtxDropCount    uint32
	SentNackCount   uint32
	RecvNackCount   uint32
	RtxCount        uint32
	RecvRtxReqCount uint32
	SentRtxReqCount uint32
	Sources         []RTPSourceStats
}

type RTPSourceStats struct {
	// Always present
	SSRC        uint32
	Internal    bool
	Validated   bool
	ReceivedBye bool
	IsCSRC      bool
	IsSender    bool
	SeqnumBase  int32 // -1 if unknown
	ClockRate   int32 // -1 if unknown

	// Optional (peer address)
	RTPFrom  string // "" if absent
	RTCPFrom string

	// Always set (counters)
	OctetsSent      uint64
	PacketsSent     uint64
	OctetsReceived  uint64
	PacketsReceived uint64
	BytesReceived   uint64
	Bitrate         uint64 // bits/sec
	PacketsLost     int32
	Jitter          uint32 // in clock-rate units
	SentPLICount    uint32
	RecvPLICount    uint32
	SentFIRCount    uint32
	RecvFIRCount    uint32
	SentNACKCount   uint32
	RecvNACKCount   uint32
	RecvPacketRate  uint32

	// Last SR (have-sr gates the rest)
	HaveSR        bool
	SRNTPTime     uint64 // 32.32 NTP fixed-point
	SRRTPTime     uint32
	SROctetCount  uint32
	SRPacketCount uint32

	// Last RB we sent. Only non-internal sources carry these — SentRB is always
	// false for internal sources, so LastSentRB is nil. RoundTrip is always 0
	// (no sent-rb-round-trip field) and SSRC is implicit (= the parent SSRC).
	SentRB     bool         // sent-rb gate
	LastSentRB *ReportBlock // contents (nil iff !SentRB)

	// Last RB we received about this source.
	// For internal: from remote about us. For non-internal: deprecated mirror
	// of LastSentRB.
	HaveRB bool         // have-rb gate
	LastRB *ReportBlock // contents (nil iff !HaveRB)

	// Internal sources only — most recent RR per peer
	ReceivedRR []ReceiverReport
}

type ReportBlock struct {
	SSRC          uint32 // rb-ssrc
	FractionLost  uint8  // rb-fractionlost (0–255)
	PacketsLost   int32
	ExtHighestSeq uint32
	Jitter        uint32 // clock-rate units
	LSR           uint32 // 16.16 NTP short
	DLSR          uint32 // 16.16 NTP short
	RoundTrip     uint32 // 16.16 NTP short — only on RB, not sent-rb
}

type ReceiverReport struct {
	SSRC          uint32
	SenderSSRC    uint32 // rb-sender-ssrc
	FractionLost  uint8
	PacketsLost   int32
	ExtHighestSeq uint32
	Jitter        uint32
	LSR, DLSR     uint32
	RoundTrip     uint32
}

func (e *SipBin) DumpStats(self *gst.Bin) *gst.Structure {
	st := gst.NewStructure("call-stats")

	for i := range e.Tracks {
		if i == 0 || e.Tracks[i] == nil {
			continue
		}
		kind := livekit.TrackSource(i)
		stats, err := e.getStats(kind)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get stats for track source %s: %v", kind, err))
			self.Error(fmt.Sprintf("Failed to get stats for track source %s", kind), err)
			continue
		}
		if stats == nil {
			continue
		}

		if err := st.SetValue(kind.String(), glib.ArbitraryValue{Data: stats}); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set stats structure for track source %s: %v", kind, err))
			self.Error(fmt.Sprintf("Failed to set stats structure for track source %s", kind), err)
		}

		if err := st.SetValue(fmt.Sprintf("%s-caps", kind), e.Tracks[kind].Caps.Copy()); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set caps structure for track source %s: %v", kind, err))
			self.Error(fmt.Sprintf("Failed to set caps structure for track source %s", kind), err)
		}
	}

	return st
}

func (e *SipBin) getStats(kind livekit.TrackSource) (*RTPSessionStats, error) {
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		return nil, fmt.Errorf("invalid track source: %s(%d)", kind, int(kind))
	}

	track := e.Tracks[kind]
	if track == nil || !track.initialized {
		return nil, nil
	}

	rtpSessionVal, err := e.RtpBin.Emit("get-internal-session", uint(kind))
	if err != nil {
		return nil, fmt.Errorf("failed to get internal session for track source %s: %w", kind, err)
	}
	rtpSession, ok := rtpSessionVal.(*glib.Object)
	if !ok {
		return nil, fmt.Errorf("failed to convert internal session to element for track source %s: %w", kind, err)
	}

	statsVal, err := rtpSession.GetProperty("stats")
	if err != nil {
		return nil, fmt.Errorf("failed to get stats for track source %s: %w", kind, err)
	}
	statsSt, ok := statsVal.(*gst.Structure)
	if !ok {
		return nil, fmt.Errorf("failed to convert stats to RTPStats for track source %s: %w", kind, err)
	}

	stats := toStats(statsSt)
	return &stats, nil
}

func toStats(stats *gst.Structure) RTPSessionStats {
	if stats == nil {
		return RTPSessionStats{}
	}
	out := RTPSessionStats{
		RtxDropCount:    structU32(stats, "rtx-drop-count"),
		SentNackCount:   structU32(stats, "sent-nack-count"),
		RecvNackCount:   structU32(stats, "recv-nack-count"),
		RtxCount:        structU32(stats, "rtx-count"),
		RecvRtxReqCount: structU32(stats, "recv-rtx-req-count"),
		SentRtxReqCount: structU32(stats, "sent-rtx-req-count"),
	}

	v, err := stats.GetValue("source-stats")
	if err != nil {
		return out
	}
	arr, ok := v.(*glib.ValueArray)
	if !ok || arr == nil {
		return out
	}
	out.Sources = make([]RTPSourceStats, 0, arr.Len())
	for i := 0; i < arr.Len(); i++ {
		entry, err := arr.Index(i)
		if err != nil {
			continue
		}
		st, ok := entry.(*gst.Structure)
		if !ok || st == nil {
			continue
		}
		out.Sources = append(out.Sources, toSourceStats(st))
	}

	return out
}

func toSourceStats(s *gst.Structure) RTPSourceStats {
	out := RTPSourceStats{
		SSRC:            structU32(s, "ssrc"),
		Internal:        structBool(s, "internal"),
		Validated:       structBool(s, "validated"),
		ReceivedBye:     structBool(s, "received-bye"),
		IsCSRC:          structBool(s, "is-csrc"),
		IsSender:        structBool(s, "is-sender"),
		SeqnumBase:      structI32(s, "seqnum-base"),
		ClockRate:       structI32(s, "clock-rate"),
		RTPFrom:         structString(s, "rtp-from"),
		RTCPFrom:        structString(s, "rtcp-from"),
		OctetsSent:      structU64(s, "octets-sent"),
		PacketsSent:     structU64(s, "packets-sent"),
		OctetsReceived:  structU64(s, "octets-received"),
		PacketsReceived: structU64(s, "packets-received"),
		BytesReceived:   structU64(s, "bytes-received"),
		Bitrate:         structU64(s, "bitrate"),
		PacketsLost:     structI32(s, "packets-lost"),
		Jitter:          structU32(s, "jitter"),
		SentPLICount:    structU32(s, "sent-pli-count"),
		RecvPLICount:    structU32(s, "recv-pli-count"),
		SentFIRCount:    structU32(s, "sent-fir-count"),
		RecvFIRCount:    structU32(s, "recv-fir-count"),
		SentNACKCount:   structU32(s, "sent-nack-count"),
		RecvNACKCount:   structU32(s, "recv-nack-count"),
		RecvPacketRate:  structU32(s, "recv-packet-rate"),
		HaveSR:          structBool(s, "have-sr"),
		SRNTPTime:       structU64(s, "sr-ntptime"),
		SRRTPTime:       structU32(s, "sr-rtptime"),
		SROctetCount:    structU32(s, "sr-octet-count"),
		SRPacketCount:   structU32(s, "sr-packet-count"),
	}
	out.SentRB = structBool(s, "sent-rb")
	if out.SentRB {
		out.LastSentRB = readReportBlock(s, "sent-rb-")
	}
	out.HaveRB = structBool(s, "have-rb")
	if out.HaveRB {
		out.LastRB = readReportBlock(s, "rb-")
	}
	out.ReceivedRR = readReceivedRR(s)
	return out
}

// readReportBlock extracts the 7- or 8-field report block for the given prefix.
// "sent-rb-" lacks a round-trip field, so RoundTrip will be zero in that case.
func readReportBlock(s *gst.Structure, prefix string) *ReportBlock {
	return &ReportBlock{
		SSRC:          structU32(s, prefix+"ssrc"),
		FractionLost:  uint8(structU32(s, prefix+"fractionlost")),
		PacketsLost:   structI32(s, prefix+"packetslost"),
		ExtHighestSeq: structU32(s, prefix+"exthighestseq"),
		Jitter:        structU32(s, prefix+"jitter"),
		LSR:           structU32(s, prefix+"lsr"),
		DLSR:          structU32(s, prefix+"dlsr"),
		RoundTrip:     structU32(s, prefix+"round-trip"),
	}
}

func readReceivedRR(s *gst.Structure) []ReceiverReport {
	v, err := s.GetValue("received-rr")
	if err != nil {
		return nil
	}
	list, ok := v.(*gst.ValueListValue)
	if !ok || list == nil {
		return nil
	}
	n := list.Size()
	if n == 0 {
		return nil
	}
	out := make([]ReceiverReport, 0, n)
	for i := uint(0); i < n; i++ {
		rr, ok := list.ValueAt(i).(*gst.Structure)
		if !ok || rr == nil {
			continue
		}
		out = append(out, ReceiverReport{
			SSRC:          structU32(rr, "rb-ssrc"),
			SenderSSRC:    structU32(rr, "rb-sender-ssrc"),
			FractionLost:  uint8(structU32(rr, "rb-fractionlost")),
			PacketsLost:   structI32(rr, "rb-packetslost"),
			ExtHighestSeq: structU32(rr, "rb-exthighestseq"),
			Jitter:        structU32(rr, "rb-jitter"),
			LSR:           structU32(rr, "rb-lsr"),
			DLSR:          structU32(rr, "rb-dlsr"),
			RoundTrip:     structU32(rr, "rb-round-trip"),
		})
	}
	return out
}

func structU32(s *gst.Structure, key string) uint32 {
	v, err := s.GetValue(key)
	if err != nil {
		return 0
	}
	u, _ := v.(uint)
	return uint32(u)
}

func structI32(s *gst.Structure, key string) int32 {
	v, err := s.GetValue(key)
	if err != nil {
		return 0
	}
	i, _ := v.(int)
	return int32(i)
}

func structU64(s *gst.Structure, key string) uint64 {
	v, err := s.GetValue(key)
	if err != nil {
		return 0
	}
	u, _ := v.(uint64)
	return u
}

func structBool(s *gst.Structure, key string) bool {
	v, err := s.GetValue(key)
	if err != nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

func structString(s *gst.Structure, key string) string {
	v, err := s.GetValue(key)
	if err != nil {
		return ""
	}
	str, _ := v.(string)
	return str
}
