package nvvp8video

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"nv-vp8-video",
		gst.RankNone,
		&NvVp8Video{},
		gst.ExtendsBin,
	)
}
