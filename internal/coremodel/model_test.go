package coremodel

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

func TestCoreModelPreservesLegacyValidationAndShapes(t *testing.T) {
	c := Config{MaxRanges: 4, MaxRangeMS: 1000, MaxJobMS: 2000}
	source := Source{ID: "source", Path: "local.media", DurationMS: 2000}
	request := ExportRequest{SourceID: "source", Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "separate", SubtitleMode: "omit", Rendition: "draft", Ranges: []edit.Range{{StartMS: 100, EndMS: 100, Label: "  trimmed  "}}}
	err := request.Validate(c, source)
	if err == nil || err.Error() != `range 1 ("trimmed"): set end_ms after start_ms (100)` {
		t.Fatal("validation order or diagnostic changed", err)
	}
	if request.Quality != "720p" || request.OutputMode != "" || request.SubtitleMode != "" || request.Ranges[0].Label != "trimmed" {
		t.Fatal("partial normalization lost", request)
	}
	if EditSource(Source{URL: "https://example.invalid/video"}).HasLocalMedia || !EditSource(source).HasLocalMedia {
		t.Fatal("local source projection changed")
	}
	request.Ranges[0].EndMS = 900
	if err := request.Validate(c, source); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"source_id":"source","ranges":[{"start_ms":100,"end_ms":900,"label":"trimmed"}],"format":"mp4","quality":"720p","cut_mode":"accurate","rendition":"draft"}` {
		t.Fatal("request JSON changed", string(data))
	}
	expiry := time.Unix(1700000000, 0).UTC()
	source = Source{Fingerprint: &SourceFingerprint{Kind: "upload", SHA256: "digest", SizeBytes: 12}, ExpiresAt: &expiry, Tracks: []edit.Track{{Index: 2, Type: "audio", Codec: "aac"}}, Path: "private-path", Owner: "private-owner", DisplayWidth: 1920, PreparationClaim: "private-claim"}
	data, err = json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	if string(object["fingerprint"]) != `{"kind":"upload","sha256":"digest","size_bytes":12}` || string(object["tracks"]) != `[{"index":2,"type":"audio","codec":"aac","default":false}]` {
		t.Fatal("source public metadata changed", string(data))
	}
	for _, key := range []string{"Path", "path", "Owner", "owner", "display_width", "PreparationClaim", "preparation_claim"} {
		if _, ok := object[key]; ok {
			t.Fatal("private source field serialized", key)
		}
	}
	for _, name := range []string{"expires_at", "provider_video_id", "source_url", "preview_url", "embed_url", "thumbnail_url"} {
		if _, ok := object[name]; !ok {
			t.Fatal("nullable source field omitted", name)
		}
	}
	// The full compatibility carrier still accepts service settings alongside the
	// local settings; aliases must not substitute a narrower engine config.
	c = Config{DatabaseURL: "db", WorkerHealthPath: "health", WorkerPreparationConcurrency: 2, UploadTimeout: time.Hour, MaxOutputBytes: 42, FFmpegThreads: 3, TrustProxy: true}
	if !reflect.DeepEqual(EditLimits(c), edit.Limits{}) || c.WorkerPreparationConcurrency != 2 || c.UploadTimeout != time.Hour {
		t.Fatal("service carrier fields changed")
	}
}
