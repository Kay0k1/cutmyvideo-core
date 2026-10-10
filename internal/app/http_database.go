package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"
)

const apiDatabaseTimeout = 5 * time.Second

var errDatabaseUnavailable = errors.New("database temporarily unavailable")
var errAPIDatabaseTimeout = errors.New("API database deadline exceeded")

// Apply the deadline to a database operation, including pool acquisition, rather
// than the request: uploads, preview rendering and downloads have their own budgets.
func apiDatabase[T any](parent context.Context, operation func(context.Context) (T, error)) (T, error) {
	if err := parent.Err(); err != nil {
		var zero T
		return zero, err
	}
	ctx, cancel := context.WithTimeoutCause(parent, apiDatabaseTimeout, errAPIDatabaseTimeout)
	defer cancel()
	value, err := operation(ctx)
	return value, apiDatabaseError(parent, ctx, err)
}

func apiDatabaseExec(parent context.Context, operation func(context.Context) error) error {
	_, err := apiDatabase(parent, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, operation(ctx)
	})
	return err
}

func apiDatabaseError(parent, ctx context.Context, err error) error {
	if err == nil {
		return err
	}
	if parentErr := parent.Err(); parentErr != nil {
		// A driver failure may race the caller's cancellation without wrapping
		// it. Keep caller priority and every cause, including uncertain COMMIT.
		return errors.Join(parentErr, err)
	}
	// Preserve the underlying cause, especially an uncertain publication COMMIT.
	if errors.Is(context.Cause(ctx), errAPIDatabaseTimeout) {
		return errors.Join(errDatabaseUnavailable, err)
	}
	var connection *pgconn.ConnectError
	var network net.Error
	var postgres *pgconn.PgError
	if errors.Is(err, puddle.ErrClosedPool) || errors.As(err, &connection) || errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		(errors.As(err, &postgres) && (len(postgres.Code) >= 2 && postgres.Code[:2] == "08" || postgres.Code == "57P01" || postgres.Code == "57P02" || postgres.Code == "57P03" || postgres.Code == "53300")) {
		return errors.Join(errDatabaseUnavailable, err)
	}
	return err
}

func (s *Server) databaseSource(ctx context.Context, id, owner string) (Source, error) {
	return apiDatabase(ctx, func(ctx context.Context) (Source, error) {
		return s.Store.Source(ctx, id, owner)
	})
}

func (s *Server) databaseAddSource(ctx context.Context, source Source) error {
	return apiDatabaseExec(ctx, func(ctx context.Context) error { return s.Store.AddSource(ctx, source) })
}

func writeSourcePersistenceError(w http.ResponseWriter, ctx context.Context, err error) {
	if errors.Is(err, errDatabaseUnavailable) {
		internalError(w, err)
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, "source_timeout", "Source preparation timed out; try again")
	} else {
		internalError(w, err)
	}
}
