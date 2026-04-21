/*
 * HopSrc - GstBaseSrc that's externally driven via hop_src_push_buffer() /
 * hop_src_push_event(). Zero thread hop: the paired HopSink's render() and
 * event() callbacks push synchronously on the upstream caller's thread.
 *
 * GstBaseSrc is fundamentally task-based — its loop emits stream-start,
 * then calls create(). We can't avoid the task entirely, but we make its
 * first iteration exit immediately: create() signals "ready" and returns
 * GST_FLOW_FLUSHING, so stream-start is sent (good — establishes the pad
 * as active) and no segment is emitted (good — the real upstream segment
 * will be forwarded by HopSink, preserving correct sticky-event order:
 * stream-start → caps → segment → buffers).
 *
 * The "ready" flag lets HopSink's event/render wait briefly at startup
 * until basesrc has sent stream-start, then every subsequent forward
 * happens with no blocking on the hot path.
 */

#include "hop_src.h"
#include <gst/base/gstbasesrc.h>

struct _HopSrc {
    GstBaseSrc  parent;
    GMutex      lock;
    GCond       cond;
    gint        ready;    /* atomic: 1 once basesrc has emitted stream-start */
    gboolean    flushing;
};

G_DEFINE_TYPE(HopSrc, hop_src, GST_TYPE_BASE_SRC)

static GstStaticPadTemplate src_template =
    GST_STATIC_PAD_TEMPLATE("src",
                            GST_PAD_SRC,
                            GST_PAD_ALWAYS,
                            GST_STATIC_CAPS_ANY);

static gboolean
hop_src_start(GstBaseSrc *bs)
{
    HopSrc *s = HOP_SRC(bs);
    g_mutex_lock(&s->lock);
    s->flushing = FALSE;
    g_atomic_int_set(&s->ready, 0);
    g_mutex_unlock(&s->lock);
    return TRUE;
}

static GstFlowReturn
hop_src_create(GstBaseSrc *bs, guint64 offset, guint size, GstBuffer **buf)
{
    (void) offset; (void) size; (void) buf;
    HopSrc *s = HOP_SRC(bs);
    /* basesrc has already pushed stream-start by the time create() is
     * first called. Mark ready and bail out so basesrc does NOT push its
     * own segment — the real upstream segment will come through HopSink. */
    g_mutex_lock(&s->lock);
    g_atomic_int_set(&s->ready, 1);
    g_cond_broadcast(&s->cond);
    g_mutex_unlock(&s->lock);
    return GST_FLOW_FLUSHING;
}

static void
wait_for_ready(HopSrc *s)
{
    if (G_LIKELY(g_atomic_int_get(&s->ready))) return;
    g_mutex_lock(&s->lock);
    while (!g_atomic_int_get(&s->ready) && !s->flushing)
        g_cond_wait(&s->cond, &s->lock);
    g_mutex_unlock(&s->lock);
}

static gboolean
hop_src_unlock(GstBaseSrc *bs)
{
    HopSrc *s = HOP_SRC(bs);
    g_mutex_lock(&s->lock);
    s->flushing = TRUE;
    g_cond_broadcast(&s->cond);
    g_mutex_unlock(&s->lock);
    return TRUE;
}

static gboolean
hop_src_unlock_stop(GstBaseSrc *bs)
{
    HopSrc *s = HOP_SRC(bs);
    g_mutex_lock(&s->lock);
    s->flushing = FALSE;
    g_mutex_unlock(&s->lock);
    return TRUE;
}

GstFlowReturn
hop_src_push_buffer(HopSrc *s, GstBuffer *buf)
{
    wait_for_ready(s);
    if (G_UNLIKELY(s->flushing)) { gst_buffer_unref(buf); return GST_FLOW_FLUSHING; }
    return gst_pad_push(GST_BASE_SRC_PAD(s), buf);
}

gboolean
hop_src_push_event(HopSrc *s, GstEvent *ev)
{
    wait_for_ready(s);
    if (G_UNLIKELY(s->flushing)) { gst_event_unref(ev); return FALSE; }
    return gst_pad_push_event(GST_BASE_SRC_PAD(s), ev);
}

static void
hop_src_finalize(GObject *obj)
{
    HopSrc *s = HOP_SRC(obj);
    g_mutex_clear(&s->lock);
    g_cond_clear(&s->cond);
    G_OBJECT_CLASS(hop_src_parent_class)->finalize(obj);
}

static void
hop_src_class_init(HopSrcClass *klass)
{
    GObjectClass    *gc = G_OBJECT_CLASS(klass);
    GstElementClass *ec = GST_ELEMENT_CLASS(klass);
    GstBaseSrcClass *bc = GST_BASE_SRC_CLASS(klass);

    gc->finalize    = hop_src_finalize;
    bc->start       = hop_src_start;
    bc->create      = hop_src_create;
    bc->unlock      = hop_src_unlock;
    bc->unlock_stop = hop_src_unlock_stop;

    gst_element_class_add_static_pad_template(ec, &src_template);
    gst_element_class_set_static_metadata(ec,
        "Hop Src", "Source/Generic",
        "Minimal src: externally driven via hop_src_push_buffer()",
        "bench");
}

static void
hop_src_init(HopSrc *s)
{
    g_mutex_init(&s->lock);
    g_cond_init(&s->cond);
    /* Intentionally NOT live: a live basesrc would park its task in
     * gst_base_src_wait_playing() until the pipeline hit PLAYING. Upstream
     * (fakesrc, non-live) starts pushing stream-start/caps in PAUSED,
     * which the sink tries to forward and which would wait on our "ready"
     * flag — classic deadlock. Running non-live lets the task run its
     * first iteration immediately in PAUSED: stream-start is emitted,
     * create() returns FLUSHING, ready is set, task exits. Downstream
     * (fakesink) must still set async=FALSE so it doesn't require
     * preroll — we document that in the bench drivers. */
    gst_base_src_set_format(GST_BASE_SRC(s), GST_FORMAT_TIME);
}
