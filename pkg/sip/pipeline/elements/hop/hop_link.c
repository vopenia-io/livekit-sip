#include "hop_link.h"
#include "hop_src.h"
#include "hop_sink.h"

struct _HopLink
{
    gint refcount;
    GMutex lock;
    GstPad *sink_pad; /* hopsink's sink pad */
    GstPad *src_pad;  /* hopsrc's src pad */
};

HopLink *
hop_link_new(void)
{
    HopLink *l = g_new0(HopLink, 1);
    g_atomic_int_set(&l->refcount, 1);
    g_mutex_init(&l->lock);
    return l;
}

HopLink *
hop_link_ref(HopLink *l)
{
    g_atomic_int_inc(&l->refcount);
    return l;
}

void
hop_link_unref(HopLink *l)
{
    if (!g_atomic_int_dec_and_test(&l->refcount))
        return;
    g_mutex_clear(&l->lock);
    g_free(l);
}

void
hop_link_set_pad(HopLink *l, GstPadDirection dir, GstPad *pad)
{
    g_mutex_lock(&l->lock);
    if (dir == GST_PAD_SINK)
        l->sink_pad = pad;
    else if (dir == GST_PAD_SRC)
        l->src_pad = pad;
    g_mutex_unlock(&l->lock);
}

GstPad *
hop_link_acquire_partner(HopLink *l, GstPadDirection my_dir)
{
    GstPad *partner = NULL;
    g_mutex_lock(&l->lock);
    if (my_dir == GST_PAD_SINK)
        partner = l->src_pad;
    else if (my_dir == GST_PAD_SRC)
        partner = l->sink_pad;
    if (partner)
        gst_object_ref(partner);
    g_mutex_unlock(&l->lock);
    return partner;
}

/* Element-side helpers — defined in hop_src.c / hop_sink.c — that swap
 * the element's current link for a new shared one and re-register the
 * element's pad in the new link. */
extern void _hop_src_replace_link(GstElement *src, HopLink *l);
extern void _hop_sink_replace_link(GstElement *sink, HopLink *l);

gboolean
hop_pair_bind(GstElement *sink, GstElement *src)
{
    if (!HOP_IS_SINK(sink) || !HOP_IS_SRC(src))
        return FALSE;

    HopLink *shared = hop_link_new();
    _hop_sink_replace_link(sink, shared);
    _hop_src_replace_link(src, shared);
    /* After replace_link, both elements own a ref. Drop ours. */
    hop_link_unref(shared);
    return TRUE;
}
