package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Provider identity intentionally has no content SHA or size. A canonical URL
// digest identifies a request, not the bytes later resolved by a provider.
type SourceFingerprint struct {
	Kind               string `json:"kind"`
	SHA256             string `json:"sha256,omitempty"`
	SizeBytes          int64  `json:"size_bytes,omitempty"`
	Provider           string `json:"provider,omitempty"`
	ProviderID         string `json:"provider_id,omitempty"`
	CanonicalURLSHA256 string `json:"canonical_url_sha256,omitempty"`
}

type sourcePreparationIntent struct {
	Kind      string `json:"kind"`
	URL       string `json:"url,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type SourcePreparationError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type SourcePreparationReceipt struct {
	Version      int                     `json:"version"`
	Key          string                  `json:"key"`
	State        string                  `json:"state"`
	SourceID     string                  `json:"source_id,omitempty"`
	Source       *Source                 `json:"source,omitempty"`
	Fingerprint  *SourceFingerprint      `json:"fingerprint,omitempty"`
	RetryAfterMS int64                   `json:"retry_after_ms,omitempty"`
	Error        *SourcePreparationError `json:"error,omitempty"`
}

type sourcePreparationClaim struct {
	key, token, sourceID string
	intent               sourcePreparationIntent
	writer               *preparationResponseWriter
	server               *Server
	owner                string
}

func validPreparationKey(key string) bool {
	if len(key) < 1 || len(key) > 128 || key == "." || key == ".." || strings.TrimSpace(key) != key {
		return false
	}
	for _, b := range []byte(key) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}

func lowercaseSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

func (s *Server) uploadPreparationIntent(w http.ResponseWriter, r *http.Request) (sourcePreparationIntent, bool) {
	intent := sourcePreparationIntent{Kind: "upload"}
	if r.Header.Get("Idempotency-Key") == "" {
		return intent, true
	}
	intent.SHA256 = r.Header.Get("Upload-SHA256")
	var err error
	intent.SizeBytes, err = strconv.ParseInt(r.Header.Get("Upload-Size"), 10, 64)
	if !lowercaseSHA(intent.SHA256) || err != nil || intent.SizeBytes <= 0 {
		writeError(w, 400, "invalid_source_fingerprint", "Idempotent uploads require Upload-SHA256 and a positive Upload-Size")
		return intent, false
	}
	return intent, true
}

func preparationError(code string, retryable bool) *SourcePreparationError {
	if code == "" {
		code = "source_preparation_interrupted"
	}
	return &SourcePreparationError{Code: code, Message: "Source preparation did not complete; inspect this receipt before retrying the same intent", Retryable: retryable}
}

func (s *Store) SourcePreparation(ctx context.Context, owner, key string) (SourcePreparationReceipt, error) {
	r := SourcePreparationReceipt{Version: 1, Key: key}
	var until time.Time
	var raw []byte
	var code string
	err := s.DB.QueryRow(ctx, `SELECT state,source_id,lease_until,fingerprint,error_code FROM source_preparations WHERE owner=$1 AND key=$2`, owner, key).Scan(&r.State, &r.SourceID, &until, &raw, &code)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &r.Fingerprint); err != nil {
			return r, err
		}
	}
	if r.State == "processing" {
		if time.Now().Before(until) {
			r.RetryAfterMS = max(1, time.Until(until).Milliseconds())
		} else {
			r.State = "retryable"
		}
	}
	if r.State == "ready" {
		source, e := s.Source(ctx, r.SourceID, owner)
		if e == nil {
			r.Source = &source
			r.Fingerprint = source.Fingerprint
		} else if errors.Is(e, ErrNotFound) {
			r.Error = preparationError("source_unavailable", false)
		} else {
			return r, e
		}
	} else if r.State == "failed" || r.State == "retryable" {
		r.Error = preparationError(code, r.State == "retryable")
	}
	return r, nil
}

var errPreparationConflict = errors.New("source preparation intent conflict")
var errPreparationLimit = errors.New("source preparation receipt limit")

func (s *Store) claimSourcePreparation(ctx context.Context, owner, key string, intent sourcePreparationIntent, lease time.Duration) (*sourcePreparationClaim, error) {
	data, _ := json.Marshal(intent)
	sum := sha256.Sum256(data)
	identity := hex.EncodeToString(sum[:])
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "source-preparation:"+owner); err != nil {
		return nil, err
	}
	// Retain a key while its source exists, even beyond the receipt TTL. A stale
	// receipt must never silently authorize a second live source for that key.
	if _, err = tx.Exec(ctx, `DELETE FROM source_preparations WHERE (owner,key) IN (SELECT p.owner,p.key FROM source_preparations p WHERE p.expires_at<clock_timestamp() AND p.lease_until<clock_timestamp() AND NOT EXISTS(SELECT 1 FROM sources s WHERE s.id=p.source_id AND s.owner=p.owner) ORDER BY p.expires_at,p.owner,p.key LIMIT 200)`); err != nil {
		return nil, err
	}
	var stored, state, id string
	var until time.Time
	err = tx.QueryRow(ctx, `SELECT intent_hash,state,source_id,lease_until FROM source_preparations WHERE owner=$1 AND key=$2 FOR UPDATE`, owner, key).Scan(&stored, &state, &id, &until)
	claim := &sourcePreparationClaim{key: key, token: newID("claim"), sourceID: id, intent: intent, owner: owner}
	if err == nil {
		if stored != identity {
			return nil, errPreparationConflict
		}
		if state == "ready" || state == "failed" || (state == "processing" && time.Now().Before(until)) {
			return nil, nil
		}
		_, err = tx.Exec(ctx, `UPDATE source_preparations SET state='processing',claim_token=$3,lease_until=clock_timestamp()+($4*interval '1 second'),error_code='' WHERE owner=$1 AND key=$2`, owner, key, claim.token, lease.Seconds())
	} else if errors.Is(err, pgx.ErrNoRows) {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM source_preparations WHERE owner=$1`, owner).Scan(&count); err != nil {
			return nil, err
		}
		if count >= 64 {
			return nil, errPreparationLimit
		}
		claim.sourceID = newID("src")
		_, err = tx.Exec(ctx, `INSERT INTO source_preparations(owner,key,intent_hash,kind,source_id,claim_token,state,lease_until) VALUES($1,$2,$3,$4,$5,$6,'processing',clock_timestamp()+($7*interval '1 second'))`, owner, key, identity, intent.Kind, claim.sourceID, claim.token, lease.Seconds())
	} else {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return claim, err
	}
	return claim, nil
}

