# Prepared source-retention 004→005 public transition

This is a source-only acceptance extension. It defines the evidence required for
the explicit `source-retention-004-to-005` mode; it does not record a successful
upgrade or authorize resource creation. No disposable-host acceptance,
PostgreSQL catalog validation, media exercise, performance measurement, release,
merge, deployment or production action follows from this document or the pure
Python controls.

The frozen preparation plan is bound to source-freeze digest
`499cbbaf12714ad68201b5549f4e5a3c2ec8304f1bdfb6461790585ebc37038a`, preparation
base `63873e935626d51087d0a26b84f1de9a7ed86a28`, migration-005 SQL digest
`b460686dc29fa1a156cbf74ca73da8afd2e4394cc7f2a3d6465f72e047b7c0e0`, and declared
schema-005 manifest digest
`da0357d73e4697ddf656211cda0a6d9c6f0b0bda905673a0041dcd641664d4ae`.
The declared manifest is prepared source, not observed PostgreSQL catalog
output. A future real catalog capture must confirm it or require a reviewed
correction before release. The original reviewed source-retention application
pin was commit `670089aa7cc99fe65e2a0336ff05d7c6b3047bd0`, tree
`dc83b8f861b2b6b823b8476e3c9ad273f7aa6bba`; those identities and their recorded
proofs remain historical. The current recovery-contract application pin is
commit `76e9cdb4072e8c6a2ba293ee69635601785ba62c`, tree
`ee1c6647390b8f91354fe0c371d19d81d3bf0549`. It was reviewed and committed before
this wrapper-only update. The final reviewed harness checkout/merge identity
must be separately frozen before any future hosted run. A branch name, dirty
working tree or preparation base cannot stand in for either reviewed identity.

The application pin remains provisional while its candidate-specific gates are
unresolved. The earlier reviewed commit
`46cf2f2c8205e786f01a7a05947d1d3ae409a479` failed candidate-specific Go and archive
gates and supplies no successful transition evidence. Historical 670 and
combined-candidate proof scopes cannot supply the current recovery application's
source, native archive, recovery or public-transition gates. Each remains
separate mandatory evidence for the current candidate. This source-only packet
does not establish actual transition acceptance. A source correction requires a new reviewed commit/tree and full
application-map pin; never retarget these identities automatically.

The preparation receipt also pins the original plan digest
`35021688ee2e420ce7135efa2e45a3042f07fdc4533cff5a7bb120de08f5a2f5`, original root
review digest `703fba88d8acaea6a1befa5ffcd9ea52cf175b10a77bc292255be8fa805c99d3`,
the corrected root670 review digest
`56759ddc1dd8289a4a834df466f6e7e85e23f6f68a29be0414a5ce5e744f8260`, and its
public-driver binding supplement digest
`5c6cb808cf900532a012d304c0f168e994a2afd0bfb614c5a961529e232e676f`.
These five historical preparation identities remain unchanged. The current
receipt additionally pins the recovery application source freeze
`fc86f8ebd00761c01fb47b577dade04b477ebe04ffe477efd0d20621e80f25e5`, root source
review `b0ca5a8023b85ba7afbf08e2519d146433f4a9933b9cc4913a108f032ff33de3`, and
root committed-source review
`1023ad3bcd365253782d863b6d059541e86b796ac7d4c48e2076d6cf428a8f6c`. These are
reviewed preparation identities, not results from an actual run.

## Separate modes and receipts

The existing [public profile](self-hosting.md) keeps its same-schema acceptance
contract: exact A1/current application-source equality, all 35 original checks,
all seven original groups, and its original strict receipt. Omitting the CLI
mode continues to use that contract. A schema-changing candidate fails its equality
assertion; the new transition must not weaken, bypass, replace or relabel it.

Transition evidence has receipt schema version 2 and exactly 53 named checks
in ten groups. It retains 34 original check names, replaces the original
application-equality check with `approved_application_transition`, and adds 18
named checks in three new groups.
Its groups and names are the pinned inventory in
[`source-retention-transition.py`](../scripts/source-retention-transition.py).
The receipt must enumerate every required check once, reject unknown or missing
groups/checks, and reject failed, skipped, running or partial records. Its 53
checks are a separate required total; they are not the old 35 plus a receipt that
asserts seven-group success. Passing the old receipt does not establish a schema
upgrade. Pure failure controls exercise the transition verifier's refusal
contract with synthetic files and records; their success does not establish any
of the 53 actual observations.

