package app

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStorePoolDefaultIsBoundedAndExplicitSettingsArePreserved(t *testing.T) {
	for _, tt := range []struct {
		name, connection string
		max, min, idle   int32
	}{
		{"url default", "postgres://localhost/test", 4, 0, 0},
		{"keyword default", "host=localhost dbname=test", 4, 0, 0},
		{"url maximum", "postgres://localhost/test?pool_max_conns=9", 9, 0, 0},
		{"keyword maximum", "host=localhost pool_max_conns='2'", 2, 0, 0},
		{"larger minimum", "postgres://localhost/test?pool_min_conns=8", 8, 8, 0},
		{"larger idle minimum", "host=localhost pool_min_idle_conns=12", 12, 0, 12},
		{"both minimums", "host=localhost pool_min_conns=9 pool_min_idle_conns=6", 9, 9, 6},
		{"explicit limits", "postgres://localhost/test?pool_max_conns=16&pool_min_conns=8&pool_min_idle_conns=6", 16, 8, 6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config, err := storePoolConfig(tt.connection)
			if err != nil {
				t.Fatal(err)
			}
			if config.MaxConns != tt.max || config.MinConns != tt.min || config.MinIdleConns != tt.idle {
				t.Fatalf("pool settings: max=%d min=%d idle=%d; want %d/%d/%d", config.MaxConns, config.MinConns, config.MinIdleConns, tt.max, tt.min, tt.idle)
			}
		})
	}
	for _, raw := range []string{"postgres://localhost/test?pool_max_conns=0", "host=localhost pool_max_conns=invalid", "host=localhost pool_min_conns=invalid"} {
		if _, err := storePoolConfig(raw); err == nil {
			t.Fatal("invalid explicit pool setting was accepted")
		}
	}
}

func TestStorePoolIdleDefaultsPreserveExplicitDurations(t *testing.T) {
	for _, tt := range []struct {
		name, connection  string
		idle, health      time.Duration
		max, min, minIdle int32
	}{
		{"url defaults", "postgres://localhost/test", 5 * time.Second, time.Second, 4, 0, 0},
		{"keyword defaults", "host=localhost dbname=test", 5 * time.Second, time.Second, 4, 0, 0},
		{"url idle only", "postgres://localhost/test?pool_max_conn_idle_time=2m", 2 * time.Minute, time.Second, 4, 0, 0},
		{"keyword idle only", "host=localhost pool_max_conn_idle_time='3m'", 3 * time.Minute, time.Second, 4, 0, 0},
		{"url health only", "postgres://localhost/test?pool_health_check_period=17s", 5 * time.Second, 17 * time.Second, 4, 0, 0},
		{"keyword health only", "host=localhost pool_health_check_period='19s'", 5 * time.Second, 19 * time.Second, 4, 0, 0},
		{"url both", "postgres://localhost/test?pool_max_conn_idle_time=1h&pool_health_check_period=7m", time.Hour, 7 * time.Minute, 4, 0, 0},
		{"keyword both fractional", "host=localhost pool_max_conn_idle_time='125ms' pool_health_check_period='12.5ms'", 125 * time.Millisecond, 12500 * time.Microsecond, 4, 0, 0},
		{"explicit zero idle", "host=localhost pool_max_conn_idle_time='0'", 0, time.Second, 4, 0, 0},
		{"url limits unchanged", "postgres://localhost/test?pool_max_conns=16&pool_min_conns=8&pool_min_idle_conns=6&pool_max_conn_idle_time=40s&pool_health_check_period=3s", 40 * time.Second, 3 * time.Second, 16, 8, 6},
		{"keyword limits unchanged", "host=localhost pool_max_conns=12 pool_min_conns=7 pool_min_idle_conns=5 pool_max_conn_idle_time='45s' pool_health_check_period='4s'", 45 * time.Second, 4 * time.Second, 12, 7, 5},
		{"implicit maximum follows minimum", "host=localhost pool_min_conns=8 pool_min_idle_conns=6", 5 * time.Second, time.Second, 8, 8, 6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config, err := storePoolConfig(tt.connection)
			if err != nil {
				t.Fatal(err)
			}
			if config.MaxConnIdleTime != tt.idle || config.HealthCheckPeriod != tt.health {
				t.Fatalf("idle settings: idle=%s health=%s; want %s/%s", config.MaxConnIdleTime, config.HealthCheckPeriod, tt.idle, tt.health)
			}
			if config.MaxConns != tt.max || config.MinConns != tt.min || config.MinIdleConns != tt.minIdle {
				t.Fatalf("idle policy changed pool capacity: max=%d min=%d idle=%d; want %d/%d/%d", config.MaxConns, config.MinConns, config.MinIdleConns, tt.max, tt.min, tt.minIdle)
			}
			for _, key := range []string{"pool_max_conn_idle_time", "pool_health_check_period"} {
				if _, leaked := config.ConnConfig.RuntimeParams[key]; leaked {
					t.Fatalf("pool-only parameter %s leaked into PostgreSQL connection settings", key)
				}
			}
		})
	}
}

