package sip

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/livekit/media-sdk/srtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

// VideoDirection describes the media direction negotiated with the SIP peer.
type VideoDirection struct {
	// Send indicates that the SIP peer will send video to us.
	Send bool
	// Recv indicates that the SIP peer is willing to receive video from us.
	Recv bool
}

// VideoConfig holds negotiated video parameters.
type VideoConfig struct {
	CodecName   string
	Type        byte
	ClockRate   uint32
	MimeType    string
	SDPFmtpLine string
	Direction   VideoDirection
}

type videoCodec struct {
	Name      string
	MimeType  string
	ClockRate uint32
	Fmtp      string
	Feedback  []string
}

type videoCodecOffer struct {
	codec       *videoCodec
	payloadType byte
}

type remoteVideoCodec struct {
	payloadType byte
	name        string
	clockRate   uint32
	fmtp        string
}

var supportedVideoCodecs = []*videoCodec{
	{
		Name:      "H264",
		MimeType:  webrtc.MimeTypeH264,
		ClockRate: 90000,
		Fmtp:      "profile-level-id=42e01f;packetization-mode=1",
		Feedback: []string{
			"nack",
			"nack pli",
			"ccm fir",
			"goog-remb",
		},
	},
	{
		Name:      "VP8",
		MimeType:  webrtc.MimeTypeVP8,
		ClockRate: 90000,
		Feedback: []string{
			"nack",
			"nack pli",
			"ccm fir",
			"goog-remb",
		},
	},
}

func addCryptoAttributes(attrs []sdp.Attribute, profiles []srtp.Profile) []sdp.Attribute {
	if len(profiles) == 0 {
		return attrs
	}
	for _, p := range profiles {
		key := append([]byte{}, p.Key...)
		key = append(key, p.Salt...)
		encoded := base64.StdEncoding.WithPadding(base64.StdPadding).EncodeToString(key)
		attrs = append(attrs, sdp.Attribute{
			Key:   "crypto",
			Value: fmt.Sprintf("%d %s inline:%s", p.Index, p.Profile, encoded),
		})
	}
	return attrs
}

func offerVideoDescription(port int, profiles []srtp.Profile, encrypted bool) ([]videoCodecOffer, *sdp.MediaDescription) {
	if port == 0 {
		return nil, nil
	}
	var offers []videoCodecOffer
	attrs := make([]sdp.Attribute, 0, len(supportedVideoCodecs)*4)
	formats := make([]string, 0, len(supportedVideoCodecs))
	payload := byte(120)
	for _, codec := range supportedVideoCodecs {
		pt := payload
		payload++
		offers = append(offers, videoCodecOffer{codec: codec, payloadType: pt})
		styp := strconv.Itoa(int(pt))
		formats = append(formats, styp)
		attrs = append(attrs, sdp.Attribute{Key: "rtpmap", Value: fmt.Sprintf("%s %s/%d", styp, codec.Name, codec.ClockRate)})
		if codec.Fmtp != "" {
			attrs = append(attrs, sdp.Attribute{Key: "fmtp", Value: fmt.Sprintf("%s %s", styp, codec.Fmtp)})
		}
		for _, fb := range codec.Feedback {
			attrs = append(attrs, sdp.Attribute{Key: "rtcp-fb", Value: fmt.Sprintf("%s %s", styp, fb)})
		}
	}
	attrs = append(attrs, sdp.Attribute{Key: "rtcp-mux"})
	attrs = append(attrs, sdp.Attribute{Key: "sendrecv"})
	if encrypted {
		attrs = addCryptoAttributes(attrs, profiles)
	}
	proto := "AVP"
	if encrypted {
		proto = "SAVP"
	}
	return offers, &sdp.MediaDescription{
		MediaName: sdp.MediaName{
			Media:   "video",
			Port:    sdp.RangedPort{Value: port},
			Protos:  []string{"RTP", proto},
			Formats: formats,
		},
		Attributes: attrs,
	}
}

func answerVideoDescription(port int, cfg *VideoConfig, profiles []srtp.Profile, encrypted bool) *sdp.MediaDescription {
	if cfg == nil || port == 0 {
		return nil
	}
	attrs := []sdp.Attribute{
		{Key: "rtpmap", Value: fmt.Sprintf("%d %s/%d", cfg.Type, cfg.CodecName, cfg.ClockRate)},
		{Key: "rtcp-mux"},
	}
	dir := cfg.Direction
	switch {
	case dir.Send && dir.Recv:
		attrs = append(attrs, sdp.Attribute{Key: "sendrecv"})
	case dir.Send:
		attrs = append(attrs, sdp.Attribute{Key: "recvonly"})
	case dir.Recv:
		attrs = append(attrs, sdp.Attribute{Key: "sendonly"})
	default:
		attrs = append(attrs, sdp.Attribute{Key: "inactive"})
	}
	if cfg.SDPFmtpLine != "" {
		attrs = append(attrs, sdp.Attribute{Key: "fmtp", Value: fmt.Sprintf("%d %s", cfg.Type, cfg.SDPFmtpLine)})
	}
	if codec := matchVideoCodec(cfg.CodecName); codec != nil {
		for _, fb := range codec.Feedback {
			attrs = append(attrs, sdp.Attribute{Key: "rtcp-fb", Value: fmt.Sprintf("%d %s", cfg.Type, fb)})
		}
	}
	if encrypted {
		attrs = addCryptoAttributes(attrs, profiles)
	}
	proto := "AVP"
	if encrypted {
		proto = "SAVP"
	}
	return &sdp.MediaDescription{
		MediaName: sdp.MediaName{
			Media:   "video",
			Port:    sdp.RangedPort{Value: port},
			Protos:  []string{"RTP", proto},
			Formats: []string{strconv.Itoa(int(cfg.Type))},
		},
		Attributes: attrs,
	}
}

