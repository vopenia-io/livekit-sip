package pipeline

/*
#cgo pkg-config: gstreamer-1.0
#include <gst/gst.h>

void custom_gst_log_filter(GstDebugCategory *category,
                           GstDebugLevel level,
                           const gchar *file,
                           const gchar *function,
                           gint line,
                           GObject *object,
                           GstDebugMessage *message,
                           gpointer user_data) {
    // Only intercept warnings from the 'bin' category
    if (level == GST_LEVEL_WARNING && g_strcmp0(gst_debug_category_get_name(category), "bin") == 0) {
        const gchar *msg_text = gst_debug_message_get(message);

        // Match GStreamer's exact internal typo: "loop dected"
        if (g_strstr_len(msg_text, -1, "loop dected")) {
			return; // Drop the log by returning before it prints
        }
    }

    // Pass everything else to the default GStreamer handler to print normally
    gst_debug_log_default(category, level, file, function, line, object, message, user_data);
}

void custom_glib_log_handler(const gchar *log_domain,
                             GLogLevelFlags log_level,
                             const gchar *message,
                             gpointer user_data) {
    // Check for the exact GLib string
    if (g_strstr_len(message, -1, "loop detected in the graph")) {
        return; // Drop the log
    }
    // Pass everything else to the default handler
    g_log_default_handler(log_domain, log_level, message, user_data);
}

void setup_gst_log_handler() {
	g_log_set_handler("GStreamer", G_LOG_LEVEL_WARNING, custom_glib_log_handler, NULL);
	// gst_debug_remove_log_function(gst_debug_log_default);
	// gst_debug_add_log_function((GstLogFunction)custom_gst_log_filter, NULL, NULL);
}
*/
import "C"

import (
	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/activeselector"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/g711opus"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/g711opusdtmf"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/h264vp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/iomanager"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/lkroom"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/opusg711"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/opusg711mix"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sinkwriter"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipmanager"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sourcereader"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/vp8h264"
)

var MainLoop *glib.MainLoop

func init() {
	gst.Init(nil)
	if !sourcereader.Register() {
		panic("failed to register sourcereader element")
	}

	if !sinkwriter.Register() {
		panic("failed to register sinkwriter element")
	}

	if !h264vp8.Register() {
		panic("Failed to register h264-vp8")
	}

	if !vp8h264.Register() {
		panic("Failed to register vp8-h264")
	}

	if !g711opus.Register() {
		panic("Failed to register g711-opus")
	}

	if !g711opusdtmf.Register() {
		panic("Failed to register g711-opus-dtmf")
	}

	if !opusg711.Register() {
		panic("Failed to register opus-g711")
	}

	if !opusg711mix.Register() {
		panic("Failed to register opus_g711_mix")
	}

	if !sipmanager.Register() {
		panic("Failed to register sipmanager")
	}

	if !lkroom.Register() {
		panic("Failed to register lkroom")
	}

	if !activeselector.Register() {
		panic("Failed to register active-selector")
	}

	if !iomanager.Register() {
		panic("Failed to register io_manager")
	}

	MainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)

	// C.setup_gst_log_handler()

	go MainLoop.Run()
}
