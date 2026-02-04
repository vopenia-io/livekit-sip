package sipmanager

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"sipmanager",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&SipManager{},
		// The base subclass this element extends
		gst.ExtendsBin,
	) && gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"sip_media",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&SipMedia{},
		// The base subclass this element extends
		gst.ExtendsBin,
	)
}
