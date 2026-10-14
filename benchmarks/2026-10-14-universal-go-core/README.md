# Universal Go core performance pass, 2026-10-14

Status: the finite implementation, verification and production-toolchain
comparison are complete. Several heavy workloads improve; some joined workloads
are slower. No overall acceleration, regression-free guarantee or competitor
comparison is established.

The immutable baseline is `a9c75df83f965bf1d56e2263ca585ab77aeaf305`.
The original build-time evidence manifest SHA-256 is
`660f1007db49c49ad01c6f1c2aa4bf564709823689342c9c26decf3b61091ba9`.
The final execution manifest is
`ad92380c9ce478fe778ce030b5e8f30d81b78c8a1ba3cbfc82c85a900260e38b`;
the [rebinding receipt](execution-rebinding.json) distinguishes execution
configuration from actual compilation provenance. No Go binaries were rebuilt
for the toolchain change. The [final generation](generation.json) registers one
complete attempt, excluding every earlier attempt's timing.

The accepted comparison contains **627 whole operations and 4,792 independently
decoded files**: 567 primary operations / 4,542 files and a separately registered
60-operation / 250-file control. There are 72 primary comparisons, plus 20
isolated-Go process observations. Additional qualification and CPU diagnostics
are excluded from these counts. See the [selected results](results.md),
[all cases](cases.csv), [all primary observations](observations.csv),
[metrics and limits](metrics-summary.json) and [proof index](proof-index.json).

The finite [protocol](../../scripts/core-performance.md) registers 65 CLI,
embedding and guarded-HLS scenarios plus six concurrent HTTP/worker scenarios.
It retains short, distant, backward, overlapping and repeated ranges; MP4/MKV/MP3;
accurate/copy; separate/joined; and actual VFR, rotation, audio/subtitle and crop
fixtures. Raw timing is separate from independent probing and full decode.

The source checkout for the baseline remains unchanged. Candidate drafts began
before the on-disk corpus manifest was written; the workload and baseline were
frozen before the paired measurements, without using candidate timing to select
cases. This chronology must not be described as a pre-edit manifest freeze.

## Measured outcomes

Times below are cohort medians. The speed ratio is the median of matched
baseline/candidate ratios, which need not equal the ratio of those medians.
The baseline and candidate use the same production-family FFmpeg 5.1.9 tools.

| Workload | Baseline → candidate wall time | Paired ratio; faster pairs |
|---|---:|---:|
| Complex copy MP4, 20 separate cuts | 4.769 → 2.777 s | 1.715×; 3/3 |
| Complex copy MKV, 20 separate cuts | 5.084 → 2.914 s | 1.744×; 3/3 |
| Complex accurate MP4, 20 separate cuts | 9.550 → 7.757 s | 1.242×; 5/5 |
| Embedded copy MKV, 32-cut join | 13.570 → 10.828 s | 1.248×; 5/5 |
| HTTP copy, four owners / 16 jobs | 4.821 → 4.299 s | 1.122×; 5/5 |
| HTTP MP3, four owners / 16 jobs | 3.216 → 2.950 s | 1.088×; 5/5 |
| Controlled mixed workload, preparation P0 → P1 | 8.637 → 8.131 s | 1.062×; 5/5 |

The complex 20-cut plan contains four exact repeats. Its accurate path avoids
four encodes (20→16 FFmpeg invocations), and CPU falls 18.578→15.047 seconds.
The embedding MKV join reduces FFmpeg invocations 97→64 and FFprobe 98→92.
These improvements apply to the registered inputs and range plans; they do not
establish equivalent gains for distinct ranges or every codec/container.

Server bursts start after uploads and end after all artifact bytes are read;
polling is 250 ms. Each job has two 1.1-second cuts. The mixed witness uses real
HTTP sessions, imports, uploads, jobs and downloads with a controlled extractor
and HTTPS transport, rather than the public network or native yt-dlp. Its local
jobs have two six-second cuts, polling is 500 ms, and the fixed transport delay
is 250 ms. P1 overlap and all admission/accounting gates pass, but this is not a
sustained production throughput or reliability result. Preparation stays zero
by default.