Transition handling requires the explicit mode
`source-retention-004-to-005`. Missing or unknown transition mode is refused.
Only the exact schema/version sequence 004→005 is allowed: the old state has
exactly migrations 1–4 with their original markers and SQL checksums, and the
current state has exactly migrations 1–5 with those same first four entries and
the reviewed fifth entry. Non-004 old state, non-005 current state, an unreviewed
commit/tree, changed source identity or missing transition evidence is refused.
No mode infers permission to execute an upgrade from a candidate's version.

The public workflow explicitly selects `source-retention-004-to-005` for
automatic pull-request and push runs. Its required `workflow_dispatch` input
`acceptance_mode` defaults to the same transition; `same-schema` remains an
explicit historical opt-in and retains the original equality requirement. Every
transition run installs the pinned Go toolchain for its PostgreSQL test oracles.
The flow and report-verification steps use the same event/input selector; neither
derives a mode from the candidate's source identity. Missing or unknown dispatch
values are refused by the CLI. The driver requires
`--mode source-retention-004-to-005` for a transition. Verification must repeat
the same explicit mode; omitting it keeps the original same-schema verifier and
refuses a transition receipt. Read-only verification has this form:

```sh
python3 -B scripts/self-hosting-acceptance.py \
  --mode source-retention-004-to-005 \
  --verify-report /absolute/evidence-directory/acceptance-report.json
```

| Position | Exact marker | Required state |
| --- | --- | --- |
| 1 | `20261005-storage-queue-v3` | Old and current |
| 2 | `20261010-ordered-migrations-v1` | Old and current |
| 3 | `20261010-storage-maintenance-v1` | Old and current |
| 4 | `20261010-cache-retention-v1` | Old and current; old terminal version |
| 5 | `20261011-source-retention-v1` | Current only; current terminal version |

These rows accompany exact SQL checksum and catalog manifest comparisons. Merely
adding a fifth marker to an unobserved or mismatched catalog is insufficient.

## Immutable baseline and execution context

The old A1 source stays sealed at commit
`0f93cd654a998be7740ba1d6fee4a44a6f7b3464`, tree
`58a431cf209a36b8acb0052ca77653b480814d48`. Its unchanged application reference
is `06bb74bca90a79fbf399c01cf71f7f44af186431`, with original application-map digest
`a1aa7b968c06eaeb075d2cd73f8afbf309a1bc12121d1a3e598ba2561e785f90`.
Never patch, regenerate, rebuild from altered source, or add migration005 to the
old sealed inventory. Build A1 from its own immutable source and build the new
candidate from the exact independently reviewed inventory.

The current immutable application inventory has exactly 169 files and digest
`1ac2c591d2e20c628ddfe2cf01f9e8a6af7bcde92e154d521794e97094d86534`. It includes
`git_blob` identities and was derived from the committed recovery application,
not a working tree. The historical root670 inventory has 168 files and digest
`f22a2be71284389a47631e11474dcee272c5278d7a5a0cd85c273dace7e22ccd`. The
superseded 46cf full map had digest
`08d7d213fb8f33ae057af7a5f4d7b5b187ed25fca86bee0bdcead15d0de38d31`; its earlier
preparation packet's projection omitted `git_blob` and had digest
`0988f39c1fcba6189acf57f2167aa72f79fc426c4af9e16981956b35676817db`.
These are different serialized inventories, not interchangeable hashes. The
transition verifies the current 169-file full map, refuses substitution of the
historical 168-file map, and retains the original A1 full-map digest independently;
it never claims old/new application equality. The executing wrapper must descend
from the exact reviewed recovery application commit and have byte-identical
application files; later unrelated application changes require another reviewed
commit/tree/map pin. No environment variable or branch selector changes this pin.

Preserve the reviewed conntrack policy helper at SHA-256
`ad653eb4eff9fb8f40b1d733bcab7b6394b98f03a769bab18b126234d861aef0`.
The transition does not relax configuration, HTTPS, route, raw TCP egress,
private administrator credentials, separate roles, image ownership, container
constraints, lifecycle locks, backup/restore or cleanup oracles. It retains the
final PostgreSQL TCP healthcheck and independent SQL round trip. Runtime grants
must stay reduced; function `EXECUTE` denial and invoker security are tested as
actual privileges, not asserted from grant files.

