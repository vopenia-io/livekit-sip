package lkroom

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

func Register() bool {
	return gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"lkroom",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&lkroom{},
		// The base subclass this element extends
		gst.ExtendsBin,
	) && gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"lkroom_sinkrtcp",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&sinkRtcp{},
		// The base subclass this element extends
		base.ExtendsBaseSink,
	) && gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"lkroom_sinkcamera",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&sinkCamera{},
		// The base subclass this element extends
		base.ExtendsBaseSink,
	) && gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"lkroom_srctrack",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&SrcTrack{},
		// The base subclass this element extends
		gst.ExtendsBin,
	) && gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"lkroom_srctrack_rtp",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&SrcTrackRtp{},
		// The base subclass this element extends
		base.ExtendsBaseSrc,
	)
}
