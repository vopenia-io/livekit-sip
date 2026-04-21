#pragma once

#include <gst/gst.h>
#include <gst/base/gstbasesrc.h>

G_BEGIN_DECLS

#define HOP_TYPE_SRC (hop_src_get_type())
G_DECLARE_FINAL_TYPE(HopSrc, hop_src, HOP, SRC, GstBaseSrc)

/* Push a buffer directly out this src's src pad. Takes ownership of buf.
 * Blocks briefly on first use, until the basesrc task has emitted
 * stream-start. */
GstFlowReturn hop_src_push_buffer(HopSrc *src, GstBuffer *buf);

/* Push an event directly out this src's src pad. Takes ownership of ev.
 * Same "wait until ready" semantics as hop_src_push_buffer. */
gboolean      hop_src_push_event(HopSrc *src, GstEvent *ev);

G_END_DECLS
