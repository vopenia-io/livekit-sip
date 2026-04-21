/*
 * HopSink - minimal GstBaseSink paired with HopSrc. render() forwards
 * the received buffer directly to the paired HopSrc via
 * hop_src_push_buffer(), so the whole chain runs on the caller's thread
 * — no queue, no thread hop. Set the paired src via the "src" property
 * before PLAYING.
 */

#include "hop_sink.h"
#include "hop_src.h"

struct _HopSink {
    GstBaseSink parent;
    HopSrc     *src;   /* held ref; set via "src" property */
};

G_DEFINE_TYPE(HopSink, hop_sink, GST_TYPE_BASE_SINK)

enum { PROP_0, PROP_SRC };

static GstStaticPadTemplate sink_template =
    GST_STATIC_PAD_TEMPLATE("sink",
                            GST_PAD_SINK,
                            GST_PAD_ALWAYS,
                            GST_STATIC_CAPS_ANY);

static void
hop_sink_set_property(GObject *obj, guint id, const GValue *val, GParamSpec *p)
{
    HopSink *s = HOP_SINK(obj);
    if (id == PROP_SRC) {
        if (s->src) { g_object_unref(s->src); s->src = NULL; }
        GObject *o = g_value_get_object(val);
        if (o) s->src = HOP_SRC(g_object_ref(o));
    } else {
        G_OBJECT_WARN_INVALID_PROPERTY_ID(obj, id, p);
    }
}

static void
hop_sink_get_property(GObject *obj, guint id, GValue *val, GParamSpec *p)
{
    HopSink *s = HOP_SINK(obj);
    if (id == PROP_SRC) g_value_set_object(val, s->src);
    else G_OBJECT_WARN_INVALID_PROPERTY_ID(obj, id, p);
}

static GstFlowReturn
hop_sink_render(GstBaseSink *bs, GstBuffer *buf)
{
    HopSink *s = HOP_SINK(bs);
    return hop_src_push_buffer(s->src, gst_buffer_ref(buf));
}

/* Forward events to the paired src's pad so they reach downstream.
 * STREAM_START is already emitted by the basesrc task before it exits —
 * forwarding it too would duplicate. Everything else (CAPS, SEGMENT, TAG,
 * custom downstream events, EOS) is forwarded, preserving upstream's
 * sticky-event order. basesink's parent event handler is still called so
 * it keeps its own state in sync and posts EOS on the bus as normal. */
static gboolean
hop_sink_event(GstBaseSink *bs, GstEvent *ev)
{
    HopSink *s = HOP_SINK(bs);
    if (s->src && GST_EVENT_TYPE(ev) != GST_EVENT_STREAM_START)
        hop_src_push_event(s->src, gst_event_ref(ev));

    return GST_BASE_SINK_CLASS(hop_sink_parent_class)->event(bs, ev);
}

static void
hop_sink_finalize(GObject *obj)
{
    HopSink *s = HOP_SINK(obj);
    if (s->src) g_object_unref(s->src);
    G_OBJECT_CLASS(hop_sink_parent_class)->finalize(obj);
}

static void
hop_sink_class_init(HopSinkClass *klass)
{
    GObjectClass     *gc = G_OBJECT_CLASS(klass);
    GstElementClass  *ec = GST_ELEMENT_CLASS(klass);
    GstBaseSinkClass *bc = GST_BASE_SINK_CLASS(klass);

    gc->set_property = hop_sink_set_property;
    gc->get_property = hop_sink_get_property;
    gc->finalize     = hop_sink_finalize;
    bc->render       = hop_sink_render;
    bc->event        = hop_sink_event;

    g_object_class_install_property(gc, PROP_SRC,
        g_param_spec_object("src", "Src",
            "Peer HopSrc element to forward buffers to",
            HOP_TYPE_SRC,
            G_PARAM_READWRITE | G_PARAM_STATIC_STRINGS));

    gst_element_class_add_static_pad_template(ec, &sink_template);
    gst_element_class_set_static_metadata(ec,
        "Hop Sink", "Sink/Generic",
        "Minimal sink: forwards buffers synchronously to a paired HopSrc",
        "bench");
}

static void
hop_sink_init(HopSink *s)
{
    (void) s;
    gst_base_sink_set_sync(GST_BASE_SINK(s), FALSE);
    gst_base_sink_set_async_enabled(GST_BASE_SINK(s), FALSE);
}
