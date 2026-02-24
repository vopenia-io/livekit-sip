package tracks

import "github.com/go-gst/go-gst/gst"

var CAT *gst.DebugCategory

func PadProbeDrop(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
	return gst.PadProbeDrop
}
