# TTL preview membership benchmark

Removing `OFFSET 0` from the preview-reservation `EXISTS` check reduced PostgreSQL median execution time from **9.9020 ms to 5.7465 ms** on this fixture. The candidate was faster in all 20 pairs. All 16 targeted tests, 40 rollback/state checks and cleanup passed. This measures one query on one synthetic dataset; complete maintenance latency, worker throughput, larger-preview scaling and performance SLAs remain unmeasured.

## Change and source

The baseline is [f80cb9bba29f2ba97b37152bbf3388735d4a3173](https://github.com/Kay0k1/cutmyvideo-core/commit/f80cb9bba29f2ba97b37152bbf3388735d4a3173), schema005. The candidate removes only the preview `OFFSET 0` from the production query:

```sql
-- Before
EXISTS(SELECT 1 FROM storage_reservations WHERE kind='preview' AND job_id=candidate.id OFFSET 0)
-- After
EXISTS(SELECT 1 FROM storage_reservations WHERE kind='preview' AND job_id=candidate.id)
```

Jobs `OFFSET 0`, materialized candidate selection/order, the 200-source batch bound, fence, real pins, quarantine, accounting and unknown-commit handling remain intact. The two-file candidate patch also adds the false-preview-pin/empty-ID/batch regression check; existing test bodies are preserved.

[Run 38147656939, attempt 1](https://github.com/Kay0k1/cutmyvideo-core/actions/runs/38147656939) used harness head `fe362759a9176418ffb88dd6424adafc6e77efee`. Its product files remain at f80; the [exact candidate patch](https://github.com/Kay0k1/cutmyvideo-core/blob/fe362759a9176418ffb88dd6424adafc6e77efee/benchmarks/ttlpreviewprobe/candidate.patch) was applied in a disposable runtime worktree. [baseline.sql](baseline.sql), [candidate.sql](candidate.sql) and their SHA-256 identities are recorded in the [paired report](paired-report.json).

## Procedure

Both queries use the same fresh database, schema005, triggers, foreign keys, indexes, ordinary runtime role and settings. The warm synthetic fixture contains 1,000 retained/pinned sources, 200 expired eligible sources and 200 preview reservations. Data is populated and analyzed before timing. We retain all 20 samples per query: odd pairs baseline/candidate, even pairs candidate/baseline. **The first pair is included; no samples or warmups are excluded.**

Each sample uses `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` with arguments 3600/200 inside the storage-fenced transaction. Original plans are saved before decoding. In-transaction checks require 1,000 retained sources and zero expired sources. Each rollback is acknowledged; all 40 snapshots match the initial 15-table row/count/xmin digest byte-for-byte. Counters equal stored/reserved ledgers in every sample and before/after the study. One `go test -race` invocation passed all 16 tests, with no failures or skips.

## Results

p50 is the conventional median; p95 is linearly interpolated at `(n-1)*0.95`. PostgreSQL time is the original `Execution Time` scalar. Client elapsed time covers the measured EXPLAIN call; fence acquisition is recorded separately. Neither is complete maintenance time.

| Time, ms | Baseline p50 | Candidate p50 | Baseline p95 | Candidate p95 |
| --- | ---: | ---: | ---: | ---: |
| PostgreSQL execution | 9.9020 | 5.7465 | 10.44385 | 5.82810 |
| Client elapsed | 12.3227505 | 8.0511595 | 12.92463055 | 8.64057650 |
| Fence acquisition | 0.4651385 | 0.4588775 | 0.58830235 | 0.52566980 |

Server p50 decreased 41.97%; client p50 decreased 34.66%. Both are lower in 20/20 pairs. Median paired baseline-minus-candidate differences are 4.181 ms and 4.235098 ms, respectively.

All 20 baseline preview nodes are `Seq Scan`, `SubPlan 3`, with **200 loops**, 200 rows filtered per loop and **600 shared-buffer hits**. All 20 candidate nodes are `Seq Scan`, `SubPlan 4`, with **one loop**, 200 returned rows and **3 shared-buffer hits**. Non-VERBOSE JSON does not expose an explicit hash node or projection hash expression; one preview read instead of 200 is the observed change. Every plan has zero shared reads and zero temporary reads/writes. FK/fence trigger calls remain 200/200/200/1. Inclusive nested node times are not summed.

A 200-source candidate bound does **not** bound global preview rows, read work or memory. Large preview inventories, small batches, cold caches, concurrency and full maintenance/worker throughput were not measured. Planner behavior and benefits may differ there.

## Environment

- PostgreSQL 17.11, `postgres@sha256:3645570cccdfa447589da9f57dd740faa29b30938e861289a5574b6ca6b03826`; amd64 child `66aafa11cf15800a3c94763f7e11d1e7b2e37e5e84bc4ce027cb6e1e4bfaf4df`, config `589f742565e4f6d22149176b032974f55f8201a8a37b71ed587ec5c6902e689f`.
- Owned fresh PG: hard memory 1 GiB, memory+swap 1 GiB (no extra swap), 2 CPUs, 128 pids and private 512 MiB tmpfs. No service/production database or persistent media volumes.
- `shared_buffers=128MB`, `work_mem=4MB`, `max_connections=32`, `jit=off`, `fsync=on`, `synchronous_commit=on`, identical for both queries.
- GitHub-hosted Ubuntu 24.04, Go 1.27.2/race, Go threads 2 and soft memory 128 MiB. Host admission: available memory ≥3 GiB, free disk ≥2 GiB, CPUs ≥2; all three admissions passed on a 4-CPU host.
- One product invocation: Go timeout 6 minutes/process limit 420 seconds; total runner 900 seconds plus 120-second cleanup budget. One pinned dependency/image preparation preceded offline execution. Output caps, original source guards and cleanup were verified; owned schema/roles, runtime/caches and container/network were removed without OOM.

## Earlier attempts and raw files

[Run 38143536108](https://github.com/Kay0k1/cutmyvideo-core/actions/runs/38143536108) remains FAIL due to module-cache cleanup. Its older LATERAL candidate passed 16 tests and completed 40 measurements without improvement (server p50 7.4135→7.5700 ms; preview still 200 scans). Those separate-run timings are not pooled here. The scoped cleanup fix later passed four unprivileged controls and cleanup in both subsequent runs.

[Run 38146228174 on 1410489](https://github.com/Kay0k1/cutmyvideo-core/actions/runs/38146228174) remains FAIL: 15 semantic tests passed, but an old fixture SQL hash stopped measurement before any plans were saved; cleanup passed. This successful run corrected only that fixture constant and coupled pins, retaining its identity guard and procedure.

This directory contains 84 byte-exact measurement files: paired report, two SQL literals, 40 EXPLAIN plans and 41 snapshots (initial plus 40 post-rollback). Every report filename resolves here. README brings the content count to 85; [manifest.json](manifest.json) records sizes/hashes. Runner/provider logs, execution authorization, signed download URLs and operator configuration are excluded.
