package iomanager

import (
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"io_manager",
	gst.DebugColorNone,
	"livekit SIP pipeline IO elements",
)
