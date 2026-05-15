package nvh264video

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"nv-h264-video",
		gst.RankNone,
		&NvH264Video{},
		gst.ExtendsBin,
	)
}
