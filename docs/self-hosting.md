# Public self-hosting profile

This **prepared, unvalidated current-source profile** supplies one API and one
worker on a Linux amd64 host. Hosted container acceptance has not yet completed
all seven required groups with verified cleanup. The Python controls test
configuration and refusal paths, not an installation.
Use the [local trial](operations.md) for development. Native CLI archives remain
supported by their separate five-platform distribution checks.

The public profile consists of [Compose](../deploy/compose.public.yml), the
[lifecycle wrapper](../deploy/self-host.py), [role bootstrap](../deploy/public-bootstrap.sql),
[runtime grants](../deploy/public-grants.sql) and the explicit
[egress helper](../deploy/egress-policy.py). It uses this repository alone; the
product website is maintained privately.

## Host and storage boundary

Use a dedicated Linux amd64 host with a local rootful Docker engine, Compose v2,
Python 3.10 or newer, GNU tar, and Docker's iptables backend (`legacy` or
`nf_tables` compatibility). Native nftables, rootless/user-namespace Docker,
remote Docker contexts, IPv6-enabled project containers and foreign network
members are refused. The bridge netfilter setting must already be enabled;
the helper does not change host sysctls or install packages.

On the dedicated installation host, the administrator must load `br_netfilter`
and enable bridge IPv4 filtering before using the policy helper:

```sh
sudo modprobe br_netfilter
sudo sysctl -w net.bridge.bridge-nf-call-iptables=1
```

Persist those prerequisites using the host's module/sysctl configuration before
relying on later reboots. A missing module/sysctl causes refusal. These commands
are for the dedicated installation host, not a shared development/operator host.

Run the wrapper and policy helper as the same root operator. Keep the checkout,
Python interpreter and Docker CLI trusted: Docker access is host administration.
Choose two unused RFC1918 `/24` networks and an unused loopback port. The policy
owns only its project chains and four hooks on the two exact project bridges;
review its inert `plan` before any explicit `apply`. It never flushes host chains.
Existing chains with changed rules are refused instead of overwritten. A second
fresh-restore project may coexist: only exact generated hooks on distinct project
bridges are allowed ahead of this project's hooks. Broad, negated, wildcard or
shadowing dispatch rules remain refusal conditions. The complete conntrack
`RETURN` rule accepts both iptables serialization orders for the exact two-state
set `ESTABLISHED,RELATED`; added/duplicate states, negation, flags, other targets
and changed rule order still refuse. A refusal prints a fixed diagnostic code,
never a raw exception, command or private configuration.

The API and worker run as UID/GID 1000 with read-only roots, dropped capabilities,
no new privileges and separate private 256 MiB temporary filesystems. The shared
media volume is mounted at `/data`; the database has a separate persistent
volume and no published port. PostgreSQL runs as UID/GID 999. App, database,
temporary storage, logs, memory, CPU, process counts and graceful-stop intervals
are bounded in Compose. Only `/data` and the PostgreSQL volume retain user state.
PostgreSQL readiness checks its final TCP listener at `127.0.0.1:5432`; the
temporary Unix-socket server used during first initialization is not readiness.

Version, tool-inspection and media-tar helpers have separate random names and
project/profile/purpose/identity/source labels; they never use Compose ownership
labels. They run without a network, as UID/GID 1000, with read-only roots and
fixed CPU, memory, swap and process limits. Backup mounts only the owned media
volume read-only; restore mounts that same fresh project's volume writable.
After a helper exits, fails or times out, the wrapper discovers its exact name,
verifies the actual image, labels, command and mount, then removes and confirms
only that resource. Foreign or changed resources are left untouched and cause
refusal. Local subprocess timeouts and handled SIGINT/SIGTERM interruption kill
the wrapper's own child process group and enter the checked helper cleanup;
Docker daemon cleanup is a separate checked step. Dump/archive descriptors are
streamed through the same bounded runner. A refused backup/restore leaves apps
stopped and preserves private partial files for diagnosis.

