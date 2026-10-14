package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/jackc/pgx/v5"
)

func thumbnailLazyFixture(t *testing.T) (*Server, Source, platformInfo, []byte) {
	t.Helper()
	store := testStore(t)
	c := ledgerConfig(t)
	c.SourceTimeout = 10 * time.Second
	server := NewServer(c, store)
	source, metadata := metadataCacheFixture()
	source.ID = newID("src")
	source.URL = "https://www.twitch.tv/videos/123"
	metadata.Extractor = "Twitch"
	metadata.Thumbnail = "https://media.example/thumbnail.jpg?token=private-thumbnail"
	canonical := "/api/v1/sources/" + source.ID + "/thumbnail"
	source.ThumbnailURL = &canonical
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if err := store.CachePlatformMetadata(context.Background(), source, metadata); err != nil {
		t.Fatal(err)
	}
	var content bytes.Buffer
	if err := png.Encode(&content, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return server, source, metadata, content.Bytes()
}

func thumbnailLazyRequest(source Source, ctx context.Context) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/sources/"+source.ID+"/thumbnail", nil).WithContext(ctx)
	r.SetPathValue("id", source.ID)
	return r
}

func awaitThumbnailTest(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("owned thumbnail request did not reach its controlled boundary")
	}
}

func thumbnailFixtureFetcher(content []byte, count *atomic.Int32) thumbnailFetcher {
	return func(ctx context.Context, c Config, id, raw string, limit int64) (string, error) {
		count.Add(1)
		return stageThumbnailWithin(ctx, c, id, bytes.NewReader(content), limit, fsdurableThumbnailPublish)
	}
}

// Reuse the production publication barrier without exposing an upstream URL.
func fsdurableThumbnailPublish(ctx context.Context, stage, final string) (os.FileInfo, error) {
	return fsdurable.Publish(ctx, stage, final)
}

func TestLazyThumbnailPresentationAcceptsOnlyCanonicalOwnerEndpoint(t *testing.T) {
	for _, tc := range []struct {
		kind, url string
		removed   bool
		want      bool
	}{{"platform", "/api/v1/sources/src/thumbnail", false, true},
		{"youtube", "/api/v1/sources/src/thumbnail", false, true},
		{"platform", "https://media.example/private-thumbnail", false, false},
		{"platform", "/api/v1/sources/foreign/thumbnail", false, false},
		{"upload", "/api/v1/sources/src/thumbnail", false, false},
		{"platform", "/api/v1/sources/src/thumbnail", true, false}} {
		source := Source{ID: "src", Kind: tc.kind, ThumbnailURL: &tc.url, MediaRemoved: tc.removed}
		completeSourcePresentation(&source)
		if (source.ThumbnailURL != nil) != tc.want {
			t.Fatal("unsafe or missing lazy presentation", tc)
		}
	}
}

func TestLazyThumbnailImportPublishesMetadataWithoutImageWork(t *testing.T) {
	server, previous, _, _ := thumbnailLazyFixture(t)
	r := httptest.NewRequest("POST", "/api/v1/sources", strings.NewReader(`{"url":"https://www.twitch.tv/videos/123"}`))
	w := httptest.NewRecorder()
	server.addSourceWithClient(w, r, previous.Owner, func() *http.Client {
		t.Fatal("cached platform import unexpectedly opened an upstream client")
		return nil
	})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var opened Source
	if err := json.Unmarshal(w.Body.Bytes(), &opened); err != nil || opened.ID == previous.ID || opened.ThumbnailURL == nil {
		t.Fatal("metadata-only source was not returned", err, opened)
	}
	if *opened.ThumbnailURL != "/api/v1/sources/"+opened.ID+"/thumbnail" || strings.Contains(w.Body.String(), "private-thumbnail") {
		t.Fatal("private image address entered public source presentation")
	}
	persisted, err := server.Store.Source(context.Background(), opened.ID, previous.Owner)
	if err != nil || persisted.ThumbnailPath != "" || persisted.ThumbnailURL == nil {
		t.Fatal("import staged an image or lost the canonical lazy URL", err, persisted)
	}
	files, reserved := ledgerBytes(t, server.Store)
	if files != 0 || reserved != 0 || len(server.slots) != 0 {
		t.Fatal("metadata-only import retained image work/accounting", files, reserved, len(server.slots))
	}
}

