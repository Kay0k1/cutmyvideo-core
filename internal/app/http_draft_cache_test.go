package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type draftCacheSink struct {
	*httptest.ResponseRecorder
	fromCalls, flushCalls int
	afterRead             func()
	readErr, flushErr     error
	deadline              time.Time
}

func (w *draftCacheSink) ReadFrom(r io.Reader) (int64, error) {
	w.fromCalls++
	n, err := io.Copy(struct{ io.Writer }{w.ResponseRecorder}, r)
	if w.afterRead != nil {
		w.afterRead()
	}
	if err == nil {
		err = w.readErr
	}
	return n, err
}
func (w *draftCacheSink) FlushError() error                      { w.flushCalls++; return w.flushErr }
func (w *draftCacheSink) SetWriteDeadline(value time.Time) error { w.deadline = value; return nil }

type draftCacheUnwrapper struct{ http.ResponseWriter }

func (w draftCacheUnwrapper) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Optional interfaces are delegated deliberately, just as production wrappers
// must do; Unwrap alone does not promise an underlying ReaderFrom fast path.
type draftCacheReaderFromWrapper struct{ http.ResponseWriter }

func (w draftCacheReaderFromWrapper) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w draftCacheReaderFromWrapper) ReadFrom(r io.Reader) (int64, error) {
	return w.ResponseWriter.(io.ReaderFrom).ReadFrom(r)
}

type draftCacheVoidFlusher struct{ http.ResponseWriter }

func (w draftCacheVoidFlusher) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w draftCacheVoidFlusher) Flush()                      {}

func TestDraftCacheWrappedFullTransferAndFlushError(t *testing.T) {
	payload := bytes.Repeat([]byte("wrapped-draft"), 1024)
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unwrap", "reader-from", "body-guard", "wrapped-flush-error", "void-flusher"} {
		t.Run(name, func(t *testing.T) {
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			sink := &draftCacheSink{ResponseRecorder: httptest.NewRecorder()}
			sink.Header().Set("Content-Type", "video/mp4")
			var wrapped http.ResponseWriter = draftCacheUnwrapper{sink}
			wantFrom, wantFlush, wantHint := 0, 1, 1
			switch name {
			case "reader-from":
				wrapped = draftCacheReaderFromWrapper{sink}
				wantFrom = 1
			case "body-guard":
				wrapped = &bodyGuardResponseWriter{ResponseWriter: sink, body: &trackedRequestBody{complete: true}, http1: true}
			case "wrapped-flush-error":
				sink.flushErr = errors.New("buffered tail failed")
				wantHint = 0
			case "void-flusher":
				wrapped = draftCacheVoidFlusher{sink}
				wantFlush, wantHint = 0, 0
			}
			deadline := time.Now().Add(time.Minute)
			if http.NewResponseController(wrapped).SetWriteDeadline(deadline) != nil || !sink.deadline.Equal(deadline) {
				t.Fatal("wrapped writer hides response deadline")
			}
			calls := 0
			r := httptest.NewRequest(http.MethodGet, "/download", nil)
			serveDraftArtifactContent(wrapped, r, "clip.mp4", info, file, func(opened *os.File) {
				calls++
				if opened != file {
					t.Fatal("wrapped transfer changed descriptor")
				}
			})
			if calls != wantHint || sink.flushCalls != wantFlush || sink.fromCalls != wantFrom || sink.Code != 200 || !bytes.Equal(sink.Body.Bytes(), payload) {
				t.Fatal("wrapped full-transfer observation changed", calls, sink.flushCalls, sink.fromCalls)
			}
		})
	}
}

