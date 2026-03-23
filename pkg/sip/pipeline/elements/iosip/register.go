package iosip

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"io_manager_sip",
		gst.RankNone,
		&IoManagerSip{},
		gst.ExtendsBin,
	)
}