func TestLazyThumbnailOwnerRejectionPerformsNoResolutionOrFetch(t *testing.T) {
	server, source, _, _ := thumbnailLazyFixture(t)
	unexpected := func(context.Context, Source) (platformInfo, error) {
		t.Fatal("foreign owner resolved metadata")
		return platformInfo{}, nil
	}
	fetch := func(context.Context, Config, string, string, int64) (string, error) {
		t.Fatal("foreign owner fetched an image")
		return "", nil
	}
	w := httptest.NewRecorder()
	server.sourceThumbnailWithPreparation(w, thumbnailLazyRequest(source, context.Background()), "foreign", fetch, unexpected)
	if w.Code != 404 {
		t.Fatal("foreign owner was not rejected", w.Code)
	}
}

func TestLazyThumbnailConcurrentGetsReuseFileAndCommitExactCharge(t *testing.T) {
	server, source, metadata, content := thumbnailLazyFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	var fetches atomic.Int32
	var began atomic.Bool
	fetch := thumbnailFixtureFetcher(content, &fetches)
	held := func(ctx context.Context, c Config, id, raw string, limit int64) (string, error) {
		if began.CompareAndSwap(false, true) {
			close(started)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
		}
		return fetch(ctx, c, id, raw, limit)
	}
	resolve := func(context.Context, Source) (platformInfo, error) { return metadata, nil }
	first, second := httptest.NewRecorder(), httptest.NewRecorder()
	done := make(chan struct{}, 2)
	go func() {
		server.sourceThumbnailWithPreparation(first, thumbnailLazyRequest(source, context.Background()), source.Owner, held, resolve)
		done <- struct{}{}
	}()
	awaitThumbnailTest(t, started)
	if _, err := server.Store.DeleteSource(context.Background(), source.ID, source.Owner); !errors.Is(err, ErrSourceInUse) {
		t.Fatal("source deletion raced an in-flight image", err)
	}
	go func() {
		server.sourceThumbnailWithPreparation(second, thumbnailLazyRequest(source, context.Background()), source.Owner, held, resolve)
		done <- struct{}{}
	}()
	close(release)
	awaitThumbnailTest(t, done)
	awaitThumbnailTest(t, done)
	if first.Code != 200 || second.Code != 200 || !bytes.Equal(first.Body.Bytes(), content) || !bytes.Equal(second.Body.Bytes(), content) || fetches.Load() != 1 {
		t.Fatal("concurrent image GET did not reuse one completed file", first.Code, second.Code, fetches.Load())
	}
	files, reserved := ledgerBytes(t, server.Store)
	if files != int64(len(content)) || reserved != 0 || len(server.slots) != 0 {
		t.Fatal("thumbnail charge/reservation mismatch", files, reserved, len(server.slots))
	}
	paths, err := server.Store.DeleteSource(context.Background(), source.ID, source.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Store.DrainStorageDeletes(context.Background(), server.Config, paths); err != nil {
		t.Fatal(err)
	}
	files, reserved = ledgerBytes(t, server.Store)
	if files != 0 || reserved != 0 {
		t.Fatal("deleted thumbnail charge survived cleanup", files, reserved)
	}
}

type thumbnailTestTransport func(*http.Request) (*http.Response, error)

func (f thumbnailTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type thumbnailBlockedBody struct {
	ctx     context.Context
	started chan struct{}
	once    atomic.Bool
}

func (b *thumbnailBlockedBody) Read([]byte) (int, error) {
	if b.once.CompareAndSwap(false, true) {
		close(b.started)
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *thumbnailBlockedBody) Close() error { return nil }

func TestLazyThumbnailCancelledBodyRemovesPartialAndReleasesPins(t *testing.T) {
	server, source, metadata, _ := thumbnailLazyFixture(t)
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	fetch := func(ctx context.Context, c Config, id, raw string, limit int64) (string, error) {
		return fetchThumbnailWithClient(ctx, c, id, raw, limit, func() *http.Client {
			return &http.Client{Transport: thumbnailTestTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, ContentLength: -1, Body: &thumbnailBlockedBody{ctx: r.Context(), started: started}, Header: http.Header{}}, nil
			})}
		})
	}
	response, done := httptest.NewRecorder(), make(chan struct{})
	go func() {
		server.sourceThumbnailWithPreparation(response, thumbnailLazyRequest(source, ctx), source.Owner, fetch, func(context.Context, Source) (platformInfo, error) { return metadata, nil })
		close(done)
	}()
	awaitThumbnailTest(t, started)
	if _, err := os.Stat(filepath.Join(server.Config.DataDir, "sources", source.ID+".thumbnail.part")); err != nil {
		t.Fatal("slow upstream did not enter bounded private staging", err)
	}
	cancel()
	awaitThumbnailTest(t, done)
	if response.Code != 404 {
		t.Fatal("optional cancelled image unexpectedly succeeded", response.Code)
	}
	for _, suffix := range []string{".thumbnail", ".thumbnail.part"} {
		if _, err := os.Stat(filepath.Join(server.Config.DataDir, "sources", source.ID+suffix)); !os.IsNotExist(err) {
			t.Fatal("cancelled image leaked", suffix, err)
		}
	}
	files, reserved := ledgerBytes(t, server.Store)
	if files != 0 || reserved != 0 || len(server.slots) != 0 {
		t.Fatal("cancelled image retained charge/admission", files, reserved)
	}
	if _, err := server.Store.DeleteSource(context.Background(), source.ID, source.Owner); err != nil {
		t.Fatal("cancelled image retained source reader", err)
	}
}

