package iosip

import (
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"io_manager_sip",
	gst.DebugColorNone,
	"livekit SIP pipeline SIP IO element",
)