Bind both clean Git inventories, `SOURCE.json` and image metadata, actual image
IDs, source commit/tree, runtime revision/version, tool versions, and each real
database-dump/media-archive hash. Candidate version strings alone are
insufficient. Bind the actual hosted checkout and provider merge/head identity;
for pull requests the real merge parents, head commit and equal head/merge tree
must agree with the reviewed execution context. Use the actual supported
provider, repository, event, run ID, attempt and checkout SHA. Different attempts
or runs are different evidence scopes even when their visible branch or job name
matches.

Freeze the driver, collector, verifier and context bytes before downloading or
accepting fresh results. The verifier must inspect the actual bounded raw
evidence files and compare their bytes, sizes and hashes with the receipt and
frozen context. In this driver, read-only host/daemon probes and owned private
source clones and project configuration setup precede that context capture.
The exact receipt field `frozen_before_image_builds_and_database_media_changes`
means it occurs before image builds, PostgreSQL initialization, API/worker media
work or schema migration; it does not claim to precede those probes or private
setup. Old schema004 role/ownership/refusal captures, catalogs, the
migration observer, initial backfill pins, runtime DML, TTL observations, Go
records and recovery observations bind the same provider, checkout, candidate
and frozen-context digest. Catalogs also identify their exact phase and actual
PostgreSQL container; paired cancellation/A1-refusal captures require the same
container and unchanged catalog/history. Initial backfill records require valid
exact job/preview counts and revision zero, including the historical preview pin.
Evidence paths must stay within the evidence directory and refer
to regular files; path traversal, symlinks, replaced files and missing files are
refusals. A summary, log excerpt, boolean pass flag, rewritten metadata, old
artifact, fixture or other candidate's successful run cannot supply missing
observations. Preserve bounded, redacted failure snapshots; exclude credentials,
cookies, private keys and private backup contents from public evidence.

## Ordered disposable-host observations

These are required future phases, not instructions to start resources during
source-only preparation. Execute only under a separately authorized disposable
Linux-host run, using the public wrapper and exact frozen driver/context.

1. Build exact A1 and the reviewed candidate from their immutable inventories.
   Initialize A1 and verify the final TCP listener and independent SQL round
   trip. Observe the real schema004 catalog, exactly four original migration
   markers/checksums, the administrator/migration/runtime separation, private
   administrator credentials and runtime privilege denials.
2. Publish owned sources and process retained results/idempotent jobs through
   actual API/worker paths. Cover queued, running, waiting and terminal jobs;
   exact expired and foreign-owner preview pins; cached previews; metadata-only
   expired sources; and a healthy eligible tail behind retained pinned rows.
   Capture original source values, ownership, jobs, artifacts, byte counters,
   physical files and file digests. Direct-SQL drift fixtures under recorded
   roles supplement this publication/processing evidence.
3. Stop every A1 API, worker and maintenance writer through the public wrapper.
   Verify stopped processes and the existing bounded egress/filter state. Take
   and hash a coordinated schema004 PostgreSQL/media backup. Retain original
   source values separately from the future derived columns, together with
   original jobs, artifacts, ledgers and accounting. Do not use source `xmin`
   equality as a migration-preservation oracle: backfill may rewrite it.
4. Restore the original backup into a separate disposable fault fixture. Hold a
   direct membership-table lock and run candidate migration with an earlier
   deadline. Require prompt refusal, complete DDL/marker/data rollback,
   unchanged original checksums and usable A1/schema004 state. Keep the primary
   backup untouched. Separately require migration-lock cancellation and
   concurrent-startup serialization in actual PostgreSQL fixture tests.
