package g711audio

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"g711-audio",
		gst.RankNone,
		&G711Audio{},
		gst.ExtendsBin,
	)
}
