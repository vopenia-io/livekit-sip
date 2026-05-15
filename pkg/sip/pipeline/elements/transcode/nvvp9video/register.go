package nvvp9video

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"nv-vp9-video",
		gst.RankNone,
		&NvVp9Video{},
		gst.ExtendsBin,
	)
}
