package nvvideovp9

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"nv-video-vp9",
		gst.RankNone,
		&NvVideoVp9{},
		gst.ExtendsBin,
	)
}
