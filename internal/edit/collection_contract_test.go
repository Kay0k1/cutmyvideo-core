package edit

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// This corpus is also consumed by OpenAPI and both installed SDK suites.
// Their guarantees differ: the schema checks shape, SDKs preserve portable
// edits, and the core resolves inheritance and enforces source/media budgets.
func TestSharedCollectionContractExamples(t *testing.T) {
	type collectionRequestCase struct {
		SourceID  string         `json:"source_id"`
		Plan      CollectionPlan `json:"plan"`
		ClipIDs   []string       `json:"clip_ids,omitempty"`
		Rendition string         `json:"rendition,omitempty"`
	}
	type options struct {
		Format     string `json:"format"`
		Quality    string `json:"quality"`
		CutMode    string `json:"cut_mode"`
		NoAudio    bool   `json:"no_audio"`
		AudioTrack *int   `json:"audio_track"`
		Crop       *Crop  `json:"crop"`
	}
	var corpus struct {
		SchemaVersion int `json:"schema_version"`
		Cases         []struct {
			ID      string                `json:"id"`
			Request collectionRequestCase `json:"request"`
			Valid   *bool                 `json:"valid"`
			Source  struct {
				DurationMS *int64 `json:"duration_ms"`
			} `json:"source"`
			Limits struct {
				MaxRanges  *int   `json:"max_ranges"`
				MaxRangeMS *int64 `json:"max_range_ms"`
				MaxJobMS   *int64 `json:"max_job_ms"`
			} `json:"limits"`
			Selected []string           `json:"selected_clip_ids"`
			Options  map[string]options `json:"effective_options"`
		} `json:"collection_cases"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "testdata", "project-contracts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.SchemaVersion != 1 || len(corpus.Cases) == 0 {
		t.Fatal("missing versioned collection contract cases")
	}
	seen := map[string]bool{}
	for _, example := range corpus.Cases {
		if example.ID == "" || seen[example.ID] {
			t.Fatalf("missing or duplicate case ID %q", example.ID)
		}
		seen[example.ID] = true
		if example.Valid == nil {
			t.Fatalf("case %s needs an explicit Go validity expectation", example.ID)
		}
		t.Run(example.ID, func(t *testing.T) {
			config := Limits{MaxRanges: 128, MaxRangeMS: 120000, MaxJobMS: 600000}
			source := Source{ID: example.Request.SourceID, HasLocalMedia: true, DurationMS: 120000}
			if example.Source.DurationMS != nil {
				source.DurationMS = *example.Source.DurationMS
			}
			if example.Limits.MaxRanges != nil {
				config.MaxRanges = *example.Limits.MaxRanges
			}
			if example.Limits.MaxRangeMS != nil {
				config.MaxRangeMS = *example.Limits.MaxRangeMS
			}
			if example.Limits.MaxJobMS != nil {
				config.MaxJobMS = *example.Limits.MaxJobMS
			}
			before, err := json.Marshal(example.Request)
			if err != nil {
				t.Fatal(err)
			}
			clips, err := NormalizeCollection(example.Request.Plan, CollectionExecution{
				ClipIDs: example.Request.ClipIDs, Rendition: example.Request.Rendition,
			}, config, source)
			after, marshalErr := json.Marshal(example.Request)
			if marshalErr != nil || !bytes.Equal(before, after) {
				t.Fatal("normalization changed the reusable request")
			}
			if !*example.Valid {
				var fieldError *CollectionFieldError
				if !errors.As(err, &fieldError) {
					t.Fatalf("expected a collection validation error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0, len(clips))
			actual := make(map[string]options, len(clips))
			for _, clip := range clips {
				ids = append(ids, clip.ID)
				request := clip.Request
				actual[clip.ID] = options{request.Format, request.Quality, request.CutMode,
					request.NoAudio, request.AudioTrack, request.Crop}
				if request.SourceID != source.ID || request.OutputMode != "joined" {
					t.Fatalf("clip %s lost its source scope or joined output: %+v", clip.ID, request)
				}
			}
			if !reflect.DeepEqual(ids, example.Selected) {
				t.Fatalf("selected clip order: got %v, want %v", ids, example.Selected)
			}
			if !reflect.DeepEqual(actual, example.Options) {
				t.Fatalf("effective options: got %+v, want %+v", actual, example.Options)
			}
		})
	}
}
