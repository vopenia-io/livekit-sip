#pragma once

#include <gst/gst.h>

G_BEGIN_DECLS

/* Shared, ref-counted connective tissue between a HopSink and a HopSrc.
 *
 * Each element holds one ref. The link stores raw pointers to the two
 * pads (no refcount on the pads themselves) — each element clears its
 * own slot under .lock during dispose, while the pad is still alive.
 *
 * hop_link_acquire_partner() takes the lock briefly, reads the partner
 * pad pointer, and gst_object_ref()s it before releasing the lock, so
 * the partner pad survives the forward operation even if the partner
 * element disposes concurrently.
 *
 * The lock is NEVER held across a gst_pad_push or peer_query call. */

typedef struct _HopLink HopLink;

HopLink *hop_link_new(void);
HopLink *hop_link_ref(HopLink *l);
void     hop_link_unref(HopLink *l);

/* Set or clear (pad == NULL) the slot for the given direction. */
void     hop_link_set_pad(HopLink *l, GstPadDirection dir, GstPad *pad);

/* Returns a ref'd partner pad, or NULL if the partner is gone.
 * my_dir is the direction of the *caller's* pad — partner is the other one. */
GstPad  *hop_link_acquire_partner(HopLink *l, GstPadDirection my_dir);

/* Bind two freshly-created hop elements into a pair. Releases each
 * element's per-init throwaway link and replaces it with a fresh shared
 * one. Returns FALSE if either element is the wrong type. */
gboolean hop_pair_bind(GstElement *sink, GstElement *src);

G_END_DECLS
