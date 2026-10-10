package app

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrSchemaIncompatible means startup refused an unrecognized or inconsistent
// application schema before making changes. Restoring/deploying a compatible
// database/binary is an operator decision; this is never permission to reset it.
var ErrSchemaIncompatible = errors.New("database schema is incompatible with this binary")

const storeStartupTimeout = 30 * time.Second
const schemaVersion = "20261010-ordered-migrations-v1"

// Migration SQL is immutable. Append a migration and update the expected shape;
// never edit a shipped file or reinterpret an existing marker/checksum.
// The first marker is the only version actually recorded by historical builds.
// Earlier unversioned schemas are recognized structurally, not given invented
// version identifiers. Historical binaries cannot retroactively gain this guard.
//
//go:embed migrations/*.sql migrations/*.json
var migrationFiles embed.FS

type schemaMigration struct {
	version, file, shape string
}

var schemaMigrations = []schemaMigration{
	{"20261005-storage-queue-v3", "migrations/001_20261005_storage_queue_v3.sql", "migrations/001_schema.json"},
	{schemaVersion, "migrations/002_20261010_ordered_migrations_v1.sql", "migrations/002_schema.json"},
}

func migrationSQL(m schemaMigration) string {
	value, err := migrationFiles.ReadFile(m.file)
	if err != nil {
		panic(err) // Embedded release inputs are checked by compilation/tests.
	}
	return string(value)
}

func migrationChecksum(m schemaMigration) string {
	digest := sha256.Sum256([]byte(migrationSQL(m)))
	return hex.EncodeToString(digest[:])
}

func incompatibleSchema(reason string) error {
	// Reasons describe schema structure, never DSNs, SQL values or user data.
	return fmt.Errorf("%w: %s", ErrSchemaIncompatible, reason)
}

