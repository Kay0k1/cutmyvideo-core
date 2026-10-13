# Core performance protocol

`core-performance.py freeze` prepares a stream-copy hour recording and a small
HLS fixture from already verified local acceptance media. It records the source
parent hashes, complete generation commands, independent probes, tool hashes,
baseline binary hash/version and a finite workload before timing starts. The
exclusive JSON manifest and SHA-256 sidecar are operator artifacts, not reusable
production configuration. Never replace them to fit a measured result.

The registered workload contains 40 local matrix cases (1/2/10/20/32 cuts,
accurate MP4, copied MP4/MKV and MP3, separate/joined); eight high-complexity
1080p/60-fps cases; five VFR/rotation/audio/subtitle/crop cases; four public
`pkg/engine` witnesses; and eight guarded HLS cold/warm/straggler/overlap cases.
Ranges preserve requested order, overlaps and repeated moments. Six separate
server scenarios register 1/4 owners, four jobs per owner, two cuts per job and
accurate MP4/copy MP4/MP3 with concurrency two. Server orchestration uses its own
disposable database and cgroup observations and is not inferred from CLI data.

Run only during a coordinator-granted idle window, with no overlapping tests,
builds, other encodes or live production work. The runner requires an idle-window
identifier and locks its own evidence directory. Three alternating AB/BA pairs
are exploratory; final key cases need at least five pairs. Existing OS caches
remain warm; operation workspaces are fresh. Do not label this disk-cache-cold.

`register-pairs` writes a separate, exclusive preregistration supplement without
changing the original manifest. `run --protocol-pairs --pair-supplement FILE`
executes all 65 registered cases exactly once with their required cohorts: five
pairs for each of the 26 frozen key cases, three pairs for the other 39, totaling
494 timed operations. It rejects a supplement that reduces a key cohort or changes
case membership. Every output is still independently probed and fully decoded;
there is no verification memoization. This avoids rerunning exploratory cohorts
as part of a second separate final-key pass.

Each process records wall time, waited CPU, waited maximum RSS, block I/O counts,
faults, sampled process-tree RSS/I/O and sampled media workspace bytes. Tool
wrappers count actual FFmpeg/FFprobe invocations without recording arguments or
environment. Waited maximum RSS is the largest process peak, not simultaneous
tree memory. Sampled tree RSS may count shared pages twice; peaks and very short
processes can be missed. Sampled I/O is a lower bound. Block counters are counts,
not transfer bytes. Verification executes after each timed process using the real
tools, so its subprocesses and costs are excluded from the measured process.

Every output is independently probed and fully decoded; DTS ordering, packet
payload hashes, decoded video/audio hashes, subtitles, output order and reported
source/output bounds are retained. Baseline and candidate semantic fingerprints
must agree. Container bytes/sizes are recorded separately. Verified owned media
is deleted immediately; failed runs retain their evidence. At least 3 GiB of
filesystem space remains free, with a 512 MiB allocation margin per sample.
Per-stream packet-clock hashes retain PTS, DTS and duration ticks, the stream
time base, packet count, and missing/unknown values. They must agree for both
copied and accurate outputs, independently of compressed packet payloads. Unknown
DTS is not substituted with reordered PTS or zero during the monotonicity check.
Each timed operation has a 180-second deadline and a five-second TERM grace,
followed by KILL of the original process group and identity-checked, observed
descendant group leaders. The parent is reaped and a `timeout.json` failure
receipt records the deadline, signals and exit status. Server role cgroups have
an additional runner-owned cleanup fence.

The public embedding witness builds the exact same Go source from each module
checkout. Singletons call `ExportWithOptions`; larger batches call `ExecutePlan`.
The HLS witness builds through a Go overlay with the exact same helper source.
Its separately recorded adapter is a no-op on the baseline and enters
`withHLSDownloadCache(ctx, work, 32<<20)` once per operation on the candidate.
HTTPS `media.example` requests resolve only through a fixed test transport;
production URI parsing, byte budgets, segment selection, staging, probing and
export paths remain in use. The fixture transport never contacts real hosts.
Warm HLS phase time excludes prefill; outer process CPU/RSS/I/O include prefill,
so those scopes must not be mixed in comparisons.

Example commands (replace paths with task-owned artifacts):

```sh
python3 scripts/core-performance.py freeze \
  --corpus /task/cache/corpus --manifest /task/evidence/frozen.json \
  --baseline /task/evidence/baseline-cutmy --baseline-revision BASELINE_REVISION \
  --acceptance /task/cache/acceptance --complex /task/cache/source-1080p60.mp4
python3 scripts/core-performance.py run \
  --manifest /task/evidence/frozen.json --candidate /task/evidence/candidate-cutmy \
  --candidate-revision CANDIDATE_REVISION --output /task/evidence/local-matrix \
  --kind cli --pairs 3 --idle-window COORDINATOR_WINDOW
python3 scripts/core-performance.py register-pairs \
  --manifest /task/evidence/frozen.json --output /task/evidence/pairs.json
python3 scripts/core-performance.py run \
  --manifest /task/evidence/frozen.json --candidate /task/evidence/candidate-cutmy \
  --candidate-revision CANDIDATE_REVISION --output /task/evidence/all-65 \
  --protocol-pairs --pair-supplement /task/evidence/pairs.json \
  --baseline-engine /task/evidence/baseline-engine \
  --candidate-engine /task/evidence/candidate-engine \
  --baseline-hls /task/evidence/baseline-hls.test \
  --candidate-hls /task/evidence/candidate-hls.test \
  --idle-window COORDINATOR_WINDOW
```

The optional `build-pure-witnesses` command builds identical test overlays for
isolated Go costs, with binary/toolchain/helper/checkout source provenance. It
does not execute them. `TestCorePerformancePureWitness` compares 32 MiB of fixed
PCM input against an independent integer-sample aggregate and emits identical
PCM/subtitle output hashes to check both versions. The PCM benchmark uses two
channels, 44100 Hz, 2000 bins, and 64 KiB writes; its allocation is part of each
operation while fixture generation is excluded. The subtitle benchmark rebases
10000 sorted, overlapping cues across 32 fixed 850 ms cuts. It includes one
index construction per operation on implementations that build an index; it does
not measure text parsing, a retained track cache, media decoding or whole exports.
Run each benchmark in five alternating AB/BA process pairs using
`-test.benchtime=100ms -test.count=5 -test.cpu=1`. This retains five repeated
observations within each process and five independent ordered blocks per side;
do not treat all within-process observations as independent pairs. Keep this
microbenchmark scope separate from
the 65 media scenarios and six server scenarios.

Record helper/adapter hashes and exact build commands alongside the manifest.
Optional helpers are required for their scenario kind; unavailable helpers
cannot silently skip registered work. Preserve all raw per-pair measurements,
including regressions, and report the corpus/configuration/scope with results.
This protocol provides no production SLA, arbitrary-media guarantee or competitor
comparison.