func TestLazyThumbnailRestartAndExpiredMetadataRefresh(t *testing.T) {
	server, source, metadata, content := thumbnailLazyFixture(t)
	if _, err := server.Store.DB.Exec(context.Background(), `DELETE FROM source_metadata_cache WHERE source_id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	server = NewServer(server.Config, server.Store)
	var refreshes, fetches atomic.Int32
	resolve := func(ctx context.Context, source Source) (platformInfo, error) {
		info, _, err := server.Store.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) { refreshes.Add(1); return metadata, nil })
		return info, err
	}
	w := httptest.NewRecorder()
	server.sourceThumbnailWithPreparation(w, thumbnailLazyRequest(source, context.Background()), source.Owner, thumbnailFixtureFetcher(content, &fetches), resolve)
	if w.Code != 200 || refreshes.Load() != 1 || fetches.Load() != 1 {
		t.Fatal("restart lost lazy image/refresh", w.Code, refreshes.Load(), fetches.Load())
	}
	server = NewServer(server.Config, server.Store)
	w = httptest.NewRecorder()
	server.sourceThumbnailWithPreparation(w, thumbnailLazyRequest(source, context.Background()), source.Owner, thumbnailFixtureFetcher(content, &fetches), resolve)
	if w.Code != 200 || refreshes.Load() != 1 || fetches.Load() != 1 {
		t.Fatal("persisted thumbnail was prepared again after restart")
	}
}

func TestLazyThumbnailSmallQuotaBoundsActualTransfer(t *testing.T) {
	server, source, metadata, content := thumbnailLazyFixture(t)
	server.Config.MaxOwnerBytes, server.Config.MaxStorageBytes = 1024, 1024
	server.Store.ConfigureStorage(server.Config)
	var allowance int64
	fetch := func(ctx context.Context, c Config, id, raw string, limit int64) (string, error) {
		allowance = limit
		return stageThumbnailWithin(ctx, c, id, bytes.NewReader(content), limit, fsdurableThumbnailPublish)
	}
	w := httptest.NewRecorder()
	server.sourceThumbnailWithPreparation(w, thumbnailLazyRequest(source, context.Background()), source.Owner, fetch, func(context.Context, Source) (platformInfo, error) { return metadata, nil })
	if w.Code != 200 || allowance != 1024 {
		t.Fatal("small install could not reserve bounded image", w.Code, allowance)
	}
}

func TestLazyThumbnailDisabledQuotaKeepsImageAllowanceBounded(t *testing.T) {
	for _, limits := range []struct {
		owner, global, want int64
	}{{0, 16 << 20, maxThumbnailBytes}, {32 << 20, 0, maxThumbnailBytes},
		{0, 0, maxThumbnailBytes}, {1024, 0, 1024}, {0, 1024, 1024}} {
		t.Run(strings.Join([]string{strconv.FormatInt(limits.owner, 10), strconv.FormatInt(limits.global, 10)}, "-"), func(t *testing.T) {
			server, source, metadata, content := thumbnailLazyFixture(t)
			server.Config.MaxOwnerBytes, server.Config.MaxStorageBytes = limits.owner, limits.global
			server.Store.ConfigureStorage(server.Config)
			var allowance int64
			fetch := func(ctx context.Context, c Config, id, raw string, limit int64) (string, error) {
				allowance = limit
				return stageThumbnailWithin(ctx, c, id, bytes.NewReader(content), limit, fsdurableThumbnailPublish)
			}
			w := httptest.NewRecorder()
			server.sourceThumbnailWithPreparation(w, thumbnailLazyRequest(source, context.Background()), source.Owner, fetch, func(context.Context, Source) (platformInfo, error) { return metadata, nil })
			files, reserved := ledgerBytes(t, server.Store)
			if w.Code != 200 || allowance != limits.want || files != int64(len(content)) || reserved != 0 {
				t.Fatal("disabled quota changed bounded image preparation", w.Code, allowance, files, reserved)
			}
		})
	}
}

func TestLazyThumbnailDeadlineAndChangedIdentityDoNotFetch(t *testing.T) {
	for _, kind := range []string{"deadline", "changed-identity"} {
		t.Run(kind, func(t *testing.T) {
			server, source, metadata, content := thumbnailLazyFixture(t)
			var fetches atomic.Int32
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			resolve := func(ctx context.Context, source Source) (platformInfo, error) {
				if kind == "deadline" {
					<-ctx.Done()
					return platformInfo{}, ctx.Err()
				}
				metadata.ID = "different-recording"
				return metadata, nil
			}
			w := httptest.NewRecorder()
			server.sourceThumbnailWithPreparation(w, thumbnailLazyRequest(source, ctx), source.Owner, thumbnailFixtureFetcher(content, &fetches), resolve)
			files, reserved := ledgerBytes(t, server.Store)
			if w.Code != 404 || fetches.Load() != 0 || files != 0 || reserved != 0 || len(server.slots) != 0 {
				t.Fatal("optional metadata failure escaped bounded preparation", w.Code, fetches.Load(), files, reserved)
			}
		})
	}
}

func TestLazyThumbnailOversizedBodyCannotWriteBeyondAllowance(t *testing.T) {
	server, source, metadata, content := thumbnailLazyFixture(t)
	server.Config.MaxOwnerBytes, server.Config.MaxStorageBytes = 1024, 1024
	server.Store.ConfigureStorage(server.Config)
	content = append(content, bytes.Repeat([]byte{'x'}, 2048)...)
	fetch := func(ctx context.Context, c Config, id, raw string, limit int64) (string, error) {
		return stageThumbnailWithin(ctx, c, id, &thumbnailAllowanceReader{Reader: bytes.NewReader(content), path: filepath.Join(c.DataDir, "sources", id+".thumbnail.part"), limit: limit, t: t}, limit, fsdurableThumbnailPublish)
	}
	w := httptest.NewRecorder()
	server.sourceThumbnailWithPreparation(w, thumbnailLazyRequest(source, context.Background()), source.Owner, fetch, func(context.Context, Source) (platformInfo, error) { return metadata, nil })
	files, reserved := ledgerBytes(t, server.Store)
	if w.Code != 404 || files != 0 || reserved != 0 {
		t.Fatal("oversized image was retained", w.Code, files, reserved)
	}
}

type thumbnailAllowanceReader struct {
	*bytes.Reader
	path  string
	limit int64
	t     *testing.T
}

func (r *thumbnailAllowanceReader) Read(buffer []byte) (int, error) {
	if info, err := os.Stat(r.path); err == nil && info.Size() > r.limit {
		r.t.Fatal("private staging wrote beyond the charged allowance", info.Size(), r.limit)
	}
	return r.Reader.Read(buffer)
}

func TestLazyThumbnailUncertainAttachmentKeepsCompletedBytes(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "lost-ack-after-commit"}[committed], func(t *testing.T) {
			server, source, _, content := thumbnailLazyFixture(t)
			token := newID("thumbnail")
			allowance, err := server.Store.reserveLazyThumbnail(context.Background(), server.Config, source, token)
			if err != nil {
				t.Fatal(err)
			}
			path, err := stageThumbnailWithin(context.Background(), server.Config, source.ID, bytes.NewReader(content), allowance, fsdurableThumbnailPublish)
			if err != nil {
				t.Fatal(err)
			}
			err = server.Store.attachLazyThumbnailWithCommit(context.Background(), server.Config, source, token, path, allowance, func(ctx context.Context, tx pgx.Tx) error {
				if committed {
					if err := tx.Commit(ctx); err != nil {
						return err
					}
				}
				return io.ErrUnexpectedEOF
			})
			if !errors.Is(err, ErrSourceCommitUncertain) || !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal("lost acknowledgement was treated as definite failure", err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("uncertain completed image was removed", err)
			}
			if err := server.Store.FinishSource(context.Background(), server.Config, source.Owner, token, source.ID); err != nil {
				t.Fatal(err)
			}
			files, reserved := ledgerBytes(t, server.Store)
			if files != int64(len(content)) || reserved != 0 {
				t.Fatal("uncertain file escaped accounting/reconciliation", files, reserved)
			}
		})
	}
}
