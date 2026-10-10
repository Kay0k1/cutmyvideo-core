package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/jackc/pgx/v5"
)

type publicationUncommittedAcknowledgement struct{ pgx.Tx }

func (publicationUncommittedAcknowledgement) Commit(context.Context) error {
	return io.ErrUnexpectedEOF
}

func uploadPublicationRequest(t *testing.T, media []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "recording.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(media); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	return r
}

func sourceTransactionWithLostAcknowledgement(ctx context.Context, s *Store, v Source, committed bool) error {
	c := s.storageConfig()
	sizes := make(map[string]int64)
	for _, path := range []string{v.Path, v.ThumbnailPath} {
		if path == "" {
			continue
		}
		info, err := fsdurable.Sync(path, publicationParents(c, path)...)
		if err != nil {
			return err
		}
		sizes[path] = info.Size()
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	if committed {
		return s.addSourceTransaction(ctx, publicationLostAcknowledgement{tx}, v, sizes, c)
	}
	return s.addSourceTransaction(ctx, publicationUncommittedAcknowledgement{tx}, v, sizes, c)
}

func TestSourceMissingOrUnsynchronizedFilesNeverRegister(t *testing.T) {
	for _, fault := range []string{"missing-source", "missing-thumbnail", "thumbnail-sync", "cancel-during-sync"} {
		t.Run(fault, func(t *testing.T) {
			s := testStore(t)
			c := ledgerConfig(t)
			s.ConfigureStorage(c)
			path := writeLedgerFile(t, c, "sources", "input.media", 19)
			thumbnail := writeLedgerFile(t, c, "sources", "input.thumbnail", 23)
			v := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: path, ThumbnailPath: thumbnail, DurationMS: 1000}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			synchronize := fsdurable.Sync
			if strings.HasPrefix(fault, "missing-") {
				missing := path
				if fault == "missing-thumbnail" {
					missing = thumbnail
				}
				if err := os.Remove(missing); err != nil {
					t.Fatal(err)
				}
			} else {
				synchronize = func(file string, directories ...string) (fs.FileInfo, error) {
					if fault == "thumbnail-sync" && file == thumbnail {
						return nil, errors.Join(fsdurable.ErrSyncFailed, syscall.EIO)
					}
					info, err := fsdurable.Sync(file, directories...)
					if fault == "cancel-during-sync" {
						cancel()
					}
					return info, err
				}
			}
			err := s.addSourceWithSync(ctx, v, synchronize)
			if err == nil || errors.Is(err, ErrSourceCommitUncertain) {
				t.Fatal("definite filesystem rejection claimed registration or COMMIT uncertainty", err)
			}
			if fault == "cancel-during-sync" && !errors.Is(err, context.Canceled) {
				t.Fatal("caller cancellation was lost", err)
			}
			var sources, files int
			if err := s.DB.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM sources),(SELECT count(*) FROM storage_files)").Scan(&sources, &files); err != nil || sources != 0 || files != 0 {
				t.Fatal("rejected source registered metadata or storage charge", sources, files, err)
			}
		})
	}
}

func TestUploadDefinitePublicationFailuresCleanFilesAndReservation(t *testing.T) {
	fixtureConfig := mediaConfig(t)
	media, err := os.ReadFile(previewFixture(t, fixtureConfig))
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"sync", "deferred-commit-rejection"} {
		t.Run(fault, func(t *testing.T) {
			s := testStore(t)
			c := fixtureConfig
			c.DataDir, c.SourceTimeout = filepath.Join(t.TempDir(), "new", "nested", "media"), 20*time.Second
			c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
			server := NewServer(c, s)
			publish := server.databaseAddSource
			if fault == "sync" {
				publish = func(ctx context.Context, source Source) error {
					return s.addSourceWithSync(ctx, source, func(string, ...string) (fs.FileInfo, error) {
						return nil, errors.Join(fsdurable.ErrSyncFailed, syscall.EIO)
					})
				}
			} else if _, err := s.DB.Exec(context.Background(), `CREATE FUNCTION reject_source_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection' USING ERRCODE='23514'; END $$;
CREATE CONSTRAINT TRIGGER reject_source_commit AFTER INSERT ON sources DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_source_commit();`); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			server.uploadWithPublisher(w, uploadPublicationRequest(t, media), "owner", publish)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("definite publication failure returned %d: %s", w.Code, w.Body.String())
			}
			var sources, files, reserved int
			if err := s.DB.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM sources),(SELECT count(*) FROM storage_files),(SELECT count(*) FROM storage_reservations)").Scan(&sources, &files, &reserved); err != nil || sources != 0 || files != 0 || reserved != 0 {
				t.Fatal("definite failure leaked registration or charge", sources, files, reserved, err)
			}
			entries, err := os.ReadDir(filepath.Join(c.DataDir, "sources"))
			if err != nil || len(entries) != 0 {
				t.Fatal("definite upload failure retained temporary or final source", entries, err)
			}
		})
	}
}

