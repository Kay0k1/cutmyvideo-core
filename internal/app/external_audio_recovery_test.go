package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema008ReleaseCommit = "106d0d2d7887540768cc21e3bd4e2c71629af6c4"

func TestExternalAudioPreviousBinaryRefuses009WithoutWrites(t *testing.T) {
	previous := os.Getenv("CUTMY_TEST_PREVIOUS_SCHEMA008_BINARY")
	if previous == "" {
		t.Skip("set exact owned v1.4.2 schema8 binary for the required009 refusal gate")
	}
	if !filepath.IsAbs(previous) {
		t.Fatal("previous schema8 binary must be absolute")
	}
	raw, err := os.ReadFile(previous)
	if err != nil {
		t.Fatal("owned previous binary unavailable")
	}
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != os.Getenv("CUTMY_TEST_PREVIOUS_SCHEMA008_SHA256") {
		t.Fatal("previous binary differs from its pinned source-build receipt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, previous, "version").Output()
	var identity struct {
		Version, Commit, OS, Arch string
		GoVersion                 string `json:"go_version"`
	}
	if err != nil || json.Unmarshal(version, &identity) != nil || identity.Version != "v1.4.2" || identity.Commit != schema008ReleaseCommit || identity.GoVersion != "go1.27.2" || identity.OS != runtime.GOOS || identity.Arch != runtime.GOARCH {
		t.Fatal("previous binary is not exact v1.4.2 source/toolchain/target")
	}
	f := newMigrationFixture(t)
	store := installPosterPredecessor(t, f, 9)
	a, b := storedSource(t, store, "owner"), storedSource(t, store, "owner")
	if _, err = store.CreateJob(ctx, "owner", audioRefRequest(a, b), "retained-schema009"); err != nil {
		t.Fatal(err)
	}
	before := migrationSnapshot(t, store.DB)
	command := exec.CommandContext(ctx, previous, "--json-errors", "maintenance")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "DATABASE_URL=" + f.url, "DATA_DIR=" + t.TempDir(), "GOMAXPROCS=2", "GOMEMLIMIT=128MiB"}
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	var refusal struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || ctx.Err() != nil || json.Unmarshal(bytes.TrimSpace(output), &refusal) != nil || refusal.Error.Code != "schema_incompatible" {
		t.Fatal("v1.4.2 did not refuse009 before processing; private command output withheld")
	}
	if migrationSnapshot(t, store.DB) != before {
		t.Fatal("schema8 refusal wrote009 catalog/history/rows")
	}
	legacy := posterLegacyRows(t, store.DB)
	compatible, err := OpenStore(ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	compatible.DB.Close()
	if posterLegacyRows(t, store.DB) != legacy {
		t.Fatal("009→current upgrade changed retained009 rows or history")
	}
	current := migrationSnapshot(t, store.DB)
	compatible, err = OpenStore(ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	compatible.DB.Close()
	if migrationSnapshot(t, store.DB) != current {
		t.Fatal("current compatible startup wrote retained state")
	}
	after, err := os.ReadFile(previous)
	if err != nil || sha256.Sum256(after) != hash {
		t.Fatal("previous binary changed during refusal proof")
	}
}

func externalAudioRecoveryFixture(t *testing.T, s *Store, c Config) (Job, ExportRequest) {
	t.Helper()
	ctx := context.Background()
	s.ConfigureStorage(c)
	for _, name := range []string{"sources", "work", "artifacts"} {
		if err := os.MkdirAll(filepath.Join(c.DataDir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	video, audio := filepath.Join(c.DataDir, "sources", "schema009-primary.mp4"), filepath.Join(c.DataDir, "sources", "schema009-audio.wav")
	mcRun(t, ctx, c, "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=25:duration=4", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", video)
	mcRun(t, ctx, c, "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=4", audio)
	primary, err := InspectLocal(ctx, c, video)
	if err != nil {
		t.Fatal(err)
	}
	primary.ID, primary.Owner, primary.Title = newID("source"), "owner", "Primary009"
	if err = s.AddSource(ctx, primary); err != nil {
		t.Fatal(err)
	}
	replacement, err := InspectLocal(ctx, c, audio)
	if err != nil {
		t.Fatal(err)
	}
	replacement.ID, replacement.Owner, replacement.Title = newID("source"), "owner", "Replacement009"
	if err = s.AddSource(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	i, tp, lra := -16.0, -1.0, 11.0
	r := ExportRequest{SourceID: primary.ID, Ranges: []Range{{StartMS: 0, EndMS: 2000}}, Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "joined", AudioReplace: &edit.AudioReplace{SourceID: replacement.ID}, Canvas: &edit.Canvas{Width: 180, Height: 320, Fit: "contain", Background: "#000000"}, AudioNormalize: &edit.AudioNormalize{IntegratedLUFS: &i, TruePeakDBTP: &tp, LoudnessRangeLU: &lra}}
	j, err := s.CreateJob(ctx, "owner", r, "retained009")
	if err != nil {
		t.Fatal(err)
	}
	claimed, token, err := s.Claim(ctx)
	if err != nil || claimed.ID != j.ID {
		t.Fatal("fresh009 queued export unavailable", err)
	}
	processJob(ctx, c, s, claimed, token)
	j, err = s.Job(ctx, j.ID, "owner")
	if err != nil || j.Status != "succeeded" || len(j.Items) != 1 || j.Items[0].Artifact == nil {
		t.Fatal("actual009 external audio export failed", j.Status, err)
	}
	return j, r
}

func TestExternalAudioCoordinated009BackupRestorePreservesRowsAndMedia(t *testing.T) {
	container := os.Getenv("CUTMY_TEST_PGTOOLS_CONTAINER")
	if container == "" {
		t.Skip("set owned PostgreSQL17 tools container for required009 coordinated restore")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,100}$`).MatchString(container) {
		t.Fatal("invalid owned PostgreSQL tools container")
	}
	f := newMigrationFixture(t)
	s := openMigrationStore(t, f)
	c := agentRecoveryConfig(t)
	c.MaxStorageBytes, c.MaxOwnerBytes = 128<<20, 128<<20
	j, request := externalAudioRecoveryFixture(t, s, c)
	ctx := context.Background()
	path, _, err := s.ArtifactPath(ctx, j.Items[0].Artifact.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	mcRun(t, ctx, c, "-i", path, "-f", "null", "-")
	before := metadataResolutionRecoveryRows(t, s.DB)
	media := cleanupMediaBackup(t, c.DataDir)
	parsed, err := url.Parse(f.url)
	if err != nil {
		t.Fatal("synthetic database URL invalid")
	}
	user, database := parsed.User.Username(), strings.TrimPrefix(parsed.Path, "/")
	dump := cleanupPGTool(t, container, user, database, "pg_dump", nil, "--format=custom", "--schema", f.namespace, "--no-owner", "--no-privileges")
	name := newID("audio009_restore")
	sourcePinExec(t, f.db, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	var restored *pgxpool.Pool
	t.Cleanup(func() {
		if restored != nil {
			restored.Close()
		}
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := f.db.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error("owned009 restore database cleanup failed")
		}
	})
	cleanupPGTool(t, container, user, name, "pg_restore", dump, "--exit-on-error", "--no-owner", "--no-privileges")
	cleanupMediaRestore(t, c.DataDir, media)
	copyURL := *parsed
	copyURL.Path = "/" + name
	restored, err = pgxpool.New(ctx, copyURL.String())
	if err != nil {
		t.Fatal("synthetic restored pool unavailable")
	}
	if metadataResolutionRecoveryRows(t, restored) != before {
		t.Fatal("009 pg_restore changed source refs/request/pins/jobs/artifacts/ledger")
	}
	physical := migrationSnapshot(t, restored)
	compatible, err := OpenStore(ctx, copyURL.String())
	if err != nil {
		t.Fatal("compatible009 restore refused", err)
	}
	defer compatible.DB.Close()
	compatible.ConfigureStorage(c)
	if migrationSnapshot(t, restored) != physical {
		t.Fatal("compatible009 restored startup wrote rows or catalog")
	}
	if after := cleanupMediaBackup(t, c.DataDir); !bytes.Equal(after, media) {
		t.Fatal("009 coordinated media restore changed bytes")
	}
	replayed, err := compatible.CreateJob(ctx, "owner", request, "retained009")
	if err != nil || replayed.ID != j.ID || replayed.Items[0].Artifact.ID != j.Items[0].Artifact.ID {
		t.Fatal("restored idempotency lost exact completed export", err)
	}
	var ref string
	if err = compatible.DB.QueryRow(ctx, `SELECT audio_source_id FROM jobs WHERE id=$1`, j.ID).Scan(&ref); err != nil || ref != request.AudioReplace.SourceID {
		t.Fatal("restored external source membership changed", ref, err)
	}
	assertAudioRefPins(t, compatible, request.SourceID, 1, true)
	assertAudioRefPins(t, compatible, request.AudioReplace.SourceID, 1, true)
	if _, _, err = compatible.ArtifactPath(ctx, j.Items[0].Artifact.ID, "foreign"); !errors.Is(err, ErrNotFound) {
		t.Fatal("restored artifact escaped owner scope", err)
	}
	path, _, err = compatible.ArtifactPath(ctx, j.Items[0].Artifact.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	mcRun(t, ctx, c, "-i", path, "-f", "null", "-")
	request.Ranges = []Range{{StartMS: 1000, EndMS: 3000}}
	fresh, err := compatible.CreateJob(ctx, "owner", request, "new009-after-restore")
	if err != nil {
		t.Fatal("fresh009 admission after restore refused", err)
	}
	claimed, token, err := compatible.Claim(ctx)
	if err != nil || claimed.ID != fresh.ID {
		t.Fatal("fresh009 restored claim failed", err)
	}
	processJob(ctx, c, compatible, claimed, token)
	fresh, err = compatible.Job(ctx, fresh.ID, "owner")
	if err != nil || fresh.Status != "succeeded" || fresh.Items[0].Artifact == nil {
		t.Fatal("new009 media export after restore failed", fresh.Status, err)
	}
	path, _, err = compatible.ArtifactPath(ctx, fresh.Items[0].Artifact.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	mcRun(t, ctx, c, "-i", path, "-f", "null", "-")
}
