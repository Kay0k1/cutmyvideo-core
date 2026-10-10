# Self-hosting and operations

## Install

Use the [README quick start](../README.en.md#self-hosting) and
[Compose example](../deploy/compose.example.yml) for a local trial. Docker includes
the binary, FFmpeg/ffprobe, checksum-pinned yt-dlp and Node runtime. Native CLI
archives require external media tools. API and worker share PostgreSQL and media;
keep both persistent.

The example binds API to loopback and leaves PostgreSQL unexposed. Public hosting
needs a HTTPS reverse proxy, exact `PUBLIC_ORIGIN`, `COOKIE_SECURE=true` and
proxy timeouts/body limits aligned with `UPLOAD_TIMEOUT`/`MAX_SOURCE_BYTES` and
the 90-second preview deadline. Large uploads stream to disk.

Keep private configuration outside Git with operator-only permissions.
`TRUST_PROXY=true` requires an API inaccessible directly and a proxy that
normalizes forwarded-IP headers. One API is the current public rate-limit
topology. Start with one worker slot and measured CPU/RAM/PID/disk limits.
Use a dedicated writable media volume, drop capabilities, restrict private/metadata
egress, and keep unrelated host files/secrets out of media containers. See the
[security model](security-model.md). The product UI is maintained privately; all
instructions below work with this repository alone.

## HTTPS with Caddy

This example exposes the API on your domain with Caddy running **directly on the
same host**. The API stays bound to `127.0.0.1:8080`; PostgreSQL stays inside the
Compose network. It does not provide a browser UI or require the product website.

Point a real domain's A/AAAA records to the host, open TCP ports 80/443 to Caddy,
and install Caddy using its [official installation instructions](https://caddyserver.com/docs/install).
Caddy obtains/renews certificates and redirects HTTP to HTTPS for the configured
domain. See [automatic HTTPS requirements](https://caddyserver.com/docs/automatic-https).

Keep the existing private `.env` and add the domain (replace the example):

```sh
printf '\nCUTMY_DOMAIN=video.example.org\n' >> .env
docker compose --env-file .env \
  -f deploy/compose.example.yml -f deploy/compose.https.example.yml up --build -d
```

The [HTTPS overlay](../deploy/compose.https.example.yml) sets
`PUBLIC_ORIGIN=https://video.example.org`, `COOKIE_SECURE=true` and
`TRUST_PROXY=true`. It preserves the base loopback-only API port and the remaining
limits. Use one domain without a scheme, path or port. Keep this exact origin
when integrating browser clients; cross-origin mutations are rejected.

Replace `video.example.org` in [Caddyfile.example](../deploy/Caddyfile.example)
with the same domain. On a host using Caddy's packaged systemd service, install
the reviewed configuration, validate it and reload/start Caddy:

```sh
sudo install -m 0644 deploy/Caddyfile.example /etc/caddy/Caddyfile
# Edit /etc/caddy/Caddyfile to use your domain before validation/start.
sudo caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
sudo systemctl enable --now caddy
sudo systemctl reload caddy
curl --fail https://video.example.org/readyz
```

The example uses `reverse_proxy 127.0.0.1:8080` and overwrites
`X-Forwarded-For` with Caddy's direct peer address; client-supplied forwarding
chains are not accepted. Host-local administrative processes remain trusted.
Do not publish port 8080 on a public interface with `TRUST_PROXY=true`. If a CDN
or another proxy is in front, configure its trusted-IP policy explicitly before
using that topology. Running Caddy inside a container would require a different
upstream/network arrangement: container loopback is not the host API.

Use both Compose files for subsequent starts/upgrades/backup commands in this
HTTPS installation, preserving the same project name and persistent volumes.
Keep Caddy's certificate storage persistent and its admin API local. The example
keeps Caddy's normal streaming proxy behavior and does not buffer whole uploads.
Application time/byte limits still apply. Syntax/configuration validation does
not verify your DNS, firewall or certificate issuance; check readiness through
the real HTTPS origin after installation.

The standalone Compose example does not install an outbound firewall. For a
public installation, apply a host/container egress policy allowing the service's
own PostgreSQL, public DNS and public HTTPS while denying private/loopback/link-
local/cloud-metadata destinations and other outbound ports. Keep this separate
from Caddy's inbound HTTPS rules and test it after Docker/host restarts.

## Health and capacity

```sh
docker compose --env-file .env -f deploy/compose.example.yml ps
curl --fail http://localhost:8080/healthz
curl --fail http://localhost:8080/readyz
docker compose --env-file .env -f deploy/compose.example.yml exec -T worker cutmy worker-healthcheck
```

Readiness checks PostgreSQL; worker health independently requires successful
queue/lease access within 30 seconds. `cutmy healthcheck` uses `LISTEN_ADDR`,
including custom ports; wildcard addresses are probed through local loopback.
Keep the worker marker container-local. Monitor
health failures, restarts, queue/storage wait, disk headroom and export errors.
Logs include stage timings and sanitized errors; preserve privacy when sharing.

Application storage admission is not a disk quota. Leave space for database,
backups, OS and logs. Large/unknown video inputs occupy a worker's whole pool;
concurrency does not promise throughput. Measure representative load before
raising it.

## Output publication guarantees

Current-source local exports, uploaded/direct sources, thumbnails, window
previews and server artifacts synchronize completed file data and the required
directory entries before reporting success or committing their database
metadata. Publication never replaces an existing output. Local export checks
hard-link and synchronization support in the destination filesystem before
encoding; use a filesystem that provides these operations reliably. The
containing directory hierarchy of a local export must already be persistent.
The service creates and synchronizes each missing directory in its owned media
hierarchy. It synchronizes known parents, not unrelated ancestors created by
the caller immediately before an export.

If a final name was created but directory synchronization fails, local export
returns `publication_uncertain` and retains that name. Inspect the destination
before retrying; an existing result is never overwritten or unlinked to guess
away an uncertain outcome.

A database connection failure during COMMIT can leave its outcome unknown.
Sources, previews and workers retain possibly committed files and recover from authoritative database
state instead of deleting those results. API HTTP 503 similarly does not prove
that a mutation was not applied; reuse an export's `Idempotency-Key` when replaying
its submission. Do not turn transient diagnostics into automatic retries of
arbitrary mutations.

These guarantees depend on the filesystem and storage honoring synchronization;
they do not replace backups or protect against failed hardware. The new
synchronization contract applies to newly published files; existing files are
not retroactively verified or synchronized. Unsupported filesystems fail rather
than silently acknowledging weaker publication. Native CI exercises the same
barriers on Linux, macOS and Windows; it cannot simulate a physical power loss.

## Upgrade and recover

Pin a release/image revision. Read [CHANGELOG](../CHANGELOG.md), record the old
revision/image and back up database/media before upgrading. Schema migrations run
when API/worker/maintenance opens the store. Current source uses immutable,
ordered migrations and verifies known schema shape, version markers and SQL
checksums before applying changes. Unknown/future versions or inconsistent
schemas fail startup with the stable CLI code `schema_incompatible`; successful
current-schema restarts perform no DDL. Historical releases lack this guard and
must not be started against an upgraded database. See [migration rules](migrations.md)
and the required [upgrade/recovery acceptance gate](backup-recovery.md).
Test upgrades against a restored copy first.

For a versioned Docker build, supply `VERSION`, `VCS_REF` and `BUILD_TIME` build
arguments from the reviewed tag, exact commit and commit timestamp. `cutmy version`
then identifies the deployed binary. Unspecified values remain `dev`/`unknown`
rather than advertising an official release. The native CLI archive builder
sets these values automatically from its clean tagged checkout.

From the example, after checking out the intended release:

```sh
docker compose --env-file .env -f deploy/compose.example.yml stop api worker
docker compose --env-file .env -f deploy/compose.example.yml build api
docker compose --env-file .env -f deploy/compose.example.yml up -d api worker
curl --fail http://localhost:8080/readyz
docker compose --env-file .env -f deploy/compose.example.yml exec -T worker cutmy worker-healthcheck
```

API shutdown drains in-flight requests for up to ten seconds, then forcibly closes remaining connections before database cleanup. Preserve PostgreSQL/media volumes. Interrupted jobs recover through leases within
the attempt limit; completed items remain retained. Job deadlines and user
cancellation stay terminal. Roll back to the old image only after verifying schema
compatibility; otherwise restore the coordinated backup into a fresh installation.
Keep failed-instance data for diagnosis. Never use `docker compose down -v` on a
retained installation: it deletes its database and media volumes.

## Coordinated backup

PostgreSQL stores ownership, queue and file accounting; media volumes store actual
files. Back up both while API/worker writes are stopped for a simple consistent
snapshot. Record release/image versions and store configuration privately.
Restrict/encrypt backups containing user media. Run from the repository root:

```sh
set -euo pipefail
umask 077
backup_dir="backups/$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$backup_dir"
docker compose --env-file .env -f deploy/compose.example.yml stop api worker
docker compose --env-file .env -f deploy/compose.example.yml exec -T postgres \
  pg_dump -U cutmy -d cutmy -Fc > "$backup_dir/database.dump"
docker compose --env-file .env -f deploy/compose.example.yml run --rm -T --no-deps \
  --entrypoint tar api -C /data -czf - . > "$backup_dir/media.tar.gz"
docker compose --env-file .env -f deploy/compose.example.yml up -d api worker
```

Verify both commands succeed before resuming or treating the snapshot as complete.
Move backups to protected separate storage; this is a downtime-based example,
not an automated backup system.

Restore into a **fresh Compose project/volumes**, using matching configuration and
the backed-up release, with the old service stopped or a separate port:

```sh
set -euo pipefail
export COMPOSE_PROJECT_NAME=cutmy-restore
docker compose --env-file .env -f deploy/compose.example.yml up -d --wait postgres
docker compose --env-file .env -f deploy/compose.example.yml exec -T postgres \
  pg_restore --exit-on-error --no-owner --no-privileges -U cutmy -d cutmy < "$backup_dir/database.dump"
docker compose --env-file .env -f deploy/compose.example.yml run --rm -T --no-deps \
  --entrypoint tar api -C /data -xzf - < "$backup_dir/media.tar.gz"
docker compose --env-file .env -f deploy/compose.example.yml up -d api worker
```

Verify API/worker health, source listing and a synthetic export/download before
accepting traffic. Practice restoration regularly. High-availability backups
need a coordinated snapshot strategy beyond this simple example.

## Retention and storage pressure

Worker maintenance deletes expired unpinned media and reconciles abandoned files.
Run a bounded cycle independently while the worker is stopped:

```sh
docker compose --env-file .env -f deploy/compose.example.yml run --rm -T --no-deps api maintenance
```

Each cycle has a ten-second budget; backlogs may need multiple cycles. Active jobs
pin inputs/results. Retention, file reconciliation and workspace cleanup receive
separate budgets, so an error or long scan in one stage does not consume every
stage's time. Completed directory batches and workspace scheduling positions
are persisted in PostgreSQL and survive API/worker restarts.

Initial accounting finishes before new source/preview/worker disk admission.
Large restored trees may return `503 storage_initializing` with `Retry-After: 2`;
the worker and retrying admissions advance the same stored progress. Directory
membership changes reset that directory's coverage, and completed roots are
checked again before admission opens. A call retains at most three directory
readers and advances each reader after its database batch commits. An unchanged
directory is read once during that call, with bounded names/stat/SQL batches.
Readers close on exit, cancellation, changed membership or uncertain commit;
workspace readers close before physical removal, including on Windows.
Across calls and restarts, resume verifies the saved prefix in fixed chunks.
That verification remains O(prefix); frequent interruptions, path switches or
constantly changing directories can still repeat work. Batches bound memory
and transactions, not total traversal time. Keep media
directories private and finish restoration before starting writers.

Expired metadata cache cleanup deletes at most 200 entries per statement in
expiry/source order and skips locked rows. It receives at most 250 milliseconds
and a small share of the remaining retention deadline. A cache-stage failure is
reported while successfully committed media deletions still proceed; cache-only
backlogs can use the worker's existing eight-batch retention limit.

Failed physical deletion remains charged. Its persisted retry delay increases
from 30 seconds to one hour, allowing other due files to proceed. The row is
released only after the file is absent and the parent directory has synchronized.
Filesystem, database or acknowledgement failures preserve the charge for retry;
an uncertain acknowledgement may already have committed and is reconciled from
the authoritative database state on the next attempt.
Prefer owner-safe API deletion to manually removing live media trees. Waiting
jobs have finite persisted deadlines and remain cancellable. See
[configuration](configuration.md) and
[API retention](api.md#durable-storage-admission-and-explicit-deletion).
