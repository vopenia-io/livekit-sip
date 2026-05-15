package nvav1video

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"nv-av1-video",
		gst.RankNone,
		&NvAv1Video{},
		gst.ExtendsBin,
	) && gst.RegisterElement(
		nil,
		"nv-av1-high-dec",
		gst.RankNone,
		&NvAv1HighDec{},
		gst.ExtendsBin,
	)
}
