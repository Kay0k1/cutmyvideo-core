# Normal storage operational capacity profile

This is a new source-only profile, with no measurements or dispatch yet. It
compares exact schema004 source `63873e935626d51087d0a26b84f1de9a7ed86a28`
(application164/source288 files) with current combined schema005 source
`d594cc4681dc1cab61664cec8c92bdf9a3e9444c` (application173/source391 files).
The schema005 application inventory is `d1ae273e0d56212ef549f029970cd4ea1f33276faa7e4e11c88127e3aeeacef6`.
This is the combined scan/retention/API/disk candidate, not isolated attribution
of any one optimization. N20 and the query-only preview study remain separate.

The legacy files in `../storage005` stay byte-identical to the historical
`cde55b3` tooling. Its 32 actual5ms reconciliation/bootstrap cycles,512-entry
criterion, unbounded final diagnostic recovery and recorded failures are not
read or relabeled by this profile. The new report kinds, injected test name,
artifact directory and profile `normal10s5s` distinguish the new results.

The registered workflow path remains `.github/workflows/storage005-capacity.yml`
in this isolated tooling branch. PR execution runs only this profile's pure
parser controls; the PostgreSQL job still requires an explicitly authorized
manual dispatch. Its registered ID, exact future tooling head, first refusal
and actual run must be observed rather than assumed. No merge to main, runtime
or dispatch is authorized by these files.

One identical test-only fixture is injected into both fixed fresh checkouts.
All tracked source bytes, modes, Git tree/parents and application inventories
are checked before injection, after remote compilation and after each workload.
Only the injected untracked filename is allowed. Compilation stays outside
measured calls. The fixed source pins accept neither arbitrary refs nor SQL.
The actual selector comes from its source literal. No current-only API constant
is used to compile the schema004 fixture; `RunMaintenance` exists in both.

The five retained measured stages are the same20 source-TTL plan/rollback
samples,20 samples of each foreground DML operation, held-fence cancellation
(5/50ms, not a progress promise), schema005's real128-child partial audit (N/A
at004), and four80-publication concurrent API workers. Their SQL, state,
rollback, membership and accounting oracles remain. The new scoped concurrency
copy gives each maintenance Cleanup+Drain call10s, each metadata/cache read5s,
and Reserve+Add a shared5s caller context. Its1s independent lock observer now
records errors as workload failures. This is distinct from the original1s
pressure workload; the old helper is unchanged. Protected retained rows,
healthy physical artifact bytes, each exact16-byte healthy ledger charge and exact preview pins are checked around the
new foreground stage.

The two new operational stages each create an independent fresh namespace,
role-bound pools and original N retained sources/jobs/artifacts plus200 expired
eligible sources,200 preview reservations and the canonical caches/reservations.
They materialize990 or9,900 healthy16-byte retained artifacts; deliberately
expired i%100==1 artifacts may be removed. They also create N zero-byte files
and1,0249-byte files, with exact path/size arrays and no SQL LIKE patterns.

The maintenance stage calls the actual one-shot `RunMaintenance` with a10s
caller context. It does not exercise the background worker loop. The admission
stage calls actual `ReserveSource` with a5s caller context; it does not measure
HTTP guards or upload transfer. Each has at most eight calls and stops upon
independently verified completion. No whole-scan fallback or unbounded repair
is called. Each records its effective deadline, wall time, return/ACK,
parent-expiry classification and original error type separately. A pure
original deadline leaf is expected pending work only when its own parent
expired; a mixed PostgreSQL/connection/constraint error stays genuine FAIL.
`ErrStorageInitializing` with a live parent is bounded continuation. No failed
or uncertain call is claimed complete or rolled back. A nil-error ACK observed
after its caller expires is recorded explicitly and fails the whole stage; a
later healthy ACK cannot repair that verdict.

After every call a fresh one-connection runtime pool reads committed per-path
charges and progress outside the product timer. Every previously recorded path
must remain with the exact charge; offsetting charge errors cannot pass. The
full protected-row SHA256 and counts, actual job/preview pin counts at005,
regular-file contents, and complete file/reservation ledger equality must
remain correct. Protected snapshots omit only005's four derived pin fields.
Completion requires all exact scan paths, the durable initialization marker,
three bootstrap kinds (API) or all five bootstrap/reconciliation kinds
(maintenance), a nil acknowledged return and a live parent. Admission also
requires the exact owned reservation, then a separate bounded5s Release and
fresh final state proving it gone. An expired parent does not produce a normal
PASS. The independent30s readback budget does not extend any product call.

Only1,000 and10,000 are admitted. The first manual execution should select
`maximum_size=1000`. Selecting10,000 must first execute the fresh1,000 pair;
promotion requires baseline essential five-stage correctness, all seven current
stages, exact original Go terminals, no namespace/role leaks and matching
source/PG identities. Any baseline operational failure remains a comparison
FAIL even if the current candidate qualifies for the next size. Final container
cleanup is required.100k remains refused. Populated004-to005 migration/backfill
latency is not measured here.

Resources and transport/capture guards reuse the reviewed current pilot:
Ubuntu24.04/Go1.27.2; identical pinned PostgreSQL17.11/linux-amd64 OCI index
3645570c, amd64 manifest66aafa11 and config589f7425; hard2CPU/1GiB/no-extra-swap;
shared128MB/work4MB/maxconn32/JIToff/fsync+sync-commitON; Go2threads/soft128MiB;
stage admission2GiB disk/3GiB host memory/2CPUs. These admissions are stage checks,
not continuous host quotas. The existing55-minute job,30-minute workload window,
6-minute test timeout and420s owned child capture still bound failures. A cap
refusal or timeout is not a performance result. No media decode is performed.

The15 synthetic controls passed once with zero actual child/network attempts;
those operations are denied before candidate helper imports. The identical
fixture compiled offline with Go1.27.2 against both fixed commits, without
executing either test binary. Cached actionlint1.7.12 passed with shellcheck
and pyflakes disabled. No workload for this new profile has run. Fresh workflow
registration and concrete resource/run authority remain required before any
dispatch. Synthetic controls, compilation and any single operational observation
establish neither an API latency SLA nor global maintenance/worker health or
large-preview scaling.

The420s owned child watchdog terminates and reaps its child group on refusal;
that raises into the runner finalizer, which removes and independently checks
the owned container. A Go6-minute timeout also remains a failure. A hard kill
of the Python outer or55-minute job may bypass its finalizer; such a run has no
complete cleanup receipt and cannot be accepted. Original provider logs and
owned-resource cleanup must be recovered independently before another plan.
