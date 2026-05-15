package nvvideoav1

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"nv-video-av1",
		gst.RankNone,
		&NvVideoAV1{},
		gst.ExtendsBin,
	)
}