HLS reuse reduces transferred bytes more consistently than end-to-end time.
For example, accurate cold outer traffic falls about 352→153 KiB while the
paired time ratio is 0.987×. A warm timed phase can fetch zero bytes, but the
whole operation still includes cache population. The copy-MKV straggler case
improves 1.12× in all five pairs. Full outer/phase metrics remain separate.

Observed adverse cases must remain visible:

| Workload | Baseline → candidate wall medians | Paired ratio; faster pairs |
|---|---:|---:|
| Local accurate MP4, 20-cut join | 0.625711 → 0.626926 s | 0.895590×; 1/3 |
| Local MP3, 20-cut join | 0.372444 → 0.408515 s | 0.900223×; 0/3 |
| Local MP3, two-cut join | 0.215835 → 0.224890 s | 0.918952×; 1/3 |
| Local MP3, 32-cut join | 0.481035 → 0.481223 s | 0.950773×; 2/5 |
| Embedded accurate MP4, 32-cut join | 0.806424 → 0.833670 s | 0.958581×; 0/5 |
| Local copy MKV, singleton join | 0.599210 → 0.620097 s | 0.959618×; 1/5 |
| Local accurate MP4, two separate cuts | 0.394185 → 0.410043 s | 0.973742×; 0/3 |
| Complex accurate MP4, 20-cut join | 6.37116 → 6.54564 s | 0.975400×; 2/5 |

The nearly equal cohort medians in some rows do not erase the adverse paired
results. MP3 joined20 also increases CPU 0.365735→0.404387 seconds. The later
unwrapped embedded accurate32 control gives the opposite direction, 1.104×
with 4/5 faster pairs. Separate cohorts, ordering and cache states prevent a
causal claim about wrapper overhead. Neither that control nor a later diagnostic
replaces the registered slower observations. All remaining unchanged and slower
cases are retained in the complete case table.

Resource costs also differ by workload. Subtitle indexing adds 81,924 B/op of
temporary allocations and one allocation. The single-owner server MP3 witness
has sampled aggregate RSS 231.906→253.102 MiB and sampled writes
69.223→103.082 MiB; single-owner server copy writes are
104.754→137.867 MiB. These whole-helper scopes include setup/cleanup; sampled
RSS can double-count shared pages, and sampled I/O is a lower bound. HLS cache
reuse typically adds about 0.149 MiB of cold/straggler workspace and 0.093 MiB
when warm. No aggregate ratio across different workloads is reported.

### Separate joined-path CPU diagnostic

After the complete primary and unwrapped cohorts, a registered instrumentation
experiment ran four joined cases in three alternating pairs: 24 operations and
24 files, excluded from the 627 operations / 4,792 files above. All 12 pairs
produced byte-identical files. Actual native commands, affinity and concat
contents match after normalizing only owned temporary paths; each operation
launches one FFmpeg and two FFprobe children. See the
[CPU and command proof](joined-cpu-diagnostic.json).

The spy separates waited native CPU, its own startup/work and an application
residual. That residual includes unmeasured spy logging/exit work and is **not
exact Go CPU**. MP3 joined20 is faster in all three instrumented pairs; its
residual candidate-minus-baseline changes are −1.200, −1.062 and −0.011 ms.
This does not reproduce a fixed extra 40 ms application cost, explain the
primary regression or invalidate it. MP3 joined32 remains slower in these
instrumented pairs; the higher CPU is predominantly recorded native/spy work,
with less than 1 ms residual changes in its higher-CPU pairs. Equal commands
do not establish why their execution cost differs. No thermal, noise or
Go-only root cause is proved, and no diagnostic result replaces a primary row.
Reproducing and attributing the adverse joined cases on intended deployment
hardware remains a focused follow-up before any blanket latency claim.

## Decisions requiring separate evidence

The planner timings below are separate diagnostics with the original frozen
master-build media tools; they are not pooled with the production5.1.9 cohort.

