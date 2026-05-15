package nvvideovp8

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"nv-video-vp8",
		gst.RankNone,
		&NvVideoVp8{},
		gst.ExtendsBin,
	)
}
