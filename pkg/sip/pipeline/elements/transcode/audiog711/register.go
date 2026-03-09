package audiog711

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"audio-g711",
		gst.RankNone,
		&AudioG711{},
		gst.ExtendsBin,
	)
}