The initial application limits are 1 GiB per source/fetch/output, 10 GiB shared
admission, 2 GiB per owner, one worker slot and two FFmpeg threads. API and worker
memory limits are 1/2 GiB; PostgreSQL is limited to 512 MiB. These are starting
limits, not demonstrated media throughput or a disk quota. Reserve additional
host space for database/WAL, images, logs, backups and updates. The profile does
not provision a filesystem quota. See [capacity evidence](performance.md) and
[server limits](configuration.md).

## Separate database roles

`cutmy_admin` is PostgreSQL's bootstrap/operator administrator; its password
never enters an API, worker or maintenance container. `cutmy_migrate` owns the
database and managed schema and runs the stopped-writer migration step.
`cutmy_runtime` runs the API, worker and routine maintenance: it has DML on
application tables, schema `USAGE`, database `CONNECT`, and `SELECT, MAINTAIN` on
the two migration ledgers. Startup verifies these ledgers using SHARE locks;
[PostgreSQL 17 requires MAINTAIN or a write privilege for that lock](https://www.postgresql.org/docs/17/sql-lock.html).
Runtime cannot modify the ledger, create schema objects, create roles, truncate
tables, create temporary objects, or use administrator server-file functions.

The current schema has no sequences. Grants are applied after every migration,
with writers stopped. There is no blanket function `EXECUTE` grant: the only
public function is the existing invoker storage-counter trigger, installed by
the schema owner. Future sequences/functions require a reviewed grant change.
These privilege claims still require the real acceptance checks below.

## Immutable image and private state

The wrapper requires a clean checkout at a full 40-character commit. It records
the complete immutable Git file inventory in `SOURCE.json`, materializes that
source outside Git, then builds the Linux amd64 image from those exact files.
The resulting image is selected by its local `sha256:` ID, not a mutable tag.
OCI labels and actual `cutmy version` must agree with the source commit, version,
commit-derived build time, Go toolchain and platform before a state is accepted.
Every lifecycle command verifies the saved source, private configuration and
actual image identity again.

For an untagged commit the version is an explicit candidate prerelease; pass
`--release vX.Y.Z` only when that immutable tag resolves to the supplied commit.
This is image/source identity verification, not an attestation or an image-byte
reproducibility claim. APT dependencies are not snapshot-pinned. `image.json`
records the actual FFmpeg, ffprobe, yt-dlp and Node versions for the built image.
The native CLI reproducibility and extracted-archive checks are separate.

Keep configuration, state and backups outside every checkout, in root-owned
directories mode 0700. Configuration files are mode 0600, plain `KEY=value`
without quotes or interpolation. The wrapper creates three distinct random
database passwords; edit only the domain, project name, unused subnet prefixes
and loopback port before building. Do not print credentials or use `docker
compose config` in shared logs; its output includes interpolated secrets.

The following commands describe the intended prepared lifecycle. Do not use
them on a retained or production installation until its acceptance run passes.
Replace the revision with the reviewed commit **containing this profile**:

```sh
sudo install -d -m 0700 /var/lib/cutmy-operator
profile=(sudo python3 deploy/self-host.py \
  --config /var/lib/cutmy-operator/public.env \
  --state /var/lib/cutmy-operator/image-state)
"${profile[@]}" init-config
# Edit public.env privately: domain, project, loopback port, two unused /24s.
"${profile[@]}" build --revision FULL_40_CHARACTER_COMMIT
"${profile[@]}" prepare
sudo python3 deploy/egress-policy.py \
  --config /var/lib/cutmy-operator/public.env plan
# Review the project-only rules before the explicitly privileged mutation.
sudo python3 deploy/egress-policy.py \
  --config /var/lib/cutmy-operator/public.env apply
"${profile[@]}" initialize
"${profile[@]}" up
"${profile[@]}" status
```

`prepare` creates stopped app containers and owned networks/volumes. Apply the
policy before `initialize`, which starts PostgreSQL, runs schema-owner migration
and grants runtime permissions. `up` requires the exact policy before starting
apps and verifies it again afterwards; failure stops both writers. A common
per-project lock serializes starts, migrations, backups, restores and policy
changes across old/new image-state directories.

## HTTPS and outbound policy

Every lifecycle Compose invocation includes the HTTPS overlay, so the same exact
`PUBLIC_ORIGIN=https://<domain>`, secure cookie and trusted proxy settings survive
starts, upgrades, maintenance and recovery. Caddy runs directly on the host and
connects to the loopback API. Review the [Caddy example](../deploy/Caddyfile.example)
for your domain **and configured loopback port**, validate it, retain certificate
storage and allow inbound TCP 80/443. The wrapper does not install or alter Caddy,
DNS or host inbound rules. Check readiness through the actual HTTPS origin.

API/worker public egress is IPv4 TCP 443 only, with conservative private,
link-local, loopback, metadata, multicast and reserved-range denial. Docker's
embedded DNS and the application's guarded loopback relay stay inside container
namespaces. On the internal database network the only new forwarded connection
permitted from API/worker/migration/maintenance is to their own PostgreSQL at
TCP 5432. Both project bridges deny new connections to host-local listeners,
including their gateway addresses. Established replies to the host proxy remain
allowed. This strict initial profile also denies the whole `192.0.0.0/24` range;
it may be more restrictive than the application guard for public assignments.

The helper refuses unsupported network/backend topology before mutation; it
cannot make a hostile host administrator, Docker daemon or replaced interpreter
safe. Recheck both denied and permitted traffic after firewall or Docker changes.
These claims must be verified by raw TCP probes, not only by application URL
validation.

API and worker deliberately use `restart: "no"`. After a host/Docker restart,
they remain stopped until the operator checks/reapplies the owned policy and
runs checked `up`. PostgreSQL may restart independently. This profile does not
promise automatic availability recovery; a policy-before-app system service is
separate future work. Never run raw Compose `up` or change restart policy to
bypass this ordering.

## Upgrade, backup and fresh restore

Before upgrade, stop writers and make a coordinated backup. Build the new
reviewed revision into a **new** private state using the same project/config,
run `initialize` to migrate and reapply grants, then checked `up`. Keep the old
state and backup. Reusing an old image against a new schema is not a rollback
strategy; follow [migration compatibility](migrations.md).

```sh
"${profile[@]}" backup --directory /var/lib/cutmy-operator/backup-unique
# Writers remain stopped. Resume explicitly only after checking the result.
"${profile[@]}" up
```

Backup stops both writers, refuses any other active project writer, dumps the
database with the matching container client and captures the complete media
volume with network-disabled, read-only mounts. It records source/image identity
and file bytes/SHA-256. Backup failure leaves writers stopped and partial files
for diagnosis. Backups contain private user data; protect them and keep verified
copies on separate storage. This is a downtime-based backup, not high
availability or continuous recovery.

Restore requires a different, **fresh** project with no existing named networks,
containers or media/database volumes. Use a new private configuration with three
fresh passwords, a different unused port and network prefixes. `adopt-image`
copies the verified immutable state and reuses the exact backed-up image ID:

```sh
restore=(sudo python3 deploy/self-host.py \
  --config /var/lib/cutmy-operator/restore.env \
  --state /var/lib/cutmy-operator/restore-state)
"${restore[@]}" init-config
# Edit restore.env privately; use a distinct project, port and networks.
"${restore[@]}" adopt-image --from-state /var/lib/cutmy-operator/image-state
"${restore[@]}" restore --directory /var/lib/cutmy-operator/backup-unique
sudo python3 deploy/egress-policy.py \
  --config /var/lib/cutmy-operator/restore.env plan
# Review, then explicitly apply the fresh project's policy.
sudo python3 deploy/egress-policy.py \
  --config /var/lib/cutmy-operator/restore.env apply
"${restore[@]}" initialize
"${restore[@]}" up
```

Restoration uses real `pg_restore`, validates the complete GNU media archive
before extraction, permits legitimate in-volume hardlinks, and refuses symlinks,
special files, escaped/duplicate paths and oversized records. It preserves the
canonical `/data` paths without rewriting SQL. It keeps apps stopped until the
fresh policy is applied; a separate `initialize` then migrates and reapplies
runtime grants before checked `up`. Verify retained outputs and HTTPS before
accepting traffic. Changing the data
path is a separate offline migration. No lifecycle command deletes volumes or
preexisting installations; never use `down -v` on retained state.

## Required acceptance before supported status

The prepared image-upgrade test pins the immutable baseline **A1** to
`0f93cd654a998be7740ba1d6fee4a44a6f7b3464` (tree
`58a431cf209a36b8acb0052ca77653b480814d48`) and builds **B** from the actual
CI checkout. A1 includes the TCP healthcheck and exact conntrack-order fix;
the earlier immutable baseline and its failed runs remain separate historical
evidence. No sealed old snapshot is patched to make an upgrade pass.

The application sources (`cmd`, `internal`, `pkg`, `go.mod`, `go.sum`, including
tracked tests and fixtures) must match the immutable reference
`06bb74bca90a79fbf399c01cf71f7f44af186431` in both images. This tests image/source
identity, retained data and a same-schema upgrade; it does not demonstrate a
schema-changing application upgrade. A1, B and the restored B each require the
actual final-listener TCP healthcheck and an independent SQL round trip.

A failed policy apply/remove retains a bounded, redacted filter-rule snapshot
before the expected-exit assertion, including after a command timeout. Capture
failure has a fixed unavailable reason and preserves the original policy error.
Neither failure diagnostics nor a partially completed run count as acceptance.

The disposable acceptance runner must own an explicit test project, subnets,
containers and output directory; it must refuse collision with preexisting
resources. Record source/tree/SOURCE digest, actual image ID/version/tool
versions, host/engine/backend identity and all retained logs. It must run the
same public wrapper/profile used above, rather than a less restricted test
Compose file, and perform these checks:

1. Clean-source image build and actual version match; dirty source, wrong
   commit, changed snapshot/config/image and remote/unsupported engine refuse.
2. Fresh non-root PostgreSQL initialization, schema-owner migration, API/worker
   readiness and a real upload/preview/MP4/MP3/copy/download/delete cycle through
   HTTPS. Independently decode outputs and verify owner isolation and accounting.
3. Runtime connection succeeds but ledger INSERT, DDL, CREATE ROLE, temporary
   object creation and server-file reads refuse. Migration remains separate;
   storage-counter trigger and routine maintenance work with the reduced grants.
4. Inspect actual UID, read-only roots, only declared mounts, private bounded
   temp, secrets separation, memory/CPU/PID/log limits and graceful stops.
5. Before/after policy installation, raw TCP from app/operator namespaces proves
   both gateway/host-listener/private/metadata/PG-wrong-port denial, exact own-PG
   access, permitted public HTTPS and isolation of unrelated project traffic.
   Missing/modified/reordered policy prevents startup. Duplicate apply is exact
   and removal touches only owned hooks/chains. No Docker daemon restart is
   required or authorized for this acceptance run.
6. Restore actual coordinated `pg_dump` plus all media into a fresh project using
   the same image; verify retained identities, download SHA/decode, counters,
   idempotency and fresh exports. Partial backup/wrong image/existing volumes/
   unsafe archive refuse without overwriting retained data.
7. Concurrent lifecycle operations from old/new state serialize on the same
   project lock. Every start after a stopped-container restart model requires
   policy verification and retains HTTPS/origin/cookie settings. Record that
   the real host/Docker boot ordering is outside this test's evidence.

Static controls are executable without Docker or firewall access:

```sh
python3 -m unittest discover -s deploy -p self_host_test.py -v
```

They require PyYAML (the repository's pinned contract-tooling environment
includes it). Passing these controls is not a substitute for the real acceptance
run, representative capacity measurement or practicing protected off-host restore.
