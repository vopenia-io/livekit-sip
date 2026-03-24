package pcm16audio

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"pcm16-audio",
		gst.RankNone,
		&PCM16Audio{},
		gst.ExtendsBin,
	)
}
