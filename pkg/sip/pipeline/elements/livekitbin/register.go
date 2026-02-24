package livekitbin

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/tracks"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"livekitbin",
		gst.RankNone,
		&LivekitBin{},
		gst.ExtendsBin,
	) && gst.RegisterElement(
		nil,
		"livekitbin_sinktrack",
		gst.RankNone,
		&tracks.SinkTrack{},
		base.ExtendsBaseSink,
	) && gst.RegisterElement(
		nil,
		"livekitbin_sinkrtcp",
		gst.RankNone,
		&tracks.SinkRtcp{},
		base.ExtendsBaseSink,
	) && gst.RegisterElement(
		nil,
		"livekitbin_srctrack",
		gst.RankNone,
		&tracks.SrcTrack{},
		gst.ExtendsBin,
	) && gst.RegisterElement(
		nil,
		"livekitbin_srctrack_rtp",
		gst.RankNone,
		&tracks.SrcTrackRtp{},
		base.ExtendsBaseSrc,
	)
}
