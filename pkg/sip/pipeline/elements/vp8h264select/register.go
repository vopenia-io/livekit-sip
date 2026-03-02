package vp8h264select

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"vp8_h264_select",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&Vp8H264Select{},
		// The base subclass this element extends
		gst.ExtendsBin,
	)
}