5. With writers stopped, migrate only through the schema-owner path using the
   exact candidate. Observe the real schema005 catalog, including function
   bodies/security attributes and trigger UPDATE columns, WHEN conditions and
   owned targets. Verify all five markers/checksums, exact initial pin counts
   including historical previews, original source values, jobs, media and byte
   accounting. Capture elapsed migration/backfill plus final TCP/SQL readiness.
   Timeout is a failed gate; keep the existing 30-second cap. Do not silently
   enlarge, remove or retry past that cap to manufacture a passing receipt.
   Capture migration/backfill observations before routine maintenance: the
   public `migrate` service ordinarily runs `maintenance`, which legitimately
   removes expired previews and can then delete their expired metadata sources.
   The prepared observer inherits that exact service's image, schema-owner role,
   network and container restrictions, with command `server` so the same guarded
   `OpenStore` completes before any cleanup. It publishes no ports, receives no
   HTTP requests, and is the only permitted migration writer. Capture its actual
   ID, command, image/source identity and sanitized constraints, then stop and
   remove it before normal wrapper initialization runs maintenance and grants.
   This observation boundary requires separate root review before execution;
   it is not accepted operational evidence. The later maintenance phase may
   legitimately remove the expired synthetic preview/source after their exact
   pre-maintenance pin counts and original values have been captured.
6. Apply normal reduced runtime grants and start candidate API/worker. Exercise
   actual runtime DML for all-job and exact-preview membership, idempotent
   INSERT, status/heartbeat changes, job/source-ID transfers, preview kind/ID
   transfers, direct cancellation, rollback and a preview/deletion race.
   Function `EXECUTE` remains denied, with no schema/role/history privileges or
   SECURITY DEFINER conversion. Preserve source/API contracts and byte totals.
   The schema-owner gate rejects altered function security/body/targets,
   trigger UPDATE columns and conditions without data writes.
7. Observe indexed TTL selection visiting at most 200 candidates before the
   actual-reference guard. A pinned false-zero prefix survives, becomes
   ineligible, and allows a following batch to delete the healthy tail.
   Independently audit false-positive counts through the all-source cursor,
   durable 128-child chunks and explicit unfinished progress. Cover restart,
   membership-revision reset, backdated wrap, rollback/cancellation, and a hot
   source that does not prevent another source's correction. Global sweep
   completion does not imply all partial child passes completed. Tombstones
   remain charged until actual physical removal/synchronization. After a real
   COMMIT with lost acknowledgement, recover from durable state without duplicate
   deletion or accounting release.
8. Before rollback, run the unchanged schema-guarded A1 binary on a separate
   schema005 copy. Require `schema_incompatible`, nonzero exit and no row,
   schema or ledger mutation. Restore the immutable pre-upgrade schema004
   backup into a fresh separate installation; verify A1 TCP/SQL, retained
   values, media, counters, ownership and jobs. Rollback is that fresh restore,
   not an in-place reverse migration or dropping schema005 columns/markers.
9. Take and hash a coordinated candidate/schema005 backup, restore into fresh
   candidate resources and verify the five-version catalog/checksums, derived
   counts/audit progress, API/worker recovery, physical media and byte accounting.
   Exercise new publication/export/download/decode after restore. Require
   policy apply/verify/remove, complete filter restoration, final TCP/SQL and
   verified removal of every owned container, network, volume, process, private
   key, state and work directory. Cleanup needs independent actual evidence;
   an otherwise passing receipt with incomplete cleanup fails.

## Separate gates and limits

The selected Go execution contains thirteen groups: ten source-retention groups
that use actual PostgreSQL, one pure catalog-manifest control, and two existing
PostgreSQL migration concurrency/cancellation groups. Named receipt checks reuse
that exact execution's raw events. The bounded-plan check also requires the
actual public runtime TTL plan and the real partial-child audit observations;
the pure manifest control alone supplies neither bound.
Future validation still requires the complete existing suite, the five native platform/archive checks,
archive recovery, and strict source/archive identity for the actual candidate
commit. A skipped check is missing evidence. None is implied by source-only
Python controls or by the transition receipt's named-check inventory.

Matched 10k/100k retained/eligible/poison/hot-child query plans and foreground
admission, publication, preview and job-status latency measurements are separate
future gates. Measure trigger cost, fence duration/waiters, first/resumed audit
seeks, all-source sweep and partial completeness. Keep the existing fixed
32×5ms stress limits and failures. Static LIMIT clauses do not establish a 5ms
SLA, measured bounded latency or a before/after improvement.

Current N20 runs use frozen old06bb binaries and provide no schema005 evidence.
Earlier public-profile, source, archive and native-platform proofs remain
immutable evidence for their own exact commits/runs. They cannot be borrowed to
complete the new receipt. The [source-retention semantics](source-retention.md)
and [migration compatibility rules](migrations.md) describe the prepared behavior;
actual acceptance and production readiness remain unestablished for this
transition.
