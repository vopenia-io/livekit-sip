package g711opus

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"g711-opus",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&G711Opus{},
		// The base subclass this element extends
		gst.ExtendsBin,
	)
}
