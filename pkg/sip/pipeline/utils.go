package pipeline

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

func LinkPad(src, dst *gst.Pad) error {
	if src == nil {
		return fmt.Errorf("source pad is nil")
	}
	if dst == nil {
		return fmt.Errorf("destination pad is nil")
	}
	if r := src.Link(dst); r != gst.PadLinkOK {
		return fmt.Errorf("failed to link pads: %s", r.String())
	}
	return nil
}

func ReleasePad(pad *gst.Pad) {
	if pad != nil {
		parent := pad.GetParentElement()
		if parent != nil {
			parent.ReleaseRequestPad(pad)
		}
	}
}

func CastErr[T any](v any, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	casted, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("failed to cast value")
	}
	return casted, nil
}

func ValidatePad(pad *gst.Pad) error {
	if pad == nil {
		return fmt.Errorf("pad is nil")
	}
	parent := pad.GetParentElement()
	if parent == nil {
		return fmt.Errorf("pad's parent element is nil")
	}
	return nil
}

// UnrefElement safely unrefs an element if it's not nil
func UnrefElement(elem *gst.Element) {
	if elem != nil {
		elem.Unref()
	}
}

// UnrefElements safely unrefs multiple elements
func UnrefElements(elems ...*gst.Element) {
	for _, elem := range elems {
		UnrefElement(elem)
	}
}

// DisconnectSignal safely disconnects a signal handler from an element
func DisconnectSignal(elem *gst.Element, handle glib.SignalHandle) {
	if elem != nil && handle != 0 {
		elem.HandlerDisconnect(handle)
	}
}

// ReleaseAllRequestPads releases all request pads from an element.
// This is useful for elements like rtpbin that create internal ghost/proxy pads.
func ReleaseAllRequestPads(elem *gst.Element) {
	if elem == nil {
		return
	}
	pads, err := elem.GetPads()
	if err != nil {
		return
	}
	for _, pad := range pads {
		// Only release request pads (not static pads)
		// Request pads have templates with GST_PAD_REQUEST presence
		tmpl := pad.GetPadTemplate()
		if tmpl != nil && tmpl.Presence() == gst.PadPresenceRequest {
			// Deactivate and unlink first
			pad.SetActive(false)
			if peer := pad.GetPeer(); peer != nil {
				if pad.GetDirection() == gst.PadDirectionSource {
					pad.Unlink(peer)
				} else {
					peer.Unlink(pad)
				}
			}
			elem.ReleaseRequestPad(pad)
		}
	}
}
