package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func audioRefRequest(primary, audio Source) ExportRequest {
	r := requestFor(primary)
	r.AudioReplace = &edit.AudioReplace{SourceID: audio.ID}
	return r
}

func assertAudioRefPins(t *testing.T, s *Store, id string, want int64, valid bool) {
	t.Helper()
	var count, actual int64
	var gotValid bool
	err := s.DB.QueryRow(context.Background(), `SELECT retention_job_pins,retention_pins_valid,(SELECT count(*) FROM jobs WHERE source_id=s.id)+(SELECT count(*) FROM jobs WHERE audio_source_id=s.id) FROM sources s WHERE id=$1`, id).Scan(&count, &gotValid, &actual)
	if err != nil || count != want || gotValid != valid || valid && count != actual {
		t.Fatalf("external refs %s stored=%d valid=%v actual=%d want%d/%v: %v", id, count, gotValid, actual, want, valid, err)
	}
}

func TestExternalAudioMigration008UpgradePreservesRowsAndReadOnlyReopen(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, `CREATE TABLE app_schema_versions(version text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range schemaMigrations[:8] {
		if _, err = tx.Exec(ctx, migrationSQL(m)); err != nil {
			t.Fatal(m.version, err)
		}
	}
	for i, m := range schemaMigrations[:8] {
		if _, err = tx.Exec(ctx, `INSERT INTO app_schema_versions(version) VALUES($1);`, m.version); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO app_schema_migrations(position,version,checksum) VALUES($1,$2,$3)`, i+1, m.version, migrationChecksum(m)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind) VALUES('old-primary','owner','Preserve recording',4000,'upload'); INSERT INTO jobs(id,owner,source_id,request,items,status) VALUES('old-job','owner','old-primary','{"ranges":[{"start_ms":0,"end_ms":1000}]}','[]','succeeded'); INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms) VALUES('old-artifact','owner','old-job','/synthetic/old.mp4','old.mp4',19,0,1000)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	rows := func() string {
		var data string
		err := f.db.QueryRow(ctx, `SELECT jsonb_build_object('sources',(SELECT jsonb_agg(jsonb_build_object('row',to_jsonb(s),'xmin',xmin::text)) FROM sources s),'jobs',(SELECT jsonb_agg(jsonb_build_object('row',to_jsonb(j)-'audio_source_id','xmin',xmin::text)) FROM jobs j),'artifacts',(SELECT jsonb_agg(jsonb_build_object('row',to_jsonb(a),'xmin',xmin::text)) FROM artifacts a),'ledger',(SELECT jsonb_agg(jsonb_build_object('row',to_jsonb(m),'xmin',xmin::text)) FROM app_schema_migrations m WHERE position<=8))::text`).Scan(&data)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := rows()
	s := openMigrationStore(t, f)
	if after := rows(); after != before {
		t.Fatal("009 rewrote legacy source/job/artifact/history rows")
	}
	var ref *string
	if err = s.DB.QueryRow(ctx, `SELECT audio_source_id FROM jobs WHERE id='old-job'`).Scan(&ref); err != nil || ref != nil {
		t.Fatal("legacy request gained an external reference", ref, err)
	}
	assertMigrationLedger(t, s)
	snapshot := migrationSnapshot(t, s.DB)
	for range 2 {
		reopened, err := OpenStore(ctx, f.url)
		if err != nil {
			t.Fatal(err)
		}
		reopened.DB.Close()
		if after := migrationSnapshot(t, s.DB); after != snapshot {
			t.Fatal("009 compatible reopen wrote catalog/history/application data")
		}
	}
}

