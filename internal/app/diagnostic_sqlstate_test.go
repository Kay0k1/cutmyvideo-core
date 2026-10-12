package app

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestDiagnosticSQLStateExcludesPrivateFieldsAndInvalidCodes(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, code := range []string{"23505", "private-connection-fixture", "bad\n1"} {
		var output bytes.Buffer
		slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
		cause := &pgconn.PgError{Code: code, Message: "private-message-fixture", Detail: "private-detail-fixture", InternalQuery: "private-query-fixture", ConstraintName: "private-constraint-fixture"}
		response := httptest.NewRecorder()
		internalError(response, errors.Join(errors.New("private-wrapped-fixture"), cause))
		for _, private := range []string{"private-message-fixture", "private-detail-fixture", "private-query-fixture", "private-constraint-fixture", "private-wrapped-fixture", "private-connection-fixture", "bad\n1"} {
			if strings.Contains(output.String(), private) || strings.Contains(response.Body.String(), private) {
				t.Fatal("diagnostic disclosed a private field")
			}
		}
		if strings.Contains(output.String(), "sqlstate=") != (code == "23505") {
			t.Fatalf("SQLSTATE logging guard did not match code class: %s", output.String())
		}
	}
}
