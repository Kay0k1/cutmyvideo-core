# Immutable schema inputs

`001_20261005_storage_queue_v3.sql` is the concatenation of `schema`,
`platformMetadataCacheSchema` and `storageSchema` from tag v0.2.1, commit
`f2be62c42a6a0d3d7a017b40bd683596bc578b95`. That marker's SQL is unchanged since
its introduction in commit `49d84f7d53350a3eeb0e3d68a725bb6b7d634c32`.

`002_20261010_ordered_migrations_v1.sql` introduces the ordered/checksummed
migration history. Append future migrations rather than editing these files.

`003_20261010_storage_maintenance_v1.sql` persists scan progress, workspace
cleanup and deletion retries. `004_20261010_cache_retention_v1.sql` replaces the
cache expiry index with an expiry/source ordering for bounded cleanup; it does
not rewrite application rows or accounting state.

The corresponding `*_schema.json` files capture catalog metadata after each
position on isolated PostgreSQL 17: column types/defaults/nullability,
constraints, required index definitions, accounting triggers and accounting
function body/attributes. Namespace names in owned trigger targets are
normalized; application data is not included. Startup uses these small manifests
to reject incompatible managed schema before changing it.

See the [migration policy](../../../docs/migrations.md).
