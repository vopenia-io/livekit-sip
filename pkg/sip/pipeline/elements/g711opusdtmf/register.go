package g711opusdtmf

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"g711-opus-dtmf",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&G711OpusDtmf{},
		// The base subclass this element extends
		gst.ExtendsBin,
	)
}
