# Supplemental repeated-download observation

The draft-serving path was slower in this synthetic 4 MiB repeat-download experiment:
the median of three Go per-operation averages was **15.763003 ms** versus
**6.095654 ms** for the ordinary final-serving path,
an increase of **9.667349 ms**, or **2.585941×** (about 2.586×). All six observed
measurements are retained below and in [results.json](results.json).

Both paths used the same M7 candidate build. Three ordinary-final measurements
ran before three draft measurements; order was not balanced. Each measured
result used 20 iterations. These are averages of those iterations, not the p50
latency of individual requests. Go benchmark calibration can add requests
excluded from the six reported averages.
The synthetic file was written and fsynced before the timers; transfers used a
real local HTTP server. Database and authentication lookups were outside this
benchmark.

| Serving path | Measurement | Iterations | ns/op | MB/s |
|---|---:|---:|---:|---:|
| ordinary-final | 1 | 20 | 5240532 | 800.36 |
| ordinary-final | 2 | 20 | 6095654 | 688.08 |
| ordinary-final | 3 | 20 | 8886390 | 471.99 |
| draft | 1 | 20 | 27207957 | 154.16 |
| draft | 2 | 20 | 15196632 | 276.00 |
| draft | 3 | 20 | 15763003 | 266.09 |

The exact command, from `/src` in the tested native-check intermediate image:

```sh
go test -p=2 -mod=readonly -run="^$" -bench="^BenchmarkDraftCacheRealHTTPRepeatedSyntheticDownload$" -benchtime=20x -count=3 -timeout=120s ./internal/app
```

The actual environment was Linux/amd64, Go 1.27.2 and an AMD Ryzen 9 5950X. A new
container had a 1 GiB memory cap, 1.5 CPU quota, 128 PID limit, no swap allowance,
a read-only root and no external network; its own loopback served HTTP. Private
temporary data used an ext4 disk-backed bind mount. GOMAXPROCS was 2 and
GOMEMLIMIT was 384 MiB. The writable Go cache was copied from the already tested image
before execution. The container exited 0 without OOM and was removed after its
original evidence was retained.

The actual intent was recorded at `2026-10-16T15:50:51.975630+00:00`; the container ran from
`2026-10-16T15:50:52.187262215Z` through `2026-10-16T15:50:56.134346575Z`.
Those timestamps include setup and are not a first-clip latency measurement.

Candidate source: [`internal/app/http_draft_cache_test.go`](https://github.com/Kay0k1/cutmyvideo-core/blob/4531d7e2b63cf81421ca48da1d1f216b4e53bd00/internal/app/http_draft_cache_test.go#L415),
commit `4531d7e2b63cf81421ca48da1d1f216b4e53bd00`, version `perf-first-draft-M7`.
The measured benchmark-source SHA256 is
`d184c442cd102387f2e9d801e8b7fa5dca40263f13afbde3e213b3ae3d2bff7d`.
The tested intermediate image is
`sha256:0246b75da9d725d6b83fb96ae7fa4f07627ddb964652040e89badcb6d302a592`;
the corresponding candidate runtime image is
`sha256:d19fe885ec1159c8050e3a87c9f9cce4f01a16b789dfecbd27341a1995f33d32`.
Original raw-output, result, closure and build-evidence SHA256 values are retained
in results.json; private container identities, paths and production configuration
are excluded.

This observation documents an adverse repeat-I/O tradeoff. It makes no SLA,
export-speed, aggregate-memory or formal-acceptance claim. No cold-cache state
was asserted. The separate goal of obtaining the first usable draft from a
fifteen-clip interview needs its own registered full-workload evidence; these
repeat-download numbers do not measure that goal. Concurrent or later readers
can incur additional I/O when the draft cache hint is applied, and the hint
need not evict dirty, pinned or immediately reused pages.
