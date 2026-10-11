# Fixed 004/005 PostgreSQL pilot

This is prepared benchmark tooling, not a performance result. The application
under comparison is fixed at `63873e935626d51087d0a26b84f1de9a7ed86a28`
(schema004) and `cf612b3b2c7610e4f8cac3dba22258b756f129a4` (schema005).
The newer API recovery branch and the separate mixed-media N20 workload are
outside this comparison. No runtime code or existing tests are changed here.

`identity.json` binds all164/168 canonical tracked application files, including
existing tests, and the actual selector SQL. Seven reused fixture/helper
functions are identical in both revisions. One common `.txt` Go fixture is
added as a new untracked test file to each fresh checkout. Source maps, Git
identity, injected bytes and tracked worktree status are checked before
injection, after compilation and after every workload. Both test binaries are
compiled before the measured workloads; compilation is not a SQL timing.

The manual workflow uses one fresh Ubuntu24.04 job, Go1.27.2 and one PostgreSQL
17.11 container pinned to OCI index
`sha256:3645570cccdfa447589da9f57dd740faa29b30938e861289a5574b6ca6b03826`
with explicit `linux/amd64`. The registry index, amd64 child manifest and config
digest are verified separately. An image-store `.Id` may use index identity;
it is not automatically described as a config digest. PostgreSQL has hard
2CPU/1GiB limits, `shared_buffers=128MB`, `work_mem=4MB`,32connections,
`jit=off`, `fsync=on` and `synchronous_commit=on`. The Go workload uses
`GOMAXPROCS=2` and soft `GOMEMLIMIT=128MiB`. Before setup, each compile and each
workload, admission requires2GiB free disk,3GiB available host memory and2CPUs.
These stage checks are not continuous disk/memory quotas. Raw Go `/usr/bin/time`
usage and PostgreSQL cumulative cgroup peak memory are retained with their
measurement scope.

The only admitted sizes are1,000 and10,000. Each uses a fresh namespace with N
retained sources/jobs/artifacts,200eligible expired sources and the unchanged
canonical cache, reservation and accounting fixture. Source/artifact chronology
is deterministic. The1,000 before/after pair must finish and pass all essential
correctness stages before10,000 begins. A failed short-cycle gate stays failed
while allowing the diagnostic10,000 pair when its essential correctness passed.
100,000 is refused; it requires a separate reviewed follow-up after actual pilot
results and resource admission.

The seven stages distinguish:

- Twenty real source TTL `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON)` samples under the
  existing storage fence. Each deletes exactly200eligible rows, preserves all N
  job-pinned sources and rolls back to identical rows/counters. Full plans and
  separate client/server times are saved. The report's `source_ttl.fence` field
  measures BEGIN and advisory-lock acquisition, including wait; it does not
  measure total fence hold. Subsequent COUNT oracles run under the same
  transaction after the timed SQL and outside its client/server timer. These
  verification scans extend the test transaction's fence hold. No foreground
  workload overlaps these20samples. Hot-lock behavior is measured separately in
  the held-fence and concurrent stages; these isolated samples support no
  production total-fence, planner or SLA improvement claim.
- Twenty samples each of status-only job UPDATE, job INSERT and exact preview
  INSERT, including actual invoker triggers and rollback/accounting checks.
  This first pilot does not cover every lifecycle DML type or historical source
  birth cardinality.
- The actual held storage fence with5ms/50ms caller contexts and acquisition
  after release. Wall times and outcomes are saved; a5ms wall-time guarantee is
  not claimed.
- Schema005's real persisted audit: a source with129job and129preview children
  remains partial after128+128, while its healthy successor is repaired. Ten
  committed units and raw persisted state are saved. Schema004 is explicitly
  N/A. Audit-only rows are removed before the common foreground workload. No
  full sweep, restart/revision storm or worst-case drift repair completion claim
  follows from this small probe.
- The original four-worker/80-publication foreground workload against20real
  maintenance calls, with cache, lease and accounting oracles.
- The original32×5ms public reconciliation gate, including progress before its
  later full scan and the unchanged-scan tuple invariant.
- The original32×5ms real bootstrap/admission gate, including progress before
  its later full retry. A later successful long pass cannot turn this gate PASS.

The fixture migrates a fresh schema before seeding. It does **not** measure
large populated004→005 backfill/startup latency; the current30s migration cap
needs a separate upgrade fixture. It also does not benchmark100k, media decode,
all-pinned/all-eligible cardinality variants or adversarial mutation/restart
storms. Those remain items in the reviewed larger measurement matrix.

Runtime DML runs with a generated NOLOGIN role via `SET ROLE` on fixture-admin
sessions. It has the existing ordinary-table DML grants, protected migration
ledgers and no schema CREATE/superuser privilege or function EXECUTE grants.
This tests effective invoker permissions, not production login authentication.
Every test namespace/role is checked absent after its process; the container is
removed and checked absent in the runner finalizer. Password/token environment
files and test executables are excluded from downloadable measurement evidence.

No dispatch has been authorized by this preparation. A new workflow may need
registration before manual dispatch. The path-filtered PR job runs only pure
parser controls with all actual children/network calls denied; the PostgreSQL
job requires `workflow_dispatch`. After the first pure run, the operator must
observe its registered workflow ID before freezing an exact dispatch context.
[GitHub's workflow-dispatch documentation](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_dispatch)
describes the default-branch/first-run requirement. Do not merge to main merely
to make the benchmark available.

After root reviews and freezes a future actual run/attempt/job/head, preserve the
original API responses, log ZIP, artifact ZIP/CRC members and every failure in a
fresh collection directory. `verify_saved.py` is the prepared offline artifact
parser; it additionally requires a reviewed context and original byte catalog.
It records whether measurement evidence verified separately from whether all
workload gates passed. It exits1 for a measured workload FAIL, even when some
latency stages passed. Never rerun to replace original failures or report these
synthetic parser fixtures as actual PostgreSQL benchmark evidence.