func parseVideoOffer(md *sdp.MediaDescription) (*VideoConfig, bool) {
	if md == nil || md.MediaName.Media != "video" {
		return nil, false
	}
	if md.MediaName.Port.Value == 0 {
		return nil, false
	}
	dir := directionFromAttributes(md.Attributes)
	codecs := parseRemoteVideoCodecs(md)
	for _, rc := range codecs {
		if codec := matchVideoCodec(rc.name); codec != nil {
			cfg := &VideoConfig{
				CodecName:   codec.Name,
				Type:        rc.payloadType,
				ClockRate:   codec.ClockRate,
				MimeType:    codec.MimeType,
				SDPFmtpLine: rc.fmtp,
				Direction:   dir,
			}
			if cfg.SDPFmtpLine == "" {
				cfg.SDPFmtpLine = codec.Fmtp
			}
			return cfg, true
		}
	}
	return nil, false
}

func mapVideoAnswer(md *sdp.MediaDescription, offers []videoCodecOffer) *VideoConfig {
	if md == nil || md.MediaName.Media != "video" || md.MediaName.Port.Value == 0 {
		return nil
	}
	offerMap := make(map[string]videoCodecOffer, len(offers))
	for _, off := range offers {
		offerMap[strconv.Itoa(int(off.payloadType))] = off
	}
	dir := directionFromAttributes(md.Attributes)
	codecs := parseRemoteVideoCodecs(md)
	for _, rc := range codecs {
		key := strconv.Itoa(int(rc.payloadType))
		if off, ok := offerMap[key]; ok {
			cfg := &VideoConfig{
				CodecName:   off.codec.Name,
				Type:        rc.payloadType,
				ClockRate:   off.codec.ClockRate,
				MimeType:    off.codec.MimeType,
				SDPFmtpLine: rc.fmtp,
				Direction:   dir,
			}
			if cfg.SDPFmtpLine == "" {
				cfg.SDPFmtpLine = off.codec.Fmtp
			}
			return cfg
		}
	}
	return nil
}

func parseRemoteVideoCodecs(md *sdp.MediaDescription) []remoteVideoCodec {
	fmts := md.MediaName.Formats
	if len(fmts) == 0 {
		return nil
	}
	rtpMap := make(map[string]remoteVideoCodec, len(fmts))
	for _, attr := range md.Attributes {
		switch attr.Key {
		case "rtpmap":
			parts := strings.Fields(attr.Value)
			if len(parts) != 2 {
				continue
			}
			pt := parts[0]
			sub := strings.Split(parts[1], "/")
			if len(sub) == 0 {
				continue
			}
			name := strings.ToUpper(sub[0])
			clock := uint32(90000)
			if len(sub) > 1 {
				if v, err := strconv.Atoi(sub[1]); err == nil {
					clock = uint32(v)
				}
			}
			rtpMap[pt] = remoteVideoCodec{payloadType: parsePayloadType(pt), name: name, clockRate: clock}
		case "fmtp":
			pt, params, found := strings.Cut(attr.Value, " ")
			if !found {
				continue
			}
			entry := rtpMap[pt]
			entry.payloadType = parsePayloadType(pt)
			entry.fmtp = params
			rtpMap[pt] = entry
		}
	}
	var codecs []remoteVideoCodec
	for _, fmt := range fmts {
		if entry, ok := rtpMap[fmt]; ok {
			codecs = append(codecs, entry)
		} else {
			pt := parsePayloadType(fmt)
			codecs = append(codecs, remoteVideoCodec{payloadType: pt})
		}
	}
	return codecs
}

func parsePayloadType(v string) byte {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	if n < 0 {
		n = 0
	}
	if n > 255 {
		n = 255
	}
	return byte(n)
}

func matchVideoCodec(name string) *videoCodec {
	for _, codec := range supportedVideoCodecs {
		if strings.EqualFold(codec.Name, name) {
			return codec
		}
	}
	return nil
}

func directionFromAttributes(attrs []sdp.Attribute) VideoDirection {
	dir := VideoDirection{Send: true, Recv: true}
	for _, attr := range attrs {
		switch attr.Key {
		case "sendonly":
			dir.Send = true
			dir.Recv = false
		case "recvonly":
			dir.Send = false
			dir.Recv = true
		case "inactive":
			dir.Send = false
			dir.Recv = false
		case "sendrecv":
			dir.Send = true
			dir.Recv = true
		}
	}
	return dir
}

func findVideoDescription(sd *sdp.SessionDescription) *sdp.MediaDescription {
	if sd == nil {
		return nil
	}
	for _, md := range sd.MediaDescriptions {
		if md.MediaName.Media == "video" {
			return md
		}
	}
	return nil
}