func (s *Server) startSourcePreparation(w http.ResponseWriter, r *http.Request, owner string, intent sourcePreparationIntent) (*sourcePreparationClaim, http.ResponseWriter, bool) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return nil, w, true
	}
	if !validPreparationKey(key) {
		writeError(w, 400, "invalid_idempotency_key", "Use an Idempotency-Key of 1 to 128 visible ASCII bytes")
		return nil, w, false
	}
	claim, err := apiDatabase(r.Context(), func(ctx context.Context) (*sourcePreparationClaim, error) {
		return s.Store.claimSourcePreparation(ctx, owner, key, intent, max(uploadTimeout(s.Config), s.Config.SourceTimeout)+time.Minute)
	})
	if errors.Is(err, errPreparationConflict) {
		writeError(w, 409, "source_preparation_conflict", "This source preparation key belongs to a different immutable intent")
		return nil, w, false
	}
	if errors.Is(err, errPreparationLimit) {
		writeError(w, 429, "source_preparation_limit", "This owner has reached its retained source preparation receipt limit")
		return nil, w, false
	}
	if err != nil {
		// No transfer has begun. If claiming COMMIT lost its acknowledgement,
		// release only this token so a retained empty intent is retryable now.
		if claim != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), apiDatabaseTimeout)
			_, _ = s.Store.DB.Exec(cleanup, `UPDATE source_preparations SET state='retryable',lease_until=clock_timestamp() WHERE owner=$1 AND key=$2 AND claim_token=$3 AND state='processing'`, owner, key, claim.token)
			cancel()
		}
		lookupError(w, err)
		return nil, w, false
	}
	if claim == nil {
		receipt, e := apiDatabase(r.Context(), func(ctx context.Context) (SourcePreparationReceipt, error) {
			return s.Store.SourcePreparation(ctx, owner, key)
		})
		if e != nil {
			lookupError(w, e)
			return nil, w, false
		}
		if receipt.State == "ready" && receipt.Source != nil {
			writeJSON(w, 200, receipt.Source)
			return nil, w, false
		}
		if receipt.State == "ready" {
			writeError(w, 410, "source_unavailable", "The retained source is no longer available; a new preparation is explicit")
			return nil, w, false
		}
		if receipt.State == "failed" {
			writeError(w, 422, receipt.Error.Code, receipt.Error.Message)
			return nil, w, false
		}
		w.Header().Set("Retry-After", strconv.FormatInt(max(1, (receipt.RetryAfterMS+999)/1000), 10))
		writeError(w, 409, "source_preparation_in_progress", "This preparation is already running; reconcile its retained receipt")
		return nil, w, false
	}
	claim.server = s
	claim.writer = &preparationResponseWriter{ResponseWriter: w}
	return claim, claim.writer, true
}