func TestExternalAudioAdmissionsAuthorizeBothReferencesAtomically(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(fmt.Sprint(limited), func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			primary, audio, foreign, removed, pending := storedSource(t, s, "owner"), storedSource(t, s, "owner"), storedSource(t, s, "foreign"), storedSource(t, s, "owner"), storedSource(t, s, "owner")
			remote := storedSource(t, s, "owner")
			sourcePinExec(t, s.DB, `UPDATE sources SET kind='platform',path='' WHERE id=$1`, remote.ID)
			sourcePinExec(t, s.DB, `UPDATE sources SET media_removed=true WHERE id=$1`, removed.ID)
			sourcePinExec(t, s.DB, `INSERT INTO source_media_cleanup(source_id,successful_job_id,protected_job_ids) VALUES($1,'synthetic-success','{}')`, pending.ID)
			admit := func(r ExportRequest, key string) (Job, error) {
				if limited {
					return s.CreateJobLimited(ctx, "owner", r, key, 20, 50)
				}
				return s.CreateJob(ctx, "owner", r, key)
			}
			for _, candidate := range []struct {
				name   string
				modify func(*ExportRequest)
				want   error
			}{
				{"foreign-audio", func(r *ExportRequest) { r.AudioReplace.SourceID = foreign.ID }, ErrNotFound},
				{"missing-audio", func(r *ExportRequest) { r.AudioReplace.SourceID = "missing" }, ErrNotFound},
				{"foreign-primary", func(r *ExportRequest) { r.SourceID = foreign.ID }, ErrNotFound},
				{"remote-primary", func(r *ExportRequest) { r.SourceID = remote.ID }, nil},
				{"remote-audio", func(r *ExportRequest) { r.AudioReplace.SourceID = remote.ID }, nil},
				{"removed-primary", func(r *ExportRequest) { r.SourceID = removed.ID }, ErrSourceMediaRemoved},
				{"removed-audio", func(r *ExportRequest) { r.AudioReplace.SourceID = removed.ID }, ErrSourceMediaRemoved},
				{"pending-audio", func(r *ExportRequest) { r.AudioReplace.SourceID = pending.ID }, ErrSourceMediaRemoved},
				{"same-source", func(r *ExportRequest) { r.AudioReplace.SourceID = primary.ID }, nil},
				{"local-path", func(r *ExportRequest) { r.AudioReplace.SourceID = ""; r.AudioReplace.Input = "/untrusted/file.wav" }, nil},
			} {
				r := audioRefRequest(primary, audio)
				candidate.modify(&r)
				if _, err := admit(r, ""); err == nil || candidate.want != nil && !errors.Is(err, candidate.want) {
					t.Fatalf("%s incorrectly admitted: %v", candidate.name, err)
				}
				var count int
				if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&count); err != nil || count != 0 {
					t.Fatal("refused admission retained partial work", candidate.name, count, err)
				}
			}
			r := audioRefRequest(primary, audio)
			job, err := admit(r, "stable-key")
			if err != nil {
				t.Fatal(err)
			}
			var ref string
			if err = s.DB.QueryRow(ctx, `SELECT audio_source_id FROM jobs WHERE id=$1`, job.ID).Scan(&ref); err != nil || ref != audio.ID {
				t.Fatal("external reference was not persisted", ref, err)
			}
			assertAudioRefPins(t, s, primary.ID, 1, true)
			assertAudioRefPins(t, s, audio.ID, 1, true)
			r.AudioReplace.SourceID = "missing"
			recovered, err := admit(r, "stable-key")
			if err != nil || recovered.ID != job.ID || recovered.Request.AudioReplace.SourceID != audio.ID {
				t.Fatal("idempotency replaced its original recording", recovered, err)
			}
			got, err := s.Source(ctx, audio.ID, "owner")
			if err != nil || !got.RetentionBlocked || got.ExpiresAt != nil {
				t.Fatal("external source presentation ignored retained job pin", got, err)
			}
		})
	}
}