func TestDraftCacheTransferWriterPreservesInterfacesAndCountsErrors(t *testing.T) {
	sink := &draftCacheSink{ResponseRecorder: httptest.NewRecorder()}
	writer := &draftTransferWriter{ResponseWriter: sink}
	deadline := time.Now().Add(time.Minute)
	if http.NewResponseController(writer).SetWriteDeadline(deadline) != nil || !sink.deadline.Equal(deadline) || writer.Unwrap() != sink {
		t.Fatal("response controller no longer reaches original writer")
	}
	if n, err := io.CopyN(writer, bytes.NewReader([]byte("clip")), 4); n != 4 || err != nil || sink.fromCalls != 1 || writer.written != 4 {
		t.Fatal("ReadFrom was bypassed or counted twice", n, err)
	}
	if n, err := writer.Write([]byte("xx")); n != 2 || err != nil || writer.written != 6 {
		t.Fatal("ordinary Write was not counted", n, err)
	}
	if http.NewResponseController(writer).Flush() != nil || sink.flushCalls != 1 {
		t.Fatal("FlushError delegation changed")
	}
	// CopyN normalizes a full-length read error to nil; our tracker must retain it.
	sink = &draftCacheSink{ResponseRecorder: httptest.NewRecorder(), readErr: io.ErrUnexpectedEOF}
	writer = &draftTransferWriter{ResponseWriter: sink}
	if n, err := io.CopyN(writer, bytes.NewReader([]byte("clip")), 4); n != 4 || err != nil || !writer.failed {
		t.Fatal("full-length transfer error was forgotten", n, err)
	}
	wrapped := &draftTransferWriter{ResponseWriter: draftCacheUnwrapper{httptest.NewRecorder()}}
	if n, err := wrapped.ReadFrom(bytes.NewReader([]byte("fallback"))); n != 8 || err != nil || wrapped.written != 8 {
		t.Fatal("fallback ReadFrom recursed or changed bytes", n, err)
	}
}

func TestDraftCacheFullTransferGuards(t *testing.T) {
	for _, name := range []string{"full", "range", "range-if-stale", "empty-range", "head", "not-modified", "precondition", "cancel", "cancel-after-copy", "copy-error", "flush-error", "encoding", "late-encoding", "mutation", "no-error-flush"} {
		t.Run(name, func(t *testing.T) {
			payload := bytes.Repeat([]byte("immutable-draft"), 1024)
			path := filepath.Join(t.TempDir(), "clip.mp4")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, "/download", nil).WithContext(ctx)
			sink := &draftCacheSink{ResponseRecorder: httptest.NewRecorder()}
			var target http.ResponseWriter = sink
			sink.Header().Set("Content-Type", "video/mp4")
			switch name {
			case "range":
				r.Header.Set("Range", "bytes=1-5")
			case "range-if-stale":
				r.Header.Set("Range", "bytes=1-5")
				r.Header.Set("If-Range", "invalid")
			case "empty-range":
				r.Header.Set("Range", "")
			case "head":
				r.Method = http.MethodHead
			case "not-modified":
				r.Header.Set("If-Modified-Since", info.ModTime().Add(time.Hour).UTC().Format(http.TimeFormat))
			case "precondition":
				r.Header.Set("If-Match", `"different"`)
			case "cancel":
				cancel()
			case "cancel-after-copy":
				sink.afterRead = cancel
			case "copy-error":
				sink.readErr = io.ErrUnexpectedEOF
			case "flush-error":
				sink.flushErr = errors.New("write failed")
			case "encoding":
				sink.Header().Set("Content-Encoding", "gzip")
			case "late-encoding":
				sink.afterRead = func() { sink.Header().Set("Content-Encoding", "gzip") }
			case "mutation":
				sink.afterRead = func() {
					if err := file.Truncate(1); err != nil {
						t.Fatal(err)
					}
				}
			case "no-error-flush":
				target = sink.ResponseRecorder
			}
			calls := 0
			serveDraftArtifactContent(target, r, "clip.mp4", info, file, func(opened *os.File) {
				calls++
				if opened != file {
					t.Fatal("hint reopened a pathname instead of using the served descriptor")
				}
			})
			if (calls == 1) != (name == "full") {
				t.Fatal("wrong reclaim eligibility", name, calls)
			}
			if name == "full" && (sink.Code != 200 || !bytes.Equal(sink.Body.Bytes(), payload) || sink.flushCalls != 1) {
				t.Fatal("full draft bytes/flush changed")
			}
			if name == "range" && (sink.Code != 206 || !bytes.Equal(sink.Body.Bytes(), payload[1:6])) {
				t.Fatal("range changed")
			}
			if name == "not-modified" && sink.Code != 304 {
				t.Fatal("conditional response changed")
			}
			if name == "precondition" && sink.Code != 412 {
				t.Fatal("precondition response changed")
			}
		})
	}
}