func TestUploadLostAcknowledgementRetainsFilesChargeAndRecovers(t *testing.T) {
	fixtureConfig := mediaConfig(t)
	media, err := os.ReadFile(previewFixture(t, fixtureConfig))
	if err != nil {
		t.Fatal(err)
	}
	for _, committed := range []bool{true, false} {
		name := "committed"
		if !committed {
			name = "not-committed"
		}
		t.Run(name, func(t *testing.T) {
			s := testStore(t)
			c := fixtureConfig
			c.DataDir, c.SourceTimeout = t.TempDir(), 20*time.Second
			c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
			server := NewServer(c, s)
			var prepared Source
			w := httptest.NewRecorder()
			server.uploadWithPublisher(w, uploadPublicationRequest(t, media), "owner", func(ctx context.Context, v Source) error {
				prepared = v
				err := sourceTransactionWithLostAcknowledgement(ctx, s, v, committed)
				if !errors.Is(err, ErrSourceCommitUncertain) || !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatal("lost source acknowledgement was not classified", err)
				}
				return err
			})
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("unknown source publication returned %d", w.Code)
			}
			data, err := os.ReadFile(prepared.Path)
			if err != nil || !bytes.Equal(data, media) || strings.HasSuffix(prepared.Path, ".part") {
				t.Fatal("unknown COMMIT lost or changed the complete final source", err)
			}
			if _, err := os.Stat(prepared.Path + ".part"); !os.IsNotExist(err) {
				t.Fatal("unknown COMMIT retained a duplicate staging name", err)
			}
			files, reserved := ledgerBytes(t, s)
			if committed {
				if files != int64(len(media)) || reserved != 0 {
					t.Fatal("committed source is not charged exactly once", files, reserved)
				}
				if restored, err := s.Source(context.Background(), prepared.ID, "owner"); err != nil || restored.Path != prepared.Path {
					t.Fatal("committed source cannot be recovered", err)
				}
			} else {
				if reserved < int64(len(media)) {
					t.Fatal("unresolved source lost its preparation charge", reserved)
				}
				if _, err := s.DB.Exec(context.Background(), "UPDATE storage_reservations SET expires_at=now()-interval '1 second'"); err != nil {
					t.Fatal(err)
				}
				if err := s.ReconcileStorage(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				files, reserved = ledgerBytes(t, s)
				if files != int64(len(media)) || reserved != 0 {
					t.Fatal("abandoned source was not adopted before releasing its reservation", files, reserved)
				}
			}
		})
	}
}

