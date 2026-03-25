package g711dtmfaudio

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"g711dtmf-audio",
		gst.RankNone,
		&G711DTMFAudio{},
		gst.ExtendsBin,
	)
}
