package wavsource

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"wavsource",
		gst.RankNone,
		&WavSource{},
		gst.ExtendsBin,
	)
}
