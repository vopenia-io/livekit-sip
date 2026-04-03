package nvvideoh264

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"nv-video-h264",
		gst.RankNone,
		&NvVideoH264{},
		gst.ExtendsBin,
	)
}
