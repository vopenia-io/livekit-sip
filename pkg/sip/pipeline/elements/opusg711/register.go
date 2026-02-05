package opusg711

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"opus-g711",
		gst.RankNone,
		&OpusG711{},
		gst.ExtendsBin,
	)
}
