package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQL can reject a statement normally, then stop responding while the
// connection remains open. Cancellation of the operation's earlier context
// cannot bound a rollback issued with a fresh background context.
func stallingRollbackStore(t *testing.T) (*Store, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop, cancel := context.WithCancel(context.Background())
	rollbackSeen := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			conn, err := listener.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			closeOnStop := context.AfterFunc(stop, func() { _ = conn.Close() })
			defer closeOnStop()
			backend := pgproto3.NewBackend(conn, conn)
			if _, err = backend.ReceiveStartupMessage(); err != nil {
				return err
			}
			backend.Send(&pgproto3.AuthenticationOk{})
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if err = backend.Flush(); err != nil {
				return err
			}
			for {
				message, err := backend.Receive()
				if err != nil {
					return err
				}
				query, ok := message.(*pgproto3.Query)
				if !ok {
					return errors.New("fake PostgreSQL expected a simple query")
				}
				switch query.String {
				case "begin":
					backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("BEGIN")})
					backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
				case "rollback":
					close(rollbackSeen)
					// Intentionally send no response. A bounded rollback must close
					// its own connection, rather than wait for test cleanup.
					_, err = io.Copy(io.Discard, conn)
					return err
				default:
					backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "23505", Message: "synthetic statement refusal"})
					backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'E'})
				}
				if err = backend.Flush(); err != nil {
					return err
				}
			}
		}()
	}()
	pool, err := pgxpool.New(context.Background(), "postgres://audit@"+listener.Addr().String()+"/audit?sslmode=disable&default_query_exec_mode=simple_protocol&pool_max_conns=1")
	if err != nil {
		cancel()
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		listener.Close()
		pool.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
				t.Error("fake PostgreSQL failed", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("fake PostgreSQL did not stop")
		}
	})
	return &Store{DB: pool}, rollbackSeen
}

func TestHTTPProjectAndPreparationRollbackHasIndependentBound(t *testing.T) {
	for _, operation := range []string{"source-preparation", "issue-handoff", "redeem-handoff", "revoke-grants"} {
		t.Run(operation, func(t *testing.T) {
			store, rollbackSeen := stallingRollbackStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				if operation == "source-preparation" {
					_ = apiDatabaseExec(ctx, func(dbCtx context.Context) error {
						_, err := store.claimSourcePreparation(dbCtx, "owner", "rollback-test", sourcePreparationIntent{Kind: "import", URL: "https://example.com/video.mp4"}, time.Minute)
						return err
					})
					return
				}
				server := NewServer(Config{}, store)
				body := []byte("{}")
				if operation == "redeem-handoff" {
					body = []byte(`{"token":"` + base64.RawURLEncoding.EncodeToString(make([]byte, 32)) + `"}`)
				}
				r := httptest.NewRequest("POST", "https://example.com/", bytes.NewReader(body)).WithContext(ctx)
				r.SetPathValue("project", "project_rollback_test")
				w := httptest.NewRecorder()
				switch operation {
				case "issue-handoff":
					server.issueProjectHandoff(w, r, "owner")
				case "redeem-handoff":
					server.redeemProjectHandoff(w, r, "recipient")
				case "revoke-grants":
					server.revokeAgentProject(w, r, "owner", false)
				}
			}()
			select {
			case <-rollbackSeen:
			case <-done:
				t.Fatal("operation did not reach rollback")
			case <-time.After(3 * time.Second):
				t.Fatal("operation did not reach fake PostgreSQL")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(2500 * time.Millisecond):
				t.Fatal("rollback remained blocked after the independent cleanup budget")
			}
		})
	}
}
