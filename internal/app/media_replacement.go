package app

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

// HTTP accepts owner-scoped source identities, never operator filesystem paths.
// A project grant covers its primary recording only. Binding an additional
// recording requires the owner session until secondary grants are explicit.
func checkHTTPRenderReference(ctx context.Context, source Source, request ExportRequest) error {
	v := request.AudioReplace
	if v == nil {
		return nil
	}
	if v.Input != "" || v.SourceID == "" {
		return &sourceProblem{code: "invalid_argument", message: "HTTP audio_replace requires source_id; local input paths are unavailable"}
	}
	if v.SourceID == source.ID {
		return &sourceProblem{code: "invalid_argument", message: "audio_replace must reference a separate uploaded recording"}
	}
	if source.Path == "" || source.Kind != "upload" {
		return &sourceProblem{code: "invalid_argument", message: "audio_replace requires an uploaded primary recording"}
	}
	if scope, ok := ctx.Value(projectScopeKey{}).(projectScope); ok && scope.Actor != source.Owner {
		return &sourceProblem{code: "invalid_argument", message: "Adding replacement audio requires the project owner's session"}
	}
	return nil
}

// The caller already holds decoder admission for the whole inspection/render.
// Acquiring another decoder permit here could deadlock a full-pool operation.
func (s *Server) pinExportReplacement(ctx context.Context, source Source, request ExportRequest) (context.Context, func(), error) {
	if err := checkHTTPRenderReference(ctx, source, request); err != nil {
		return ctx, func() {}, err
	}
	if request.AudioReplace == nil {
		return ctx, func() {}, nil
	}
	audio, err := s.databaseSource(ctx, request.AudioReplace.SourceID, source.Owner)
	if err != nil {
		return ctx, func() {}, err
	}
	if audio.MediaRemoved {
		return ctx, func() {}, ErrSourceMediaRemoved
	}
	if audio.Path == "" || audio.Kind != "upload" {
		return ctx, func() {}, &sourceProblem{code: "invalid_argument", message: "Replacement audio requires an uploaded recording"}
	}
	readerCtx, releaseReader, err := s.pinSourceMediaReader(ctx, audio)
	if err != nil {
		return ctx, func() {}, err
	}
	bound, releaseBinding, err := localmedia.WithAudioReplacement(readerCtx, audio.Path)
	if err != nil {
		releaseReader()
		return ctx, func() {}, localMediaError(err)
	}
	return bound, func() { releaseBinding(); releaseReader() }, nil
}

func validateHTTPCollectionInputs(plan CollectionPlan) error {
	if plan.Options.AudioReplace != nil && plan.Options.AudioReplace.Input != "" {
		return collectionInvalid("", "options.audio_replace.input", "HTTP plans cannot contain replacement filesystem inputs")
	}
	for _, clip := range plan.Clips {
		if clip.Options.AudioReplace != nil && clip.Options.AudioReplace.Input != "" {
			return collectionInvalid(clip.ID, "options.audio_replace.input", "HTTP plans cannot contain replacement filesystem inputs")
		}
	}
	return nil
}
