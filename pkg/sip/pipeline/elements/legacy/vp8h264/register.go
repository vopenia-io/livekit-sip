package vp8h264

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"vp8-h264",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&Vp8H264{},
		// The base subclass this element extends
		gst.ExtendsBin,
	)
}
