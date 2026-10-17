package localmedia

import (
	"context"
	"github.com/Kay0k1/cutmyvideo-core/internal/coremodel"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
)

type CollectionPlan = edit.CollectionPlan
type CollectionClip = edit.CollectionClip
type CollectionOptions = edit.CollectionOptions
type CollectionExecution = edit.CollectionExecution
type CollectionFieldError = edit.CollectionFieldError

type PreparedCollectionClip struct {
	ID          string        `json:"id"`
	Label       string        `json:"label,omitempty"`
	Request     ExportRequest `json:"request"`
	RequestedMS int64         `json:"requested_duration_ms"`
}

// Keep the established app diagnostic chain while shared validation remains
// independent of service/media error types. Nested field locations are retained.
func editCompatibilityError(err error) error {
	switch value := err.(type) {
	case *edit.ArgumentError:
		return &mediaerror.Problem{Code: "invalid_argument", Message: value.Message}
	case *edit.CollectionFieldError:
		return &CollectionFieldError{ClipID: value.ClipID, Field: value.Field, Err: editCompatibilityError(value.Err)}
	default:
		return err
	}
}

func NormalizeCollectionRendition(value string) (string, error) {
	rendition, err := edit.NormalizeCollectionRendition(value)
	return rendition, editCompatibilityError(err)
}

func NormalizeCollection(plan CollectionPlan, execution CollectionExecution, c Config, source Source) ([]PreparedCollectionClip, error) {
	prepared, err := edit.NormalizeCollection(plan, execution, coremodel.EditLimits(c), coremodel.EditSource(source))
	if err != nil {
		return nil, editCompatibilityError(err)
	}
	var result []PreparedCollectionClip
	for _, clip := range prepared {
		result = append(result, PreparedCollectionClip{ID: clip.ID, Label: clip.Label, Request: ExportRequest(clip.Request), RequestedMS: clip.RequestedMS})
	}
	return result, nil
}

func CollectionOutputFilename(clip PreparedCollectionClip, rendition string) string {
	return edit.CollectionOutputFilename(edit.PreparedCollectionClip{ID: clip.ID, Label: clip.Label,
		Request: edit.ExportRequest(clip.Request), RequestedMS: clip.RequestedMS}, rendition)
}

// CollectionRenderContext propagates the rendition into the shared encoder.
// It does not change ranges, crop or audio decisions. Both local exports and
// queued HTTP workers use this same context and draft encoder settings.
func CollectionRenderContext(ctx context.Context, rendition string) context.Context {
	return mediaexport.CollectionRenderContext(ctx, rendition)
}

func CollectionDraft(ctx context.Context) bool {
	return mediaexport.CollectionDraft(ctx)
}