// These are ordinary installed-schema/HTTP guards, not workload-specific media
// timings. No kernel cache hint is needed to observe authorization/classification.
func TestDraftCacheMetadataOwnerLegacyAndScopedDownload(t *testing.T) {
	store, server, creator, owner, request := remoteCollectionFixture(t, 1)
	request.Rendition = "draft"
	submitted := collectionSubmissionReport(t, httpCollectionRequest(t, server, creator, "/api/v1/collections", request, "draft-download-key"), 202)
	job, token, err := store.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{ID: newID("art"), Filename: "draft.mp4", SizeBytes: 19}
	path := writeLedgerFile(t, server.Config, "outputs", artifact.ID+".mp4", 19)
	if err = store.AddArtifact(context.Background(), owner, job.ID, path, token, artifact); err != nil {
		t.Fatal(err)
	}
	job.Status, job.Stage = "succeeded", "finished"
	job.Items[0].Status, job.Items[0].Artifact = "succeeded", &artifact
	if err = store.SaveJob(context.Background(), job, token); err != nil {
		t.Fatal(err)
	}
	gotPath, gotName, draft, err := store.artifactDownloadMetadata(context.Background(), artifact.ID, owner)
	oldPath, oldName, oldErr := store.ArtifactPath(context.Background(), artifact.ID, owner)
	if err != nil || oldErr != nil || !draft || gotPath != oldPath || gotName != oldName {
		t.Fatal("download metadata changed existing owner lookup", err)
	}
	if _, _, _, err = store.artifactDownloadMetadata(context.Background(), artifact.ID, "different-owner"); !errors.Is(err, ErrNotFound) {
		t.Fatal("metadata escaped artifact owner", err)
	}
	// A misleading draft filename cannot turn legacy/final/unknown requests into drafts.
	for _, rendition := range []string{"", "final", "future"} {
		if _, err = store.DB.Exec(context.Background(), `UPDATE jobs SET request=jsonb_set(request,'{rendition}',to_jsonb($2::text)) WHERE id=$1`, job.ID, rendition); err != nil {
			t.Fatal(err)
		}
		if _, _, draft, err = store.artifactDownloadMetadata(context.Background(), artifact.ID, owner); err != nil || draft {
			t.Fatal("non-draft request inferred from filename", err)
		}
	}
	if _, err = store.DB.Exec(context.Background(), `UPDATE jobs SET request=request-'rendition' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, draft, err = store.artifactDownloadMetadata(context.Background(), artifact.ID, owner); err != nil || draft {
		t.Fatal("legacy missing rendition was treated as draft", err)
	}
	if _, err = store.DB.Exec(context.Background(), `UPDATE jobs SET request=jsonb_set(request,'{rendition}','"draft"'::jsonb) WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	// Only supported draft media metadata enables the hint. ZIP is streamed by
	// its separate route, never persisted as an MP4 artifact; reject unknown modes.
	originalRequest, err := json.Marshal(job.Request)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		field, value string
		want         bool
	}{
		{"format", "mp4", true}, {"format", "mp3", false}, {"format", "mkv", false}, {"format", "zip", false},
		{"cut_mode", "accurate", true}, {"cut_mode", "copy", false},
		{"output_mode", "", true}, {"output_mode", "separate", true}, {"output_mode", "joined", true},
		{"output_mode", "merged", false}, {"output_mode", "zip", false}, {"output_mode", "bundle", false},
	} {
		var changed map[string]any
		if err = json.Unmarshal(originalRequest, &changed); err != nil {
			t.Fatal(err)
		}
		changed[check.field] = check.value
		raw, marshalErr := json.Marshal(changed)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err = store.DB.Exec(context.Background(), `UPDATE jobs SET request=$2::jsonb WHERE id=$1`, job.ID, string(raw)); err != nil {
			t.Fatal(err)
		}
		if _, _, draft, err = store.artifactDownloadMetadata(context.Background(), artifact.ID, owner); err != nil || draft != check.want {
			t.Fatal("unsupported artifact classification", check.field, check.value, draft, err)
		}
	}
	for _, field := range []string{"format", "cut_mode"} {
		if _, err = store.DB.Exec(context.Background(), `UPDATE jobs SET request=$2::jsonb-$3::text WHERE id=$1`, job.ID, string(originalRequest), field); err != nil {
			t.Fatal(err)
		}
		if _, _, draft, err = store.artifactDownloadMetadata(context.Background(), artifact.ID, owner); err != nil || draft {
			t.Fatal("incomplete job metadata enabled hint", field, err)
		}
	}
	if _, err = store.DB.Exec(context.Background(), `UPDATE jobs SET request=$2::jsonb WHERE id=$1`, job.ID, string(originalRequest)); err != nil {
		t.Fatal(err)
	}
	global := "/api/v1/artifacts/" + artifact.ID + "/download"
	recipient := agentProjectCookie(71)
	if w := agentProjectRequest(t, server, nil, "GET", global, nil, nil); w.Code != 401 {
		t.Fatal("download lost session guard", w.Code)
	}
	if w := agentProjectRequest(t, server, recipient, "GET", global, nil, nil); w.Code != 404 {
		t.Fatal("download exposed another owner's artifact", w.Code)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if w := agentProjectRequest(t, server, creator, "GET", global, nil, nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatal("owner full download changed", w.Code)
	}
	if w := agentProjectRequest(t, server, creator, "HEAD", global, nil, nil); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("owner HEAD changed", w.Code)
	}
	project := agentProjectDecode(t, agentProjectRequest(t, server, creator, "POST", "/api/v1/projects", projectCreateRequest{
		SourceID: request.SourceID, Plan: request.Plan, Rendition: "draft", CollectionKey: "draft-download-project",
		ClipJobs: []projectClipJob{{ClipID: request.Plan.Clips[0].ID, JobID: submitted.Clips[0].Job.ID}},
	}, nil), 201)
	base := "/api/v1/projects/" + project.ID
	issue := agentProjectRequest(t, server, creator, "POST", base+"/handoffs", struct{}{}, nil)
	var handoff struct {
		Token string `json:"token"`
	}
	if issue.Code != 201 || json.Unmarshal(issue.Body.Bytes(), &handoff) != nil {
		t.Fatal("handoff issue failed")
	}
	if w := agentProjectRequest(t, server, recipient, "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": handoff.Token}, nil); w.Code != 200 {
		t.Fatal("handoff redeem failed", w.Code)
	}
	// Editing the project's current rendition cannot relabel an older artifact.
	agentProjectDecode(t, agentProjectRequest(t, server, creator, "PUT", base, projectEditRequest{Revision: project.Revision, Plan: project.Plan, Rendition: "final"}, nil), 200)
	if _, _, draft, err = store.artifactDownloadMetadata(context.Background(), artifact.ID, owner); err != nil || !draft {
		t.Fatal("mutable project rendition relabeled original draft", err)
	}
	if w := agentProjectRequest(t, server, recipient, "GET", base+"/artifacts/"+artifact.ID+"/download", nil, nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatal("scoped full download changed", w.Code)
	}
	if w := agentProjectRequest(t, server, recipient, "GET", global, nil, nil); w.Code != 404 {
		t.Fatal("project grant became owner-wide", w.Code)
	}
	// Both ordinary and scoped ZIP retain the original draft bytes and archive
	// dispatch. Neither path calls the ordinary full-attachment draft helper.
	for _, archive := range []struct {
		cookie *http.Cookie
		route  string
	}{
		{creator, "/api/v1/jobs/" + job.ID + "/download.zip"},
		{recipient, base + "/jobs/" + job.ID + "/download.zip"},
	} {
		w := agentProjectRequest(t, server, archive.cookie, "GET", archive.route, nil, nil)
		z, zipErr := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if w.Code != 200 || zipErr != nil || len(z.File) != 1 {
			t.Fatal("draft ZIP dispatch changed", w.Code, zipErr)
		}
		entry, openErr := z.File[0].Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		data, readErr := io.ReadAll(entry)
		_ = entry.Close()
		if readErr != nil || !bytes.Equal(data, payload) {
			t.Fatal("draft ZIP bytes changed", readErr)
		}
	}
	if w := agentProjectRequest(t, server, creator, "DELETE", base+"/grants", nil, nil); w.Code != 200 {
		t.Fatal("revocation failed", w.Code)
	}
	if w := agentProjectRequest(t, server, recipient, "GET", base+"/artifacts/"+artifact.ID+"/download", nil, nil); w.Code != 404 {
		t.Fatal("revoked project artifact remained accessible", w.Code)
	}
}