func migrateStore(ctx context.Context, db *pgxpool.Pool) (err error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cutmy:migrations'))`); err != nil {
		return err
	}
	var namespace string
	if err = tx.QueryRow(ctx, "SELECT current_schema()").Scan(&namespace); err != nil {
		return err
	}
	// Inspect and migrate one namespace. A later search_path entry must not
	// silently supply another application's tables during an initial install.
	if _, err = tx.Exec(ctx, "SELECT set_config('search_path',$1,true)", pgx.Identifier{namespace}.Sanitize()); err != nil {
		return err
	}
	shape, err := readSchemaShape(ctx, tx)
	if err != nil {
		return err
	}
	var versions []string
	if _, exists := shape.Tables["app_schema_versions"]; exists {
		if err = checkTableShape("app_schema_versions", shape.Tables["app_schema_versions"], baselineSchemaShape().Tables["app_schema_versions"], false); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "LOCK TABLE app_schema_versions IN SHARE MODE"); err != nil {
			return err
		}
		rows, e := tx.Query(ctx, "SELECT version FROM app_schema_versions ORDER BY version")
		if e != nil {
			return e
		}
		versions, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
	}
	applied, err := knownMigrationPrefix(versions)
	if err != nil {
		return err
	}
	_, ledger := shape.Tables["app_schema_migrations"]
	if ledger != (applied >= 2) {
		return incompatibleSchema("ordered migration ledger and version markers disagree")
	}
	if applied == 0 {
		if err = checkUnversionedSchema(shape); err != nil {
			return err
		}
	} else if err = checkSchemaShape(shape, expectedSchemaShape(schemaMigrations[applied-1].shape), false); err != nil {
		return err
	}
	if ledger {
		if err = checkMigrationLedger(ctx, tx, applied); err != nil {
			return err
		}
	}
	if applied == len(schemaMigrations) {
		return tx.Commit(ctx) // Current startup performs no DDL or metadata writes.
	}
	if _, exists := shape.Tables["app_schema_versions"]; !exists {
		if _, err = tx.Exec(ctx, "CREATE TABLE app_schema_versions(version text PRIMARY KEY)"); err != nil {
			return err
		}
	}
	for i := applied; i < len(schemaMigrations); i++ {
		migration := schemaMigrations[i]
		if _, err = tx.Exec(ctx, migrationSQL(migration)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO app_schema_versions(version) VALUES($1)", migration.version); err != nil {
			return err
		}
		// Migration 2 introduces the ledger and records the verified baseline.
		// Later migrations append exactly one ordinal and immutable checksum.
		if i >= 1 {
			start := i
			if i == 1 {
				start = 0
			}
			for position := start; position <= i; position++ {
				m := schemaMigrations[position]
				if _, err = tx.Exec(ctx, "INSERT INTO app_schema_migrations(position,version,checksum) VALUES($1,$2,$3)", position+1, m.version, migrationChecksum(m)); err != nil {
					return err
				}
			}
		}
	}
	shape, err = readSchemaShape(ctx, tx)
	if err != nil {
		return err
	}
	if err = checkSchemaShape(shape, latestSchemaShape(), false); err != nil {
		return err
	}
	if err = checkMigrationLedger(ctx, tx, len(schemaMigrations)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func knownMigrationPrefix(versions []string) (int, error) {
	known := make(map[string]int, len(schemaMigrations))
	for i, m := range schemaMigrations {
		known[m.version] = i
	}
	seen := make(map[int]bool, len(versions))
	for _, version := range versions {
		i, ok := known[version]
		if !ok {
			return 0, incompatibleSchema("unknown or newer version marker")
		}
		seen[i] = true
	}
	for i := range versions {
		if !seen[i] {
			return 0, incompatibleSchema("migration version history is not an ordered prefix")
		}
	}
	return len(versions), nil
}

func checkMigrationLedger(ctx context.Context, tx pgx.Tx, applied int) error {
	if _, err := tx.Exec(ctx, "LOCK TABLE app_schema_migrations IN SHARE MODE"); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT position,version,checksum FROM app_schema_migrations ORDER BY position")
	if err != nil {
		return err
	}
	defer rows.Close()
	position := 0
	for rows.Next() {
		var recorded int
		var version, checksum string
		if err = rows.Scan(&recorded, &version, &checksum); err != nil {
			return err
		}
		if position >= applied || recorded != position+1 || version != schemaMigrations[position].version || checksum != migrationChecksum(schemaMigrations[position]) {
			return incompatibleSchema("migration order, version or checksum does not match this binary")
		}
		position++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if position != applied {
		return incompatibleSchema("migration ledger is incomplete")
	}
	return nil
}

type schemaColumn struct {
	Type                         string
	NotNull                      bool
	Default, Identity, Generated string
}

type schemaIndex struct {
	Unique, Valid, Ready bool
	Columns              []string
	Predicate            string
}

type schemaTrigger struct {
	Function, Enabled, OldTable, NewTable string
	Type                                  int
}

type schemaTable struct {
	Kind        string
	Columns     map[string]schemaColumn
	Constraints []string
	Indexes     map[string]schemaIndex
	Triggers    map[string]schemaTrigger
}

type schemaShape struct {
	Tables    map[string]schemaTable
	Functions map[string]string
}

func expectedSchemaShape(file string) schemaShape {
	data, err := migrationFiles.ReadFile(file)
	if err != nil {
		panic(err)
	}
	var shape schemaShape
	if err = json.Unmarshal(data, &shape); err != nil {
		panic(err)
	}
	return shape
}

func baselineSchemaShape() schemaShape { return expectedSchemaShape(schemaMigrations[0].shape) }
func latestSchemaShape() schemaShape {
	return expectedSchemaShape(schemaMigrations[len(schemaMigrations)-1].shape)
}

func checkSchemaShape(actual, expected schemaShape, legacy bool) error {
	for name, table := range expected.Tables {
		found, ok := actual.Tables[name]
		if !ok {
			return incompatibleSchema("required application table is missing")
		}
		if err := checkTableShape(name, found, table, legacy); err != nil {
			return err
		}
	}
	for name, body := range expected.Functions {
		if actual.Functions[name] != body {
			return incompatibleSchema("required application function is missing or changed")
		}
	}
	return nil
}

func checkTableShape(name string, actual, expected schemaTable, legacy bool) error {
	if actual.Kind != expected.Kind {
		return incompatibleSchema("application relation is not a supported table")
	}
	optional := func(column string) bool {
		return legacy && name == "sources" && slices.Contains([]string{"provider_id", "provider", "thumbnail_path"}, column)
	}
	for column, shape := range expected.Columns {
		if legacy && (name == "jobs" && column == "storage_wait_until" || name == "artifacts" && column == "expires_at") {
			if _, exists := actual.Columns[column]; exists {
				return incompatibleSchema("unversioned schema contains post-versioning columns")
			}
			continue
		}
		found, ok := actual.Columns[column]
		if !ok && optional(column) {
			continue
		}
		if !ok || found != shape {
			return incompatibleSchema("application column type, nullability or default is incompatible")
		}
	}
	for column := range actual.Columns {
		if _, known := expected.Columns[column]; !known {
			return incompatibleSchema("unrecognized application column")
		}
	}
	if !reflect.DeepEqual(actual.Constraints, expected.Constraints) || !reflect.DeepEqual(actual.Triggers, expected.Triggers) {
		return incompatibleSchema("application constraints or triggers are incompatible")
	}
	for index, shape := range expected.Indexes {
		found, ok := actual.Indexes[index]
		if !ok && legacy && (index == "jobs_source_idx" || index == "artifacts_job_idx") {
			continue
		}
		if !ok || !reflect.DeepEqual(found, shape) {
			return incompatibleSchema("required application index is missing or incompatible")
		}
	}
	return nil
}

func checkUnversionedSchema(shape schemaShape) error {
	if len(shape.Functions) != 0 {
		return incompatibleSchema("unversioned schema contains versioned storage functions")
	}
	base := baselineSchemaShape()
	coreTables := []string{"sources", "jobs", "artifacts"}
	legacy := false
	for name := range shape.Tables {
		if name == "app_schema_versions" {
			continue
		}
		if !slices.Contains(coreTables, name) && name != "source_metadata_cache" {
			return incompatibleSchema("unversioned schema contains unrecognized versioned storage tables")
		}
		legacy = true
	}
	if !legacy {
		return nil // No managed objects: a fresh installation.
	}
	for _, name := range coreTables {
		actual, ok := shape.Tables[name]
		if !ok {
			return incompatibleSchema("unversioned application schema is incomplete")
		}
		if err := checkTableShape(name, actual, base.Tables[name], true); err != nil {
			return err
		}
	}
	source := shape.Tables["sources"]
	_, id := source.Columns["provider_id"]
	_, provider := source.Columns["provider"]
	_, thumbnail := source.Columns["thumbnail_path"]
	if provider != thumbnail || provider && !id {
		return incompatibleSchema("unversioned provider column history is incomplete")
	}
	if cache, exists := shape.Tables["source_metadata_cache"]; exists {
		if !provider {
			return incompatibleSchema("unversioned metadata cache predates required provider columns")
		}
		if err := checkTableShape("source_metadata_cache", cache, base.Tables["source_metadata_cache"], false); err != nil {
			return err
		}
	}
	return nil
}