func TestDirectSourcePublicationRetainsCompletePrivateMedia(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.SourceTimeout, c.MaxStorageBytes, c.MaxOwnerBytes = 20*time.Second, 1<<30, 1<<30
	media, err := os.ReadFile(previewFixture(t, c))
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(c, s)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sources", strings.NewReader(`{"url":"https://media.example/recording.mkv"}`))
	server.addSourceWithClient(w, r, "owner", func() *http.Client {
		return &http.Client{Transport: networkTestTransport(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != "https://media.example/recording.mkv" {
				t.Fatal("direct source request changed its address", request.URL)
			}
			return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(media)), Header: http.Header{"Content-Type": {"video/x-matroska"}}, Body: io.NopCloser(bytes.NewReader(media))}, nil
		})}
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("direct source returned %d: %s", w.Code, w.Body.String())
	}
	var source Source
	if err := json.Unmarshal(w.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Source(context.Background(), source.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(loaded.Path)
	if err != nil || !bytes.Equal(data, media) || !strings.HasSuffix(loaded.Path, ".media") {
		t.Fatal("direct source did not retain a complete final file", err)
	}
	if _, err := s.Source(context.Background(), source.ID, "another-owner"); !errors.Is(err, ErrNotFound) {
		t.Fatal("published direct source lost ownership isolation", err)
	}
}

func TestThumbnailPublicationValidatesCompleteImageAndPreservesExistingOutput(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"complete", "truncated", "existing", "sync-failure", "uncertain"} {
		t.Run(scenario, func(t *testing.T) {
			c := Config{DataDir: filepath.Join(t.TempDir(), "new", "media")}
			id := newID("src")
			final := filepath.Join(c.DataDir, "sources", id+".thumbnail")
			data := encoded.Bytes()
			if scenario == "truncated" {
				data = data[:33] // Intact PNG header; missing image data and trailer.
			}
			if scenario == "existing" {
				if err := fsdurable.EnsureDirectory(filepath.Dir(final), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(final, []byte("caller result"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			publish := fsdurable.Publish
			if scenario == "sync-failure" {
				publish = func(context.Context, string, string) (fs.FileInfo, error) {
					return nil, errors.Join(fsdurable.ErrSyncFailed, syscall.EIO)
				}
			} else if scenario == "uncertain" {
				publish = func(ctx context.Context, stage, final string) (fs.FileInfo, error) {
					info, err := fsdurable.Publish(ctx, stage, final)
					if err != nil {
						return nil, err
					}
					return info, errors.Join(fsdurable.ErrPublicationUncertain, syscall.EIO)
				}
			}
			path, err := stageThumbnail(context.Background(), c, id, bytes.NewReader(data), publish)
			if scenario == "complete" {
				if err != nil || path != final {
					t.Fatal("complete image did not publish", path, err)
				}
				actual, err := os.ReadFile(final)
				if err != nil || !bytes.Equal(actual, data) {
					t.Fatal("thumbnail bytes changed", err)
				}
			} else {
				if err == nil || path != "" {
					t.Fatal("rejected thumbnail claimed success", path, err)
				}
				if scenario == "existing" {
					actual, err := os.ReadFile(final)
					if err != nil || string(actual) != "caller result" {
						t.Fatal("thumbnail publication replaced or removed another caller's output", err)
					}
				} else if _, err := os.Stat(final); !os.IsNotExist(err) {
					t.Fatal("failed private thumbnail preparation leaked its final file", err)
				}
			}
			if _, err := os.Stat(final + ".part"); !os.IsNotExist(err) {
				t.Fatal("thumbnail preparation leaked staging data", err)
			}
		})
	}
}

func TestPreviewPublicationFailuresRetainOnlyUncertainCommitFilesAndCharge(t *testing.T) {
	fixtureConfig := mediaConfig(t)
	input := previewFixture(t, fixtureConfig)
	for _, fault := range []string{"sync", "deleted-file", "deferred-commit-rejection", "committed-lost-ack", "uncommitted-lost-ack"} {
		t.Run(fault, func(t *testing.T) {
			s := testStore(t)
			c := fixtureConfig
			c.DataDir, c.MaxStorageBytes, c.MaxOwnerBytes = filepath.Join(t.TempDir(), "new", "media"), 1<<30, 1<<30
			server := NewServer(c, s)
			v := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: input, DurationMS: 4000}
			if err := s.AddSource(context.Background(), v); err != nil {
				t.Fatal(err)
			}
			if fault == "deferred-commit-rejection" {
				if _, err := s.DB.Exec(context.Background(), `CREATE FUNCTION reject_preview_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='preview_cache' THEN RAISE EXCEPTION 'fixture rejection' USING ERRCODE='23514'; END IF; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER reject_preview_commit AFTER UPDATE ON storage_reservations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_preview_commit();`); err != nil {
					t.Fatal(err)
				}
			}
			var id string
			publish := func(ctx context.Context, c Config, source Source, previewID string, file *os.File) error {
				id = previewID
				if fault == "sync" {
					return s.publishPreviewWithSync(ctx, c, source, id, file, func(string, ...string) (fs.FileInfo, error) {
						return nil, errors.Join(fsdurable.ErrSyncFailed, syscall.EIO)
					})
				}
				if fault == "deleted-file" {
					if err := os.Remove(file.Name()); err != nil {
						t.Fatal(err)
					}
					return s.publishPreview(ctx, c, source, id, file)
				}
				if strings.HasSuffix(fault, "lost-ack") {
					info, err := fsdurable.Sync(file.Name(), filepath.Dir(filepath.Dir(file.Name())), c.DataDir, filepath.Dir(c.DataDir))
					if err != nil {
						return err
					}
					tx, err := s.storageTx(ctx)
					if err != nil {
						return err
					}
					if fault == "committed-lost-ack" {
						err = publishPreviewTransaction(ctx, publicationLostAcknowledgement{tx}, c, source, id, info.Size())
					} else {
						err = publishPreviewTransaction(ctx, publicationUncommittedAcknowledgement{tx}, c, source, id, info.Size())
					}
					if !errors.Is(err, ErrPreviewCommitUncertain) || !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatal("lost preview acknowledgement was not classified", err)
					}
					return err
				}
				return s.publishPreview(ctx, c, source, id, file)
			}
			r := httptest.NewRequest(http.MethodGet, "/preview?start_ms=1250", nil)
			r.SetPathValue("id", v.ID)
			w := httptest.NewRecorder()
			server.sourcePreviewWithPublisher(w, r, "owner", publish)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("failed preview publication returned %d: %s", w.Code, w.Body.String())
			}
			uncertain := strings.HasSuffix(fault, "lost-ack")
			var kind string
			var charge int64
			err := s.DB.QueryRow(context.Background(), "SELECT kind,size_bytes FROM storage_reservations WHERE id=$1", id).Scan(&kind, &charge)
			dir := filepath.Join(c.DataDir, "previews", id)
			if !uncertain {
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal("definite rejection retained its preview charge", kind, charge, err)
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("definite rejection retained its workspace", err)
				}
				return
			}
			info, fileErr := os.Stat(filepath.Join(dir, "preview.mp4"))
			if err != nil || fileErr != nil || charge < info.Size() {
				t.Fatal("unknown COMMIT lost the preview or its charge", charge, err, fileErr)
			}
			if fault == "committed-lost-ack" {
				if kind != "preview_cache" || charge != info.Size() {
					t.Fatal("committed preview accounting changed", kind, charge)
				}
				server.Config.FFmpeg = "/does-not-exist"
				retry := httptest.NewRecorder()
				server.sourcePreview(retry, r, "owner")
				if retry.Code != http.StatusOK || retry.Header().Get("X-Preview-Cache") != "hit" || int64(retry.Body.Len()) != info.Size() {
					t.Fatal("committed preview could not be recovered without re-encoding", retry.Code, retry.Body.String())
				}
			} else {
				if kind != "preview" || charge != previewReservationBytes {
					t.Fatal("unresolved preview lost its original reservation", kind, charge)
				}
				if _, err := s.DB.Exec(context.Background(), "UPDATE storage_reservations SET expires_at=now()-interval '1 second' WHERE id=$1", id); err != nil {
					t.Fatal(err)
				}
				if err := s.cleanupPreviews(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("uncommitted preview did not recover after lease expiry", err)
				}
			}
		})
	}
}

