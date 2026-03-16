package opusg711mix

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"opus_g711_mix",
		gst.RankNone,
		&OpusG711Mix{},
		gst.ExtendsBin,
	)
}