func TestExternalAudioDirectMembershipTransitionsAndGuard(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a, b, c := storedSource(t, s, "owner"), storedSource(t, s, "owner"), storedSource(t, s, "owner")
	raw, _ := json.Marshal(audioRefRequest(a, b))
	sourcePinExec(t, s.DB, `INSERT INTO jobs(id,owner,source_id,request,items,idempotency_key) VALUES('direct','owner',$1,$2,'[]','one')`, a.ID, raw)
	assertAudioRefPins(t, s, a.ID, 1, true)
	assertAudioRefPins(t, s, b.ID, 1, true)
	revision, xmin := sourcePinRevision(t, s, b.ID)
	sourcePinExec(t, s.DB, `INSERT INTO jobs(id,owner,source_id,request,items,idempotency_key) VALUES('duplicate','owner',$1,$2,'[]','one') ON CONFLICT(owner,idempotency_key) DO NOTHING`, a.ID, raw)
	sourcePinExec(t, s.DB, `UPDATE jobs SET status='succeeded',lease_until=now() WHERE id='direct'`)
	if next, nextXmin := sourcePinRevision(t, s, b.ID); next != revision || nextXmin != xmin {
		t.Fatal("non-membership updates rewrote external pins")
	}
	for _, sql := range []string{`UPDATE jobs SET audio_source_id=NULL WHERE id='direct'`, `UPDATE jobs SET audio_source_id='` + c.ID + `' WHERE id='direct'`} {
		_, err := s.DB.Exec(ctx, sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatal("direct membership bypass accepted", err)
		}
	}
	sourcePinExec(t, s.DB, `UPDATE jobs SET request=jsonb_set(request,'{audio_replace,source_id}',to_jsonb($1::text)) WHERE id='direct'`, c.ID)
	assertAudioRefPins(t, s, b.ID, 0, true)
	assertAudioRefPins(t, s, c.ID, 1, true)
	revision, _ = sourcePinRevision(t, s, c.ID)
	sourcePinExec(t, s.DB, `UPDATE jobs SET id='renamed' WHERE id='direct'`)
	if next, _ := sourcePinRevision(t, s, c.ID); next != revision+1 {
		t.Fatal("external child cursor not invalidated by job ID move")
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE jobs SET request=request-'audio_replace' WHERE id='renamed'`); err != nil {
		t.Fatal(err)
	}
	rollbackStorage(tx)
	assertAudioRefPins(t, s, c.ID, 1, true)
	sourcePinExec(t, s.DB, `UPDATE jobs SET request=request-'audio_replace' WHERE id='renamed'`)
	assertAudioRefPins(t, s, c.ID, 0, true)
	assertAudioRefPins(t, s, a.ID, 1, true)
	sourcePinExec(t, s.DB, `DELETE FROM jobs WHERE id='renamed'`)
	assertAudioRefPins(t, s, a.ID, 0, true)
}

func TestExternalAudioBoundedAuditRepairsMixedReferencesAndRejectsFalseZero(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	target, other := storedSource(t, s, "owner"), storedSource(t, s, "owner")
	sourcePinExec(t, s.DB, `INSERT INTO jobs(id,owner,source_id,request,items,status) SELECT 'mixed-'||lpad(i::text,4,'0'),'owner',CASE WHEN i%2=0 THEN $1 ELSE $2 END,jsonb_build_object('audio_replace',jsonb_build_object('source_id',CASE WHEN i%2=0 THEN $2 ELSE $1 END)),'[]','succeeded' FROM generate_series(1,257) i`, target.ID, other.ID)
	assertAudioRefPins(t, s, target.ID, 257, true)
	sourcePinExec(t, s.DB, `UPDATE sources SET retention_job_pins=0,retention_pins_valid=true,created_at=now()-interval '2 hours' WHERE id=$1`, target.ID)
	tx, err := s.storageTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, sourceRetentionSQL, 3600, storageBatch)
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertAudioRefPins(t, s, target.ID, 0, false)
	tx, err = s.storageTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	var cursor *string
	var total int64
	for index, want := range []int64{128, 256, 257} {
		next, count, done, err := auditSourcePinChildren(ctx, tx, target.ID, false, cursor, total)
		if err != nil || count != want || done != (index == 2) {
			t.Fatalf("bounded mixed child scan %d: count%d done%v %v", index, count, done, err)
		}
		cursor, total = next, count
	}
	rollbackStorage(tx)
	for range 20 {
		_, _, err = s.auditSourcePinUnit(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	assertAudioRefPins(t, s, target.ID, 257, true)
	sourcePinExec(t, s.DB, `DELETE FROM jobs`)
	assertAudioRefPins(t, s, target.ID, 0, true)
	if _, err = s.Cleanup(ctx, time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Source(ctx, target.ID, "owner"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unreferenced expired audio source was not retained/deleted correctly", err)
	}
}

func TestExternalAudioDeletionProtectsActiveWorkAndPreservesCompletedResults(t *testing.T) {
	s, c, primary := sourceCleanupFixture(t)
	ctx := context.Background()
	audio := storedSource(t, s, "owner")
	j, err := s.CreateJob(ctx, "owner", audioRefRequest(primary, audio), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "running", "waiting_storage"} {
		sourcePinExec(t, s.DB, `UPDATE jobs SET status=$2 WHERE id=$1`, j.ID, status)
		for _, source := range []Source{primary, audio} {
			if _, err = s.DeleteSource(ctx, source.ID, "owner"); !errors.Is(err, ErrSourceInUse) {
				t.Fatalf("%s source accepted active %s delete: %v", source.ID, status, err)
			}
		}
	}
	sourcePinExec(t, s.DB, `UPDATE jobs SET status='succeeded' WHERE id=$1`, j.ID)
	sourcePinExec(t, s.DB, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES('external-job-reservation','owner','job',1,now()+interval '1 hour',$1)`, j.ID)
	if _, err = s.DeleteSource(ctx, audio.ID, "owner"); !errors.Is(err, ErrSourceInUse) {
		t.Fatal("external job reservation did not block deletion", err)
	}
	sourcePinExec(t, s.DB, `DELETE FROM storage_reservations WHERE id='external-job-reservation'`)
	sourcePinExec(t, s.DB, `INSERT INTO source_media_readers(id,source_id,expires_at) VALUES('external-reader',$1,now()+interval '1 hour')`, audio.ID)
	if _, err = s.DeleteSource(ctx, audio.ID, "owner"); !errors.Is(err, ErrSourceInUse) {
		t.Fatal("external reader did not block deletion", err)
	}
	sourcePinExec(t, s.DB, `DELETE FROM source_media_readers WHERE id='external-reader'`)
	path := writeLedgerFile(t, c, "artifacts", "retained-external.mp4", 19)
	sourcePinExec(t, s.DB, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms) VALUES('retained-external','owner',$1,$2,'clip.mp4',19,0,2000)`, j.ID, path)
	requestBefore := j.Request
	paths, err := s.DeleteSource(ctx, audio.ID, "owner")
	if err != nil {
		t.Fatal("finished external SET NULL deletion failed", err)
	}
	for _, p := range paths {
		if p == path || p == primary.Path {
			t.Fatal("external deletion scheduled primary/result bytes", p)
		}
	}
	got, err := s.Job(ctx, j.ID, "owner")
	if err != nil || !reflect.DeepEqual(got.Request, requestBefore) {
		t.Fatal("external deletion removed job/provenance", got, err)
	}
	var ref *string
	var artifacts int
	if err = s.DB.QueryRow(ctx, `SELECT audio_source_id,(SELECT count(*) FROM artifacts WHERE id='retained-external') FROM jobs WHERE id=$1`, j.ID).Scan(&ref, &artifacts); err != nil || ref != nil || artifacts != 1 {
		t.Fatal("SET NULL failed to preserve finished artifact", ref, artifacts, err)
	}
	assertAudioRefPins(t, s, primary.ID, 1, true)
	if _, err = s.Source(ctx, primary.ID, "owner"); err != nil {
		t.Fatal("primary source disappeared", err)
	}
}

func TestExternalAudioCleanupWaitsForReferencedJobsAndReservations(t *testing.T) {
	s, c, audio := sourceCleanupFixture(t)
	ctx := context.Background()
	primary := storedSource(t, s, "owner")
	j, err := s.CreateJob(ctx, "owner", audioRefRequest(primary, audio), "")
	if err != nil {
		t.Fatal(err)
	}
	sourcePinExec(t, s.DB, `INSERT INTO source_media_cleanup(source_id,successful_job_id,protected_job_ids) VALUES($1,'synthetic-success',ARRAY[$2])`, audio.ID, j.ID)
	for _, status := range []string{"queued", "waiting_storage", "running"} {
		sourcePinExec(t, s.DB, `UPDATE jobs SET status=$2 WHERE id=$1`, j.ID, status)
		if err = s.ProcessSourceMediaCleanup(ctx, c); err != nil {
			t.Fatal(err)
		}
		assertCleanupSource(t, s, audio, false)
	}
	sourcePinExec(t, s.DB, `UPDATE jobs SET status='succeeded' WHERE id=$1`, j.ID)
	sourcePinExec(t, s.DB, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES('audio-work','owner','job',1,now()+interval '1 hour',$1)`, j.ID)
	if err = s.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, audio, false)
	sourcePinExec(t, s.DB, `DELETE FROM storage_reservations WHERE id='audio-work'`)
	sourcePinExec(t, s.DB, `INSERT INTO source_media_readers(id,source_id,expires_at) VALUES('audio-reader',$1,now()+interval '1 hour')`, audio.ID)
	if err = s.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, audio, false)
	sourcePinExec(t, s.DB, `DELETE FROM source_media_readers WHERE id='audio-reader'`)
	if err = s.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, audio, true)
	if _, err = s.Job(ctx, j.ID, "owner"); err != nil {
		t.Fatal("media cleanup removed dependent job", err)
	}
	if _, err = s.Source(ctx, primary.ID, "owner"); err != nil {
		t.Fatal("audio media cleanup removed primary", err)
	}
}

