package mediacut

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"media-cut",
		gst.RankNone,
		&MediaCut{},
		gst.ExtendsBin,
	)
}