func TestStorePoolInvalidIdleDurationsRemainParserErrors(t *testing.T) {
	for _, tt := range []struct{ name, connection string }{
		{"url invalid idle", "postgres://localhost/test?pool_max_conn_idle_time=invalid"},
		{"keyword invalid idle", "host=localhost pool_max_conn_idle_time='invalid'"},
		{"url invalid health", "postgres://localhost/test?pool_health_check_period=invalid"},
		{"keyword invalid health", "host=localhost pool_health_check_period='invalid'"},
		{"url empty idle", "postgres://localhost/test?pool_max_conn_idle_time="},
		{"keyword empty health", "host=localhost pool_health_check_period=''"},
		{"overflow idle", "host=localhost pool_max_conn_idle_time='999999999999999h'"},
		{"overflow health", "postgres://localhost/test?pool_health_check_period=999999999999999h"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := storePoolConfig(tt.connection)
			var parseError *pgconn.ParseConfigError
			if !errors.As(err, &parseError) {
				t.Fatal("invalid explicit idle setting was replaced by a default")
			}
		})
	}
}

func TestStorePoolIdleRetirementPreservesAcquiredTransaction(t *testing.T) {
	// Use a separate two-connection pool and no application tables. Explicit
	// short durations exercise real PostgreSQL retirement without a 30s sleep.
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	q := u.Query()
	q.Set("pool_max_conns", "2")
	q.Set("pool_min_conns", "0")
	q.Set("pool_min_idle_conns", "0")
	q.Set("pool_max_conn_idle_time", "100ms")
	q.Set("pool_health_check_period", "20ms")
	q.Set("pool_max_conn_lifetime", "1h")
	u.RawQuery = q.Encode()
	config, err := storePoolConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	held, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	tx, err := held.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, "CREATE TEMP TABLE store_pool_idle_marker(value integer); INSERT INTO store_pool_idle_marker VALUES(42)"); err != nil {
		t.Fatal(err)
	}
	heldPID := held.Conn().PgConn().PID()
	spare, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sparePID := spare.Conn().PgConn().PID()
	spare.Release()
	if heldPID == sparePID {
		t.Fatal("fixture did not establish two independent PostgreSQL connections")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		stat := pool.Stat()
		if stat.MaxIdleDestroyCount() >= 1 && stat.TotalConns() == 1 && stat.AcquiredConns() == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("released connection was not retired while transaction remained acquired: total=%d acquired=%d idle_destroyed=%d", stat.TotalConns(), stat.AcquiredConns(), stat.MaxIdleDestroyCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var livePID uint32
	var marker int
	if err = tx.QueryRow(ctx, "SELECT pg_backend_pid(),value FROM store_pool_idle_marker").Scan(&livePID, &marker); err != nil {
		t.Fatal("idle retirement interrupted the acquired transaction", err)
	}
	if livePID != heldPID || marker != 42 {
		t.Fatal("idle retirement replaced the acquired transaction or changed its data")
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal("acquired transaction could not commit after spare retirement", err)
	}
	if err = held.QueryRow(ctx, "SELECT value FROM store_pool_idle_marker").Scan(&marker); err != nil || marker != 42 {
		t.Fatal("transaction data was not committed on the retained connection", err)
	}
}
