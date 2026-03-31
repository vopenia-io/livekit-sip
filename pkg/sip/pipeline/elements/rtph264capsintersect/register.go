package rtph264capsintersect

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"rtph264capsintersect",
		gst.RankNone,
		&RtpH264CapsIntersect{},
		base.ExtendsBaseTransform,
	)
}
