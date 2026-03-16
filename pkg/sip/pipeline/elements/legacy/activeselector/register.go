package activeselector

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"active-selector",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&ActiveSelector{},
		// The base subclass this element extends
		gst.ExtendsBin,
	)
}
