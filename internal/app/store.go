package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	DB          *pgxpool.Pool
	configMu    sync.RWMutex
	config      Config
	localProbes localProbeCache
}

type dbExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

var ErrBusy = errors.New("too many active jobs")

var ErrJobTooLarge = errors.New("export cannot fit the configured storage budget")

func OpenStore(ctx context.Context, url string) (*Store, error) {
	ctx, cancel := context.WithTimeout(ctx, storeStartupTimeout)
	defer cancel()
	config, err := storePoolConfig(url)
	if err != nil {
		return nil, err
	}
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err = migrateStore(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

func storePoolConfig(raw string) (*pgxpool.Config, error) {
	// Parse through pgx so URL and keyword connection strings both retain
	// their explicit pool settings. pgxpool removes these runtime parameters
	// while parsing, making explicit settings indistinguishable afterward.
	connection, err := pgx.ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	config, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	if _, explicit := connection.RuntimeParams["pool_max_conns"]; !explicit {
		// pgx defaults to NumCPU(), which reports host CPUs even when a
		// container has a small quota. Each API/worker needs a bounded pool;
		// preserve larger operator-requested minimums and explicit maxima.
		config.MaxConns = max(4, config.MinConns, config.MinIdleConns)
	}
	if _, explicit := connection.RuntimeParams["pool_max_conn_idle_time"]; !explicit {
		// Release spare connections left by brief concurrent SQL operations.
		// Pool health checks only retire idle resources; acquired connections
		// and the worker's separate notification connection remain unaffected.
		config.MaxConnIdleTime = 5 * time.Second
	}
	if _, explicit := connection.RuntimeParams["pool_health_check_period"]; !explicit {
		config.HealthCheckPeriod = time.Second
	}
	return config, nil
}

func newID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b)
}
