package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const jsonReadTimeout = 10 * time.Second

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	return decodeJSONLimit(w, r, target, 1<<20)
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	// JSON metadata is small. Do not give stalled JSON bodies the server's
	// much longer media-upload timeout, which retains a connection/goroutine.
	deadline := time.Now().Add(jsonReadTimeout)
	if parent, ok := r.Context().Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		internalError(w, err)
		return err
	}
	complete := false
	defer func() {
		if complete {
			_ = controller.SetReadDeadline(time.Time{})
		}
	}()
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		if r.ProtoMajor == 1 {
			w.Header().Set("Connection", "close")
		}
		if sourceTimedOut(r.Context(), err) {
			writeError(w, http.StatusRequestTimeout, "request_timeout", "The request timed out; try again")
		} else {
			writeError(w, 400, "invalid_request", "Send a valid JSON request with supported fields")
		}
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		if r.ProtoMajor == 1 {
			w.Header().Set("Connection", "close")
		}
		if sourceTimedOut(r.Context(), err) {
			writeError(w, http.StatusRequestTimeout, "request_timeout", "The request timed out; try again")
		} else {
			writeError(w, 400, "invalid_request", "Send exactly one JSON object")
		}
		return errors.New("trailing JSON")
	}
	complete = true
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]APIError{"error": {Code: code, Message: message}})
}

func lookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrSourceMediaRemoved) {
		sourceMediaError(w, err)
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, 404, "not_found", "The resource was not found or is not accessible in this session")
		return
	}
	internalError(w, err)
}

func internalError(w http.ResponseWriter, err error) {
	if errors.Is(err, errDatabaseUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", sourcePersistenceMessage(err, "Database is temporarily unavailable; try again"))
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusRequestTimeout, "request_timeout", sourcePersistenceMessage(err, "The request timed out; try again"))
		return
	}
	if errors.Is(err, ErrStorageInitializing) {
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, "storage_initializing", "Storage accounting is being prepared; try again shortly")
		return
	}
	attributes := []any{"error", fmt.Sprintf("%T", err)}
	// A bounded database code helps distinguish constraints from exhausted
	// storage without logging statements, parameters or private diagnostics.
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) && len(postgres.Code) == 5 {
		valid := true
		for _, character := range postgres.Code {
			if !(character >= 'A' && character <= 'Z' || character >= '0' && character <= '9') {
				valid = false
			}
		}
		if valid {
			attributes = append(attributes, "sqlstate", postgres.Code)
		}
	}
	slog.Error("request failed", attributes...)
	writeError(w, 500, "internal", sourcePersistenceMessage(err, "The operation could not be completed; try again"))
}
