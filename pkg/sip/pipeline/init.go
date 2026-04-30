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
	"github.com/livekit/sip/pkg/sip/pipeline/elements/bfcpserver"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/h264rtppaybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/hop"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/iolivekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/iosip"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/rtpcapscodecfilter"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/samplewriter"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipcompositor"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/trackfallback"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audioopus"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audiopcma"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audiopcmu"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/av1video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/dtmfaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/factorybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/h264video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvav1video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvh264video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvideoav1"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvideoh264"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvideovp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvideovp9"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvp8video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/nvvp9video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/opusaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/pcm16audio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/pcmaaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/pcmuaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videoav1"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videoh264"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp9"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp8video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp9video"
)

const QDataPadPeerKey = "livekitsip-pad-peer"

var MainLoop *glib.MainLoop

func init() {
	gst.Init(nil)

	if !hop.Register() {
		panic("Failed to register hop src/sink")
	}

	if !sipbin.Register() {
		panic("Failed to register sipbin")
	}

	if !livekitbin.Register() {
		panic("Failed to register livekitbin")
	}

	if !trackfallback.Register() {
		panic("Failed to register trackfallback")
	}

	if !iolivekit.Register() {
		panic("Failed to register io_manager_livekit")
	}

	if !iosip.Register() {
		panic("Failed to register io_manager_sip")
	}

	if !audioopus.Register() {
		panic("Failed to register audio-opus")
	}

	if !opusaudio.Register() {
		panic("Failed to register opus-audio")
	}

	if !h264video.Register() {
		panic("Failed to register h264-video")
	}

	if !nvh264video.Register() {
		panic("Failed to register nv-h264-video")
	}

	if !videovp8.Register() {
		panic("Failed to register video-vp8")
	}

	if !nvvideovp8.Register() {
		panic("Failed to register nv-video-vp8")
	}

	if !vp8video.Register() {
		panic("Failed to register vp8-video")
	}

	if !nvvp8video.Register() {
		panic("Failed to register nv-vp8-video")
	}

	if !videoh264.Register() {
		panic("Failed to register video-h264")
	}
	if !nvvideoh264.Register() {
		panic("Failed to register nv-video-h264")
	}

	if !livekitcompositor.Register() {
		panic("Failed to register livekitcompositor")
	}

	if !sipcompositor.Register() {
		panic("Failed to register sipcompositor")
	}

	if !dtmfaudio.Register() {
		panic("Failed to register dtmf-audio")
	}

	if !samplewriter.Register() {
		panic("Failed to register samplewriter")
	}

	if !pcm16audio.Register() {
		panic("Failed to register pcm16audio")
	}

	if !rtpcapscodecfilter.Register() {
		panic("Failed to register rtpcapscodecfilter")
	}

	if !h264rtppaybin.Register() {
		panic("Failed to register h264rtppaybin")
	}

	if !bfcpserver.Register() {
		panic("Failed to register bfcpserver")
	}

	if !audiopcmu.Register() {
		panic("Failed to register audio-pcmu")
	}

	if !pcmuaudio.Register() {
		panic("Failed to register pcmu-audio")
	}

	if !audiopcma.Register() {
		panic("Failed to register audio-pcma")
	}

	if !pcmaaudio.Register() {
		panic("Failed to register pcma-audio")
	}

	if !nvav1video.Register() {
		panic("Failed to register nv-av1-video")
	}

	if !nvvideoav1.Register() {
		panic("Failed to register nv-video-av1")
	}

	if !vp9video.Register() {
		panic("Failed to register vp9-video")
	}

	if !videovp9.Register() {
		panic("Failed to register video-vp9")
	}

	if !nvvp9video.Register() {
		panic("Failed to register nv-vp9-video")
	}

	if !nvvideovp9.Register() {
		panic("Failed to register nv-video-vp9")
	}

	if !av1video.Register() {
		panic("Failed to register av1-video")
	}

	if !videoav1.Register() {
		panic("Failed to register video-av1")
	}

	if !factorybin.Register() {
		panic("Failed to register factorybin")
	}

	MainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)

	// C.setup_gst_log_handler()

	go MainLoop.Run()
}