func TestDraftCacheSourceAndFinalServingUnchanged(t *testing.T) {
	payload := []byte("ordinary source or final artifact")
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	names := []string{"source", "legacy-attachment", "final-attachment"}
	if !draftCacheHintSupported {
		names = append(names, "unsupported-draft")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			sink := &draftCacheSink{ResponseRecorder: httptest.NewRecorder()}
			r := httptest.NewRequest(http.MethodGet, "/media", nil)
			if name == "unsupported-draft" {
				serveFileWithDraftHint(sink, r, path, "clip.mp4", true, true)
			} else if name == "final-attachment" {
				serveFileWithDraftHint(sink, r, path, "clip.mp4", true, false)
			} else {
				serveFile(sink, r, path, "clip.mp4", name == "legacy-attachment")
			}
			if sink.Code != 200 || !bytes.Equal(sink.Body.Bytes(), payload) || sink.flushCalls != 0 {
				t.Fatal("ordinary serving entered draft completion policy")
			}
		})
	}
}

// BenchmarkDraftCacheRealHTTPRepeatedSyntheticDownload measures repeated real
// HTTP downloads of synthetic bytes, not exports, memory savings or an SLA.
// Use a disk-backed TMPDIR. Subcases run ordinary then draft with no cache
// flushing, so their throughput is descriptive, not an order-balanced result.
func BenchmarkDraftCacheRealHTTPRepeatedSyntheticDownload(b *testing.B) {
	b.StopTimer()
	const size = 4 << 20 // Representative payload size, never a policy threshold.
	file, err := os.CreateTemp(b.TempDir(), "download-*.mp4")
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()
	if _, err = file.Write(bytes.Repeat([]byte("x"), size)); err != nil {
		b.Fatal(err)
	}
	if err = file.Sync(); err != nil {
		b.Fatal(err)
	}
	if err = file.Close(); err != nil {
		b.Fatal(err)
	}
	for _, sample := range []struct {
		name  string
		draft bool
	}{{"ordinary-final", false}, {"draft", true}} {
		b.Run(sample.name, func(b *testing.B) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				serveFileWithDraftHint(w, r, file.Name(), "clip.mp4", true, sample.draft)
			}))
			defer server.Close()
			transport := &http.Transport{DisableCompression: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
			b.SetBytes(size)
			b.ResetTimer()
			for range b.N {
				response, err := client.Get(server.URL)
				if err != nil {
					b.Fatal(err)
				}
				n, readErr := io.Copy(io.Discard, response.Body)
				closeErr := response.Body.Close()
				if response.StatusCode != http.StatusOK || n != size || readErr != nil || closeErr != nil {
					b.Fatalf("download status=%d bytes=%d read=%v close=%v", response.StatusCode, n, readErr, closeErr)
				}
			}
			b.StopTimer()
		})
	}
}
