package h264vp8

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"h264-vp8",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&H264Vp8{},
		// The base subclass this element extends
		gst.ExtendsBin,
	)
}
