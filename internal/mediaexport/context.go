package mediaexport

import "context"

type collectionRenditionKey struct{}

// CollectionRenderContext propagates the rendition into the shared encoder.
// It does not change ranges, crop or audio decisions. Both local exports and
// queued HTTP workers use this same context and draft encoder settings.
func CollectionRenderContext(ctx context.Context, rendition string) context.Context {
	return context.WithValue(ctx, collectionRenditionKey{}, rendition == "draft")
}

func CollectionDraft(ctx context.Context) bool {
	value, _ := ctx.Value(collectionRenditionKey{}).(bool)
	return value
}
