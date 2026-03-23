package iolivekit

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"io_manager_livekit",
		gst.RankNone,
		&IoManagerLivekit{},
		gst.ExtendsBin,
	)
}