func TestSourceAndPreviewSlowSyncPreserveDatabaseBudget(t *testing.T) {
	for _, resource := range []string{"source", "preview"} {
		t.Run(resource, func(t *testing.T) {
			s := testStore(t)
			c := ledgerConfig(t)
			c.MaxStorageBytes = 1 << 30
			s.ConfigureStorage(c)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			slowSync := func(path string, directories ...string) (fs.FileInfo, error) {
				info, err := fsdurable.Sync(path, directories...)
				if err != nil {
					return nil, err
				}
				time.Sleep(apiDatabaseTimeout + 100*time.Millisecond)
				return info, nil
			}
			if resource == "source" {
				v := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: writeLedgerFile(t, c, "sources", "slow.media", 41), DurationMS: 1000}
				if err := s.addSourceWithSync(ctx, v, slowSync); err != nil {
					t.Fatal("slow source barrier exhausted SQL's budget", err)
				}
				if _, err := s.Source(ctx, v.ID, v.Owner); err != nil {
					t.Fatal("source registration did not complete", err)
				}
			} else {
				v := storedSource(t, s, "owner")
				id := newID("preview")
				if err := s.reservePreview(ctx, c, v, id); err != nil {
					t.Fatal(err)
				}
				file := cachedPreviewFixture(t, c, id, 1024)
				defer file.Close()
				if err := s.publishPreviewWithSync(ctx, c, v, id, file, slowSync); err != nil {
					t.Fatal("slow preview barrier exhausted SQL's budget", err)
				}
				var kind string
				if err := s.DB.QueryRow(ctx, "SELECT kind FROM storage_reservations WHERE id=$1", id).Scan(&kind); err != nil || kind != "preview_cache" {
					t.Fatal("preview registration did not complete", kind, err)
				}
			}
		})
	}
}