func preparationSource(claim *sourcePreparationClaim) string {
	if claim != nil {
		return claim.sourceID
	}
	return newID("src")
}
func (claim *sourcePreparationClaim) attach(source *Source) {
	if claim != nil {
		source.PreparationKey = claim.key
		source.PreparationClaim = claim.token
		source.PreparationPathID = claim.sourceID + "-" + claim.token
	}
}

func sourceFileIdentity(source Source) string {
	if source.PreparationPathID != "" {
		return source.PreparationPathID
	}
	return source.ID
}

type preparationResponseWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *preparationResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *preparationResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *preparationResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.body.Len()+len(b) <= 4096 {
		_, _ = w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (claim *sourcePreparationClaim) finish() {
	if claim == nil {
		return
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(claim.writer.body.Bytes(), &body)
	code := body.Error.Code
	state := "retryable"
	if code == "unsupported_media" || code == "source_too_large" || code == "live_not_supported" {
		state = "failed"
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiDatabaseTimeout)
	defer cancel()
	// Ready is committed with the source. A lost acknowledgement or response must
	// never let failure cleanup downgrade that durable success.
	_, _ = claim.server.Store.DB.Exec(ctx, `UPDATE source_preparations SET state=$4,error_code=$5,lease_until=clock_timestamp() WHERE owner=$1 AND key=$2 AND claim_token=$3 AND state='processing'`, claim.owner, claim.key, claim.token, state, code)
}

func (s *Server) sourcePreparation(w http.ResponseWriter, r *http.Request, owner string) {
	key := r.PathValue("key")
	if !validPreparationKey(key) {
		writeError(w, 400, "invalid_idempotency_key", "Invalid source preparation key")
		return
	}
	receipt, err := apiDatabase(r.Context(), func(ctx context.Context) (SourcePreparationReceipt, error) {
		return s.Store.SourcePreparation(ctx, owner, key)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	if receipt.RetryAfterMS > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(max(1, (receipt.RetryAfterMS+999)/1000), 10))
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, receipt)
}

func verifyUploadedFingerprint(claim *sourcePreparationClaim, fingerprint *SourceFingerprint) error {
	if claim == nil {
		return nil
	}
	if fingerprint == nil || fingerprint.Kind != "content" || fingerprint.SHA256 != claim.intent.SHA256 || fingerprint.SizeBytes != claim.intent.SizeBytes {
		return fmt.Errorf("uploaded content does not match its declared fingerprint")
	}
	return nil
}