func TestExternalAudioCleanupFlagRemovesOnlyPrimaryRecording(t *testing.T) {
	s, c, primary := sourceCleanupFixture(t)
	ctx := context.Background()
	audio := storedSource(t, s, "owner")
	r := audioRefRequest(primary, audio)
	r.CleanupSource = true
	j, err := s.CreateJob(ctx, "owner", r, "")
	if err != nil {
		t.Fatal(err)
	}
	token := newID("external_audio_lease")
	sourcePinExec(t, s.DB, `UPDATE jobs SET status='running',lease_token=$2,lease_until=now()+interval '1 minute' WHERE id=$1`, j.ID, token)
	j.Status = "running"
	j = finishCleanupJob(t, s, c, j, token, "succeeded")
	if err = s.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, primary, true)
	assertCleanupSource(t, s, audio, false)
	if _, err = s.Job(ctx, j.ID, "owner"); err != nil {
		t.Fatal("primary cleanup lost replacement job", err)
	}
	assertAudioRefPins(t, s, audio.ID, 1, true)
}

func TestExternalAudioAdmissionAndDeletionShareFenceWithoutDeadlock(t *testing.T) {
	s := testStore(t)
	primary := storedSource(t, s, "owner")
	for range 4 {
		audio := storedSource(t, s, "owner")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		var job Job
		var admissionErr, deletionErr error
		go func() {
			defer wait.Done()
			<-start
			job, admissionErr = s.CreateJobLimited(ctx, "owner", audioRefRequest(primary, audio), "", 20, 50)
		}()
		go func() { defer wait.Done(); <-start; _, deletionErr = s.DeleteSource(ctx, audio.ID, "owner") }()
		close(start)
		wait.Wait()
		cancel()
		if admissionErr == nil {
			if !errors.Is(deletionErr, ErrSourceInUse) {
				t.Fatal("admitted recording was concurrently deleted", job.ID, deletionErr)
			}
			assertAudioRefPins(t, s, audio.ID, 1, true)
		} else if !errors.Is(admissionErr, ErrNotFound) || deletionErr != nil {
			t.Fatal("concurrent source admission/delete did not settle atomically", admissionErr, deletionErr)
		}
	}
}