A universal contiguous decode across distant cuts is rejected. On the registered
32-cut long-recording positions, selected intervals cover 35.2 seconds while the
bounding continuous region covers 3531.1 seconds. A separate diagnostic measured
0.182 versus 6.074 seconds wall time and 0.300 versus 12.786 seconds CPU. These
are stage observations, with different output semantics, not equivalent complete
exports. A second, output-equivalent two-cut prototype passed complete byte identity,
raw video/PCM hashes, PTS and duration checks. On three alternating pairs,
ordinary total encode time fell 107.89→92.56 ms and 1080p60 fell
1.03946→0.96827 s. Complex-input CPU fell 3.67%, while peak process RSS rose
121.07→174.42 MB (44.1%). The first-output process-complete bound worsened
485.03→968.27 ms; it excludes inspection, publication and download. The shared
process uses two simultaneous encoders, requiring revised memory/thread
admission and portable publication semantics. The current planner therefore
retains separate exports; this is a measured future opportunity, not evidence
that close sharing has no benefit. Exact repeat reuse is implemented.

The first one-pass copy-audio experiment used an output seek and lost complete
B-frame GOPs. That experiment is rejected. The final guarded packet-trimming
variant is restricted to AAC/MP3/FLAC with a verified millisecond source clock
and Matroska output. MP4, Opus, finer/unknown clocks and unsupported tools retain
the original staged path. Exact packet-clock comparison found that skipping the
intermediate Matroska normalization changed fine-clock AAC timestamps despite
identical decoded samples and packet payloads; older MP4 muxers also changed
packet durations for already coarse sources. Neither is accepted as an
output-equivalent optimization. The strengthened compatibility matrix checks
exact PTS/DTS/duration ticks, time bases and container duration in addition to
the existing independent decoded/source-timeline checks.

The guarded subset covers intentional delays, selected tracks, dual inputs,
large offsets and unknown presentation timestamps through its safe fallbacks.
All Opus outputs keep the original staged path: on 5.1.9
both versions retain pre-existing source-relative offsets of 6.5 ms for MP4
and 13 ms for Matroska, with exact candidate/baseline packet-clock and complete
decoded-PCM equality. Optimized combinations
retain the source-relative 1 ms gate.

A smaller copy reservation is not inferred from source file size plus an
arbitrary mux-overhead allowance. Likewise, packet copy retains conservative
decoder weights while its composite verification/subtitle path can decode.
Storage lock redesign depends on measured wait/hold time; durability fences are
not removed on the basis of query counts.

## Interpreting the completed comparison

The final report keeps whole operations, media-command stages and isolated Go
loops separate. A duration ratio is baseline/candidate for each matched pair;
values above one favor the candidate. Pair wins are observations, not a
statistical significance test. The complete case table retains unchanged
and slower cases alongside improvements. Three or five pairs do not establish
a p95, capacity limit or production SLA.

The main and HTTP harnesses use small counting shells that immediately execute
the frozen media tools. Only the mixed witness uses a Go invocation observer.
Both revisions use the same observer, but eliminating invocations also avoids
its per-call cost. A separately registered control therefore repeats all four
public embedding witnesses, the 32-cut copied MP4 join and the complex 20-cut
accurate separate export using direct tool symlinks: five alternating pairs per
case, 60 operations and 250 independently decoded files. It controls the main
counting-shell overhead; a tool invocation is counted once, rather than once for
the shell and again for its executable. Its CLI/embedding cases do not calibrate concurrent
server scheduling or the mixed Go observer.

The corpus intentionally contains exact repeated ranges. The complex 20-cut
fixture contains four repeats, so avoiding those four encodes is a real benefit
for that workload, not evidence that every set of 20 distinct cuts becomes as
much faster. Whole-operation comparisons retain the same ranges and order.

## Implemented paths and bounded costs

