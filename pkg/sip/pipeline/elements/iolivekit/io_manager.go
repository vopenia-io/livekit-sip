package iolivekit

import (
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"io_manager_livekit",
	gst.DebugColorNone,
	"livekit SIP pipeline LiveKit IO element",
)
