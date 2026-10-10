# Metadata cache encoding: before and after, 2026-10-10

Reusing bounded gzip encoder workspaces reduces the repeated-fill cost of the
private metadata cache. Outputs retain independent ownership and byte-identical
gzip encoding. This measures **metadata encoding only**, excluding yt-dlp,
network, PostgreSQL and FFmpeg; it is not an export-speed measurement.

## Main comparison

Medians of ten one-second runs with the same fixture, Go 1.27.1, Linux amd64,
AMD Ryzen 9 5950X on a shared KVM host and two Go scheduler threads. Historical
measurements retain their original toolchain; release builds now use Go 1.27.2.
The baseline is `c9f339550ae3b6507cfb9d2c9f526d39be33c815` plus the same benchmark
harness. The after implementation pools `gzip.BestSpeed` writers and resets them
to `io.Discard` before returning them. Complete context is in
[environment.json](environment.json).

| Fixture | Before median | After median | Time change | Allocated bytes before / after | Stored bytes |
|---|---:|---:|---:|---:|---:|
| 182 formats, repeated 2,000-character tokens | 1.753 ms | 1.321 ms | −24.7% | 1,666,373 / 581,909 (−65.1%) | 5,126, unchanged |
| 182 formats, independent tokens | 4.013 ms | 3.576 ms | −10.9% | 3,014,134 / 2,171,925 (−27.9%) | 410,809, unchanged |
| Small JSON control | 4.590 µs | 4.430 µs | Timing noise | 1,380 / 1,380 | 777, unchanged |

The independent-token fixture correctly remains uncompressed. Pooling avoids
repeated encoder construction even when compression is attempted and discarded.
The small JSON control bypasses compression and retains ten allocations.
Allocated bytes are cumulative per operation, not peak/RSS memory.

Raw primary data: [before.txt](before.txt), [after.txt](after.txt). Machine-readable
medians/ranges: [summary.json](summary.json). Additional alternating three-pair
five-second runs show the same allocation effect in
[steady-before.txt](steady-before.txt) and [steady-after.txt](steady-after.txt).
Their third pair overlapped race-test compilation; those outliers remain in the
raw data and are not hidden. Timings include normal GC/host variability.

This is a steady-state benefit. `sync.Pool` may discard idle writers during GC;
first or infrequent fills may receive no benefit. Completed output buffers are
not pooled. Concurrency/ownership regressions, gzip compatibility and existing
size/corruption limits are covered by [validation.txt](validation.txt).

## Reproduce

Use the same benchmark harness on both revisions, no concurrent build/media load,
and capture toolchain/CPU settings with the raw results:

```sh
go test ./internal/app -run '^$' -bench '^BenchmarkMetadataPayloadEncode$' \
  -benchmem -benchtime=1s -count=10 -cpu=2
```

Use the Go toolchain recorded above to reproduce the historical comparison;
do not use that older security-patch level for a new production release. For new
comparisons keep the toolchain identical between runs. Baseline and after must
both include the encode-only harness; otherwise comparing benchmark names alone
does not reproduce this experiment.