| Path | Change and applicability | Bound or remaining cost |
|---|---|---|
| Public engine / CLI | Reuse the verified source inspection within an operation; pass the complete requested range plan into the session | Fresh media outputs receive fresh probes; no database is required by `pkg/engine` |
| Exact repeats | Copy a matching verified result or reuse a copy-join component within the current operation | Copies inherit prior semantic verification with new source/result identity and byte-count checks, ordered independent output inodes and publication; changed files regenerate |
| Copied joins | Replace audio extraction plus remux with one source-audio seek and packet-trimming mux only for AAC/MP3/FLAC with a millisecond source clock and MKV output | MP4, Opus, finer/unknown clocks and unsupported tools retain the original path; one cold capability query can offset a tiny-batch saving |
| Keyframes | Plan nearby start windows and reuse successful searches | At most 16 windows / 4,096 keyframes, at most 15 seconds optional lookahead; unsuccessful wider scans fall back |
| MP4 | Relocate metadata only for final MP4 outputs | Internal join components no longer pay faststart; final output behavior is preserved |
| Subtitles | Retain one bounded parsed track and index sorted cue overlaps | About 1 MiB conservative retained-track bound; small unsorted tracks use a cached linear scan, larger tracks use the disk fallback; overlap indexing allocates more memory |
| Waveform | Aggregate integer sample maxima before normalizing each bin | Preserve split int16 samples, channels and source-rate clock; decoding remains external |
| HLS | Rolling ordered download pool and job-local reuse of complete segments/init maps | Four active downloads, eight outstanding positions, at most 128 entries / 32 MiB of cached payload files on disk; cache disabled for stores at or below 256 MiB |
| Worker source inspection | Reuse verified local probes across jobs in one worker | File ownership/retention/tool/file identity checks remain; output probes remain independent |
| Worker scheduling | Optional input preparation can overlap work behind a shared weighted media budget | Preparation remains zero by default; copy paths keep conservative decoder weights |
| Job progress | Persist only the changed item/stage under the current lease fence | PostgreSQL may still rewrite JSONB; no WAL reduction is claimed |
| Metadata misses | Persist short resolving leases instead of retaining a pooled connection during upstream work | Migration 007, bounded peer wait, token-fenced publication and recovery; cache deadlines remain unchanged |
| Cleanup | Filter eligible source-media candidates before the bounded selection limit | Revalidation remains; blocked entries no longer hide later eligible candidates |
| Remote thumbnails | Fetch on the first protected thumbnail request, outside import's critical path | Four fetch slots, two-second deadline, 2 MiB byte cap and owner/source/publication fences |

Cache hits still consume interval staging allowance, while only actual HLS
transfers consume the network byte guard. Hard links preserve immutable cached
files, not output aliases. Cleanup joins download workers before removing the
workspace; rejected cache-link cleanup failures stop subsequent admission.
Workspace accounting remains reserved until actual removal is acknowledged.

Windows file identity is captured from an open cache-file handle before closing it. A
path-based `os.Stat` identity may be lazy on Windows and cease to work after the
original producer path disappears. The native cache tests retain their original
hit assertions and cover this behavior, cancellation, pre-existing init-map
ownership and rejected-link cleanup.

## Deferred changes and limits

The common storage fence remains. On the bounded 1,000-retained-artifact
diagnostic with one/four owners, the client-observed acquisition-ACK through
commit-ACK hold was approximately 2 ms median / 3 ms p95, and the four-owner
lock-query p95 was approximately 6 ms. Those small synthetic artifacts do not
establish behavior at 100,000 retained rows or production capacity. Removing a
durability/quota fence is not justified by this fixture.

Full packet counting also remains outside admission. On the one-hour fixture
it cost about 0.20 seconds versus about 0.02 seconds for inspection and traversed
approximately 245,000 packets. This provides no universal mux-overhead bound.
Source file bytes plus an arbitrary allowance are not a safe output ceiling.
Copy weights remain conservative because verification and subtitles can decode;
no large-decoder resource proof was obtained for relaxing them.

Preparation overlap has a separate reliability limit. In the preserved v4
mixed run, one P1 media invocation remained active for about 118.8 seconds until
the original job deadline canceled it. No Go budget/cache lock cycle was found;
the evidence does not distinguish a native FFmpeg stall from invocation plumbing.
Twelve bounded diagnostic repetitions completed without reproducing it, which
does not close the original failure. The final run added bounded observation of
long-lived owned tools and excludes any diagnostically instrumented sample.
Keep preparation at zero unless the operator separately validates their tools
and workload. The final controlled P1 fixture is faster in five pairs (1.062×),
but does not resolve the earlier failure or establish production reliability.

Rust/C++ helpers, hardware encoders and smart rendering remain future candidates.
This pass first removes duplicated external work and improves the Go loops that
have a demonstrated cost. It supplies no measured reason to add another language
to the universal CPU path. Hardware-specific gains require separate devices,
quality/output checks, resource admission and fallback evidence.

The next useful measurements are specific:

