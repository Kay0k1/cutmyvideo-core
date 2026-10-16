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

`005_20261011_source_retention_v1.sql` adds derived source pin counts, membership
triggers/indexes and durable bounded audit progress. Its consistent one-time
backfill runs under the storage fence and writer-excluding table locks within the
existing startup cap. Positions 1–4 remain unchanged. The new shape is prepared
from the declared SQL; isolated PostgreSQL catalog validation is required before
this candidate can be released.

Positions 1–4 `*_schema.json` files capture catalog metadata observed on isolated
PostgreSQL 17. Position 5 is a declared, unobserved candidate shape pending its
actual PostgreSQL gate. These describe column types/defaults/nullability,
constraints, required indexes, owned trigger targets/column lists/conditions and
function bodies/attributes. Namespace names in owned trigger targets are
normalized; application data is not included. Startup uses these small manifests
to reject incompatible managed schema before changing it.

See the [migration policy](../../../docs/migrations.md).

`006_20261012_source_media_cleanup_v1.sql` adds a false-default media tombstone
flag, the durable post-success cleanup queue and renewable HTTP read leases.
It preserves historical values and accounting and is serialized by the existing
storage fence. `006_schema.json` captures the actual isolated PostgreSQL 17.11
catalog. See migration 6 rollout/compatible-recovery requirements in the policy.

`007_20261014_metadata_resolution_leases_v1.sql` adds expiring, token-fenced
platform metadata resolution claims. No SQL connection remains occupied during
upstream extraction; source deletion cascades its optional claim. Positions 1–6
remain immutable. `007_schema.json` records the expected catalog, checked against
the actual isolated PostgreSQL catalog by migration integration tests.

`008_20261016_agent_projects_v1.sql` adds owner-scoped source preparation
receipts, verified source fingerprints, bounded single-source projects and
single-recipient handoff grants. Retry intents retain separate per-clip job
identities before admission. Existing source/job/artifact rows and migrations
1–7 remain unchanged. `008_schema.json` captures the actual isolated PostgreSQL
17 catalog, including all new tables, indexes and constraints; schema 7 upgrade
and unchanged-current-startup tests verify the append-only transition.
