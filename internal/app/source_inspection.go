package app

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Track inspection creates no media files. A zero-byte reservation uses the
// existing preview membership to protect deletion/retention and shares the
// same global admission slots, including across API processes.
func (s *Store) reserveSourceInspection(ctx context.Context, source Source, id string, timeout time.Duration) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	var path string
	if err = tx.QueryRow(ctx, "SELECT path FROM sources WHERE id=$1 AND owner=$2 FOR KEY SHARE", source.ID, source.Owner).Scan(&path); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if path == "" || path != source.Path {
		return ErrNotFound
	}
	var weight int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(`+previewWeightSQL+`),0) FROM storage_reservations r LEFT JOIN sources s ON s.id=r.job_id WHERE r.kind='preview'`).Scan(&weight); err != nil {
		return err
	}
	needed := 1
	if source.Width <= 0 || source.Height <= 0 || source.Width > 8_388_608/source.Height {
		needed = 2
	}
	if weight+needed > 2 {
		return errSourceServerBusy
	}
	token := id
	if needed == 2 {
		token = "inspection-full:" + id
	}
	_, err = tx.Exec(ctx, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id,token) VALUES($1,$2,'preview',0,clock_timestamp()+($3*interval '1 second'),$4,$5)`, id, source.Owner, (timeout + time.Minute).Seconds(), source.ID, token)
	if err != nil {
		return err
	}
	return commitPreviewAdmission(ctx, tx)
}

func (s *Server) pinSourceInspection(ctx context.Context, source Source, timeout time.Duration) (func(), error) {
	return s.pinSourceInspectionWithReserve(ctx, source, timeout, s.Store.reserveSourceInspection)
}

func (s *Server) pinSourceInspectionWithReserve(ctx context.Context, source Source, timeout time.Duration, reserve func(context.Context, Source, string, time.Duration) error) (func(), error) {
	id := newID("inspection")
	release := func() { s.releasePreviewAdmission(source, id, false) }
	err := apiDatabaseExec(ctx, func(dbCtx context.Context) error { return reserve(dbCtx, source, id, timeout) })
	if err != nil {
		if errors.Is(err, errPreviewAdmissionUncertain) {
			release()
		}
		return nil, err
	}
	return release, nil
}