1. Diagnose the preserved P1 invocation stall with the exact tool build and
   captured active-process evidence before considering preparation as a default.
2. Evaluate bounded reuse of prepared HLS windows/probes. Segment reuse removes
   transfer work, but the HLS witness still performs remux/probe/export work for
   each interval. Any new cache needs timeline/init-map/discontinuity, ownership,
   cancellation and storage-accounting proof.
3. Extend the output-equivalent close-cut planner prototype with explicit
   memory, thread and first-output constraints. Distant ranges must retain fast
   seeks; sharing a bounding decode is already a measured loss.
4. Measure coalescing simultaneous cold local probes. Later jobs already hit the
   worker cache; concurrent first jobs can still inspect the same source before
   either fills it. Its contribution is much smaller than complex encoding.
   Also evaluate bounded worker-level reuse of verified keyframe windows between
   jobs. The current cross-job cache retains stream inspection, while keyframe
   searches remain operation-scoped. Any wider reuse needs owner/source/tool/file
   identity checks and eviction bounds; completed outputs still need fresh probes.
5. Profile representative distinct-range accurate exports on the intended
   hardware. Those paths are still dominated by FFmpeg; additional Go plumbing
   changes and a new language do not by themselves remove encoding cost.
6. Revisit copy admission and storage fences only with a safe output bound,
   actual decoder peaks and larger retained-row/contended-workload evidence.
7. Separate the local media package from the server package if consumer build
   dependencies matter. `pkg/engine` currently imports `internal/app`, which
   also compiles PostgreSQL, HTTP and migration dependencies. Local SDK calls
   open no database or server. Extract one shared media implementation and
   neutral types/errors, leaving owner/queue/quota/provider/HTTP adapters outside;
   preserve public type aliases, error identity and publication guarantees.
   Measure same-flag cold builds, peak build RAM and executable size before
   claiming a benefit. The existing embedding witness grew from 8,678,702 to
   8,846,344 bytes (1.9%); this does not measure package-split savings.
8. Consider an additive, closeable source handle in `pkg/engine`. Each current
   export opens its own session; a separate `Inspect` does not warm a later
   export. A bounded handle could reuse verified probes, keyframes and subtitle
   data across calls, invalidating on file/tool changes. Today callers can
   obtain operation-scoped reuse by submitting ranges together in `ExecutePlan`.
9. Offer an optional shared SDK resource budget and bounded progress callback.
   Current thread/timeout/output limits apply per operation; embedding callers
   own aggregate admission. This would improve predictable concurrent use,
   cancellation and integration rather than establish a single-export gain.
10. Extend the direct copy-audio path only through individually proved
    codec/clock/container combinations. MP4, fine clocks and Opus cannot lose
    their staged fallback; exact packet clocks and encoder-delay behavior are
    mandatory acceptance gates for any added combination.
11. Isolate native tool startup cost before evaluating a smaller FFmpeg/FFprobe
    distribution. The production bundle has 214 runtime dependency files, but
    dependency count alone does not establish startup cost or a speedup. Compare
    the same FFmpeg version and required input/codec/filter coverage, retaining
    packet clocks, decoded output, cancellation and all supported native-platform
    gates. Packaging and security-update costs belong in that decision. No
    custom-tool-build acceleration has been measured in this pass.

Go callers already use the typed, context-aware SDK; other languages can use
the versioned CLI JSON or owner/session-protected HTTP contract. Local SDK
callers supply their own admission/isolation, and the external FFmpeg/ffprobe
dependencies remain. A native FFI rewrite is not required for these integration
paths.

## Isolated Go costs

Five alternating process pairs, each with five internal 100 ms benchmark
repeats, compare the same observation helper on Go 1.27.2. Final process medians:
32 MiB PCM peaks 62.538→13.208 ms, with identical peaks and unchanged 16 KiB
allocation. The median paired ratio is 4.924× (range 4.65–5.11×, 5/5 faster).
Clipping 10,000 already-parsed subtitle cues across 32 cuts takes
308.781→76.475 µs with identical text; its paired ratio is 4.118×
(3.87–4.48×, 5/5). Allocation rises 22,449→104,373 B/op and 421→422
allocations because of the overlap index. These exclude decoding, subtitle
parsing, extraction and publication and are not full-export timings. See
[per-process observations with units](isolated-go.csv).