func TestExternalAudioRuntimeDMLGrantsInvokeDerivedPinTriggers(t *testing.T) {
	f := newMigrationFixture(t)
	admin := openMigrationStore(t, f)
	ctx := context.Background()
	a, b := storedSource(t, admin, "owner"), storedSource(t, admin, "owner")
	role, password := newID("audio_runtime"), newID("synthetic")
	roleSQL := pgx.Identifier{role}.Sanitize()
	namespace := pgx.Identifier{f.namespace}.Sanitize()
	migrationExec(t, f.db, "CREATE ROLE "+roleSQL+" LOGIN PASSWORD '"+password+"'")
	t.Cleanup(func() {
		_, _ = f.db.Exec(ctx, "DROP OWNED BY "+roleSQL)
		if _, err := f.db.Exec(ctx, "DROP ROLE "+roleSQL); err != nil {
			t.Error("owned audio runtime role cleanup failed")
		}
	})
	migrationExec(t, f.db, "GRANT USAGE ON SCHEMA "+namespace+" TO "+roleSQL)
	for table := range latestSchemaShape().Tables {
		grants := "SELECT,INSERT,UPDATE,DELETE"
		if table == "app_schema_versions" || table == "app_schema_migrations" {
			grants = "SELECT,MAINTAIN"
		}
		migrationExec(t, f.db, "GRANT "+grants+" ON TABLE "+namespace+"."+pgx.Identifier{table}.Sanitize()+" TO "+roleSQL)
	}
	migrationExec(t, f.db, "REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA "+namespace+" FROM PUBLIC,"+roleSQL)
	parsed, err := url.Parse(f.url)
	if err != nil {
		t.Fatal("synthetic runtime URL invalid")
	}
	parsed.User = url.UserPassword(role, password)
	runtime, err := OpenStore(ctx, parsed.String())
	if err != nil {
		t.Fatal("DML runtime startup refused; private fixture connection withheld")
	}
	defer runtime.DB.Close()
	j, err := runtime.CreateJob(ctx, "owner", audioRefRequest(a, b), "runtime")
	if err != nil {
		t.Fatal("DML invoker trigger insert failed", err)
	}
	assertAudioRefPins(t, admin, a.ID, 1, true)
	assertAudioRefPins(t, admin, b.ID, 1, true)
	if _, err = runtime.DB.Exec(ctx, `DELETE FROM jobs WHERE id=$1`, j.ID); err != nil {
		t.Fatal("DML invoker trigger delete failed", err)
	}
	assertAudioRefPins(t, admin, a.ID, 0, true)
	assertAudioRefPins(t, admin, b.ID, 0, true)
	_, err = runtime.DB.Exec(ctx, `CREATE TABLE forbidden_audio_runtime_ddl(id int)`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatal("DML runtime unexpectedly has DDL rights")
	}
}
