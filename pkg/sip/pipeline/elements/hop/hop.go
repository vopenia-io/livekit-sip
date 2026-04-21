package hop

/*
#cgo pkg-config: gstreamer-1.0

#include <gst/gst.h>

#include "hop_src.h"
#include "hop_sink.h"
*/
import "C"
import "github.com/go-gst/go-glib/glib"

var (
	TypeHopSrc  = glib.Type(C.HOP_TYPE_SRC)
	TypeHopSink = glib.Type(C.HOP_TYPE_SINK)
)

func Register() bool {
	if int(C.gst_element_register(nil, C.CString("hopsink"), C.GST_RANK_NONE, C.HOP_TYPE_SINK)) == 0 {
		return false
	}

	if int(C.gst_element_register(nil, C.CString("hopsrc"), C.GST_RANK_NONE, C.HOP_TYPE_SRC)) == 0 {
		return false
	}
	return true
}