## Source verification

The verified runtime source is `865e4afd072f77206ba674e7922a2d31de6d3c99`,
with broad Go/module/embedded-migration source catalog SHA-256
`3503f2bd511ce8fee94b174085fd0a151c7b787ed7e910e1561d32a3a7708e07`.
The catalog includes test files; it does not imply that every catalog file is
linked into the CLI. Subsequent report edits do not change these inputs.

All three CI workflows completed successfully: 27 successful checks and two
intentional PR skips. This includes both full race suites, all ten native source
jobs on five platforms, the entire current packet-clock compatibility matrix
including production-family FFmpeg, actual 006-to-007 self-hosting upgrade and
restore, five archived CLI consumers, archive recovery and SPDX derived from
verified executable bytes. The new fine-clock regression is a mandatory native
and Bookworm gate. See the [verification receipt](source-verification.json).

This was not a first-pass green run. One reusable Windows Python process control
failed while removing its private temporary directory, before the Go media gate.
The identical-head direct Windows job and one selective Windows rerun passed.
The underlying cleanup error remains unresolved. That partial rerun then exposed
an archive-attempt mismatch; one coherent package-producer regeneration and its
archive/recovery/gate descendants passed with the strict attempt fences intact.
Original failures, retries and artifact identities remain in the receipt.

## Native tool failure and final cohort

The first post-clock-guard full attempt, v6, stopped in the **baseline** HLS
witness. Its FFmpeg main thread remained in `sch_wait`, while all eleven native
threads waited on futexes. The owned helper reached its 170-second context
deadline before the outer 180-second watchdog. Observation attached to the
already stalled process, so this is failure evidence rather than an ordinary
uninstrumented latency sample. The witness has no SQL, worker or preparation
pipeline. Its exact native wait dependency, and any connection to the earlier
v4 P1 stall, remain unproved. See the [captured evidence](baseline-native-stall.json).

All v6 timings, including completed cases and pure benchmarks, are excluded.
The [v7 registration](generation-master-retry.json) declared exactly one fresh complete
attempt with the same Go catalog, CLI/helpers, media tools, corpus, case order,
pair counts and independent oracles. It permits no sample replacement, partial
continuation or second same-tools retry. All 65 main cases completed with 494
operations and 3,186 independently verified files. The run then stopped before
launching a four-owner candidate server sample: the free-space preflight was
27,267,072 bytes short of its unchanged 3 GiB reserve plus 512 MiB sample margin.
Successful cases had cleaned their media correctly; the retained unmatched
baseline sample and insufficient initial capacity caused this stop. No media
equivalence failure was observed in this attempt. See the
[capacity failure receipt](master-retry-capacity-failure.json).

All v7 timings are excluded from the final cohort as required by that
registration. Its completed accurate 20-cut join nevertheless remains visible
as historical adverse evidence: the candidate was slower in all five pairs,
with median wall time 2.97% higher and CPU 4.06% higher, despite byte-identical
outputs and unchanged tool counts. No cause is established by those observations;
see the [paired records](accurate-join-regression.json).

The completed final cohort uses the exact FFmpeg 5.1.9 code and library bytes copied
from the production image. The portable local executable headers relocate its
private loader and libraries; both revisions use those same relocated tools.
This is a new, explicitly rebound protocol, not a continuation of v7 or evidence
that changing FFmpeg resolves the native stall. The Go catalog and existing
compiled CLI/helper bytes remain unchanged. Fresh baseline/candidate
[media qualification](media-qualification.json) and the initial capacity check
passed before timing. All seven stages completed in the one registered attempt,
without sample replacement, continuation, diagnostic capture/taint or timeout.
All pre-guard v5 measurements and failed attempts remain historical evidence;
they are not relabelled as the final candidate. The
[toolchain proof](media-toolchain.json) records the original image and relocated
executables separately. The [build receipt](build-provenance.json),
[corpus](corpus.json), [clock-regression proof](clock-regression-proof.json) and
[retention ledger](evidence-retention.json) preserve their distinct provenance.
Raw observations, fingerprints and failure evidence stay outside Git; successful
media is removed only after the registered verification/retention checks.
