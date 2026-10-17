# Core 1.4.1 repository audit

The audit started from release `v1.4.1`, commit
`eb24d8e177de977da5e4a34b9b48ecabe14cf635`, on 2026-10-17. Follow-up work is
unreleased repository maintenance. The published tag, SDK package versions,
HTTP API and migration history retain their existing identities. Dependency
maintenance is recorded separately below.

The original narrative report was recovered from the audit conversation and
cross-checked against retained reproductions, merged changes, committed
regressions and revision-specific verification summaries. The five confirmed
defects fixed in
[PR 61](https://github.com/Kay0k1/cutmyvideo-core/pull/61) are distinguished below
from the separate optional process-lifecycle observation.

## Confirmed defects

| Original finding | Resolution | Retained proof |
| --- | --- | --- |
| A scoped collection job could commit before its project binding or capacity check. | Job admission and project binding share one transaction; replay and earlier partial results remain recoverable. | [Project admission tests](../internal/app/http_project_bindings_test.go): history limits, concurrent last-slot admission, scope recheck and partial submissions. |
| Project/source-preparation rollback could wait on a stalled database after the request deadline. | Rollback uses its own bounded context. | [Rollback test](../internal/app/http_rollback_test.go), including a deliberately stalled PostgreSQL response. |
| Relative HLS references used the playlist URL before a redirect. | Variants, segments and initialization maps resolve against the final guarded response URL. | [Redirect tests](../internal/app/hls_redirect_test.go), retaining target, redirect-count and transfer-budget guards. |
| A TypeScript source-initialization journal interrupted between hard-link publication and unlink could not resume. | Recovery recognizes the verified two-link crash window while preserving private-file checks. | [Source tests](../sdk/typescript/test/sources.test.mjs): real fresh-consumer interruption at journal, owner and state publication; unrelated hard links remain refused. |
| Portable Python/TypeScript projects rejected valid nullable crop settings. | Import/export preserves `crop: null` inheritance and explicit `clear_crop`. | [Python project tests](../sdk/python/tests/test_projects.py) and [TypeScript collection tests](../sdk/typescript/test/collections.test.mjs), including inherited and reset round trips. |

The source and installed-package SDK suites retain these regressions. The three
server groups are also required by the [native result gate](../scripts/check-native-results.py).

Later cross-platform verification also exposed two Python request/poll deadline
edges and a readiness-marker race in the process test fixture. [PR 68](https://github.com/Kay0k1/cutmyvideo-core/pull/68)
retained the existing timeout budgets, normalized their established SDK errors
and published the marker atomically. [Python client regressions](../sdk/python/tests/test_client.py)
exercise the deadline boundaries with deterministic clocks; the descendant
cancellation test retains its liveness assertions.

## Process-lifecycle closure

The original optional P3 observation concerned a configured Unix wrapper that
exits while leaving a child alive. Cancellation already terminated the process
group; ordinary parent exit could leave a descendant running, and inherited
pipes could return `exec.ErrWaitDelay` without ending that descendant. The
original reproduction used an owned synthetic wrapper. No normal FFmpeg failure
or media-triggered containment escape was established.

The Unix runner now ends its owned process group after waiting, preserving the
original process result. [The real-process regression](../internal/toolrun/process_lifecycle_test.go),
`TestRunOutputCleansDescendantsAfterParentExit`, covers redirected standard
streams with ordinary success and inherited pipes with `exec.ErrWaitDelay`.
Both cases reproduced a live child with the old Unix runner; the fixed toolrun
suite passes with the race detector while retaining those raw outcomes. Owned
fixtures have independent cleanup even when the regression fails.

The common regression is required by the native gate on all five targets.
Windows retains its existing job-object containment implementation; each
platform's execution result remains tied to the revision actually checked.

## Repository and maintenance work

| Original cleanup area | Completed work and review | Current location and proof |
| --- | --- | --- |
| Environment, generated files and repository hygiene | [PR 62](https://github.com/Kay0k1/cutmyvideo-core/pull/62) removed unused schema literals and excluded generated SDK/build/evidence data from Docker context. [PR 65](https://github.com/Kay0k1/cutmyvideo-core/pull/65) added SDK output-capacity admission and cleanup of successful runs' owned scratch. | [Ordered migrations](../internal/app/migrations/), [Docker context](../.dockerignore), [SDK runner](../scripts/check-sdks.py), [artifact retention](artifacts.md). Packages, frozen fixtures, logs and receipts remain retained. |
| API/SDK contracts and project lifetimes | [PR 64](https://github.com/Kay0k1/cutmyvideo-core/pull/64) retained both API baselines, added common examples and documented independent lifetimes. | [Compatibility policy](../api/COMPATIBILITY.md), [shared corpus](../api/testdata/project-contracts.json), [project lifecycle](agent-workflow.md#retention-access-and-recovery). Go semantics, OpenAPI structure and portable SDK imports have explicit separate expectations. |
| Shared models and server adapters | [PR 65](https://github.com/Kay0k1/cutmyvideo-core/pull/65) separated edit validation and project persistence from HTTP handlers. | [Edit model](../internal/edit/), project store/transaction code and compatibility tests retain normalization, lock/error ordering, JSON and ownership. |
| Engine dependence on the server package | [PR 67](https://github.com/Kay0k1/cutmyvideo-core/pull/67), [68](https://github.com/Kay0k1/cutmyvideo-core/pull/68), [69](https://github.com/Kay0k1/cutmyvideo-core/pull/69), [70](https://github.com/Kay0k1/cutmyvideo-core/pull/70) and [71](https://github.com/Kay0k1/cutmyvideo-core/pull/71) separated subtitle, process, metadata, export, preflight and session responsibilities, then moved the concrete backend and compatibility carriers. | [Architecture map](architecture.md), [public engine](../pkg/engine/), [local backend](../internal/localmedia/), [models](../internal/coremodel/), [errors](../internal/mediaerror/). Runtime closure guards exclude service/network/database dependencies; an outside-repository Go consumer checks public contracts. |
| Test and release maintenance | [PR 66](https://github.com/Kay0k1/cutmyvideo-core/pull/66) grouped existing check entry points, shared test-only release fixtures and removed a demonstrated duplicate native engine invocation. | [Command map](../CONTRIBUTING.md#command-map), [release fixtures](../scripts/release_test_fixtures.py), [release protocol](releases.md). Source, installed SDK, historical recovery, native archives and signed downloaded consumers retain their independent acceptance boundaries. |
| Support and installation instructions | [PR 62](https://github.com/Kay0k1/cutmyvideo-core/pull/62) refreshed the security policy, SDK distribution status and development commands, and added fixed self-hosting refusal codes without exposing raw exceptions. | [Security policy](../SECURITY.md), [SDK installation](sdk.md#install-versioned-packages), [self-hosting diagnostics](self-hosting.md#host-and-storage-boundary), [installation controls](../deploy/self_host_test.py), [1.4.1 release scope](release-1.4.1.md). GitHub release assets and registry publication remain distinct. |
| Dependency maintenance | The original audit found no reachable vulnerable call, but recommended updating `golang.org/x/sys` v0.42.0 for [GO-2026-5024](https://pkg.go.dev/vuln/GO-2026-5024). Final closure updates that pin to the first fixed release, v0.44.0; other dependency and Go/toolchain pins remain unchanged. | [Dependency pins](../go.mod), [module hashes](../go.sum) and generated notices. The updated revision requires fresh vulnerability, source, SDK and native checks; earlier green receipts do not verify this update. |
| Server file organization | Final closure groups existing functions by responsibility within the existing app package. | [HTTP jobs](../internal/app/http_jobs.go), [job admission](../internal/app/store_job_admission.go), [job leases](../internal/app/store_job_lease.go), [worker jobs](../internal/app/worker_job.go), [worker storage](../internal/app/worker_storage.go), [preview admission](../internal/app/preview_admission.go) and [preview rendering](../internal/app/preview_render.go). Existing function bodies, SQL and test identities are retained. |

Final organization work groups existing HTTP, store, worker and preview functions
by responsibility and removes verified unused private forwarding helpers. The
existing function bodies, SQL, test identities and public behavior remain the
preservation boundary. The native control suites share their event-mutation
fixture while retaining independent expected test inventories and assertions.

The workspace review classified retained reports separately from disposable
consumer scratch. It also found unused capacity on the operator disk: the root
partition was expanded from 80 to 150 GiB without deleting retained evidence.
Follow-up checks use isolated worktrees pinned to the reviewed revision. The
original disk-exhausted Go/race and installed-SDK attempts remain failed attempts;
they are not counted as successful verification.

## Verification and completion

The retained baseline has 308 mandatory native test identities, 49 native-gate
controls and 343 selected native tests on Linux/macOS (342 on Windows). The
process-return regression adds one mandatory identity on every target. Its
negative control must reject missing, skipped, failed, failed-subtest,
wrong-package, wrong-report and incomplete-package proof. Every baseline
identity, owning package/report and selected test must remain present.

Completion for a particular revision is established by its exact-commit CI and
local receipts:

- Full source acceptance with media/extractor/database prerequisites, strict
  HTTP/OpenAPI fixtures, uncached race tests, workflow checks and vulnerability
  analysis.
- Source and installed Python/TypeScript SDK tests, three built distributions,
  their frozen source-input hashes and all 15 required local/HTTP SDK results.
- All five native targets, production-family Bookworm media acceptance and
  disposable public self-hosting acceptance.
- Preserved API baselines, shared contract corpus, public Go/CLI/SDK behavior,
  migration bytes, release permissions and existing tag identity, with reviewed
  dependency updates.

Per-run execution receipts and raw diagnostics follow [artifact retention](artifacts.md).
CI reports and pull-request checks identify the revision they verified. The
separate [release protocol](releases.md) governs packaging, signatures, native
downloaded consumers and publication. This maintenance work does not create a
release or deploy an installation.
