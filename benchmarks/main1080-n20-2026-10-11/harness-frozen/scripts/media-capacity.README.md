# Bounded mixed-media workflow experiment

`media-capacity.py` compares verified **external archived executables**: v0.2.1 (`f2be62c42a6a0d3d7a017b40bd683596bc578b95`) and candidate `06bb74bca90a79fbf399c01cf71f7f44af186431`. It never builds cutmy, pulls an image, reuses a database, retries a failed mutation or changes production. This measures a whole-product workflow, including inspection, storage accounting and durability; it does not isolate an encoder optimization.

The completed main-only **N3 diagnostic** passed all eight matched cells: 24 measured workflows plus eight retained, excluded warm-ups; 96 exports and 32 previews received complete validation. Its exact raw evidence, frozen helper/config identities, source/archive/version receipts and independent cleanup checks remain preserved. This is diagnostic evidence, without p95, a capacity limit, a speedup claim or an SLA. Earlier unsuccessful startup/oracle attempts remain unsuccessful in their own immutable reports.

A separate **N20 proposal is prepared, not authorized or executed**. Completed N3 phase costs project roughly 1,210–1,347 seconds per cell and 10,230 seconds for the pair. The four-client estimate extrapolates from an actual three-client N3 batch and is unproven. Full sequential quality checks remain included. Proposed external guards are 1,800 seconds per cell and 14,400 seconds per pair; HTTP, upload, source and job budgets remain 120 seconds. The original pilot config and its 600/5,400-second guards remain unchanged.

## Workload and interpretation

The input is one frozen, licensed camera-scene derivative: 20 seconds of 1920×1080 SDR BT.709/CFR30 H.264 with one stereo AAC track. Each fresh owner uploads it, checks exact original bytes, gets a window preview miss and hit, submits accurate/1080p MP4, copy/best MP4 and accurate/best MP3 jobs for 2–8 seconds, downloads and validates them, then deletes the source and checks physical files and accounting. Both revisions produce **24fps previews**; accurate exports retain source CFR30.

| Cell | Profile | Worker concurrency | Planned N20 clients per closed batch | Measured N20 workflows per executable |
| --- | --- | ---: | ---: | ---: |
| F1 | fast | 1 | 1 | 20 |
| C1 | compact | 1 | 1 | 20 |
| F2 | fast | 2 | 4 | 20 |
| C2 | compact | 2 | 4 | 20 |

One full warm-up per cell is retained and excluded from descriptors. `pilot` uses three measured workflows; F2/C2 therefore contain one three-client batch. `descriptive` uses 20 workflows, with nearest-rank median and p95 plus every raw observation. These are **descriptive sample statistics without confidence intervals, capacity or SLA promises**. Four-client rounds are synchronized closed batches with an idle validation phase between rounds, rather than steady-state arrivals. Download-ready, full validated workflow and round wall time remain separate: full validated time includes waiting for the batch and earlier sequential verification/deletion.

The fixed input is CFR30 SDR/stereo, not a replacement for HDR, rotation/SAR, VFR, multi-audio or historical mono-AAC capability tests. A timeout or invalid output stays a failure and cannot become a successful slow timing. Existing synthetic 32×5ms storage progress gates remain failed; neither their source nor their results are relabeled by this experiment.

Optional UHD/long extensions require separately frozen inputs, a resource window and a separate budget decision. They are absent from the main-only N20 config. Synthetic UHD would not establish natural 4K detail; a repeated camera scene would not represent a three-hour recording.

## Fair output checks

Preview coded geometry is bound to each verified executable revision: f2 yields **854×480/SAR1280:1281**, an exact displayed 16:9; 06bb yields **852×480/SAR1:1**, whose displayed 71:40 differs by 0.15625% through even rounding. Both were observed in the completed pilot. An independent displayed-aspect check permits only bounded two-pixel rounding. Both SSIM inputs are normalized to that revision's exact output geometry and square pixels; dimensions/SAR are never accepted as a permissive union. The small preview-output difference is disclosed in workflow comparisons.

Packet copy must contain the entire requested prefix from its acknowledged source-aligned first keyframe, densely sampled at source 30Hz. The prefix endpoint is `(requested_end_ms - actual_start_ms)/1000`, not the acknowledged or container duration. All timestamps must be finite, strictly increasing and on the original 30Hz grid within 2 microseconds of ffprobe printing precision. Only a bounded post-request packet tail is allowed, ending no later than the smaller acknowledged/container end plus one frame. Tail timestamps are reported separately. Acknowledged duration must equal positive Go-style millisecond rounding of independently probed container duration, including half-millisecond ties. Accurate full-output CFR checks are unchanged.

Every output receives full decode, geometry/cadence/count and declared-bound checks, complete six-second PCM span with per-second440/880Hz stereo pulses and alignment, and source-aligned SSIM. Fixed floors are copy 0.99, accurate 0.90 and preview 0.80. Preview checks also verify acknowledged source-window headers, frame count and beginning/middle/end audio witnesses. Raw per-frame SSIM statistics, probes, media, hashes and calls are retained. SSIM is an approximate content/quality witness, not exact perceptual or frame-identity proof. Deletion must leave no source/job/artifact/ledger/reservation records, charged bytes or real media files.

## Resources and evidence

Linux/amd64, a local rootful Docker Unix socket and distinct actual cgroup-v2 paths are required. Remote contexts and Docker override variables are refused. API 0.5 + worker 2 + PostgreSQL 0.5 CPU quotas total **three CPUs** on the shared four-CPU host; memory limits are 512/1536/768MiB with swap disabled, FFmpeg threads 2, GOMAXPROCS 2 and GOMEMLIMIT 128MiB. Fresh disk-backed owned bind directories are used for media and PostgreSQL, with fsync/synchronous_commit/full_page_writes enabled. Exact immutable tooling and PostgreSQL image IDs must already exist. The same frozen FFmpeg/ffprobe bytes are used for both executables; there is no pull or fallback.

Services share one owned internal bridge and publish no host ports. Every HTTP client uses the live API's inspected private IPv4:8080, bound to exact labels, network/container IDs, sole membership and subnet. This follows [Docker bridge routing](https://docs.docker.com/engine/network/port-publishing/); internal-network host publishing can omit a usable binding, as recorded in [Moby's discussion](https://github.com/moby/moby/discussions/53256). There is no arbitrary-host/default fallback, external media request or egress/public-deployment security claim.

This is a **shared host**, without a dedicated verifier CPU. Verification uses a 0.5CPU/512MiB tool container after application media children become idle. Host fixture generation and the image can use different glibc; tool hashes/versions, image identity/ABI and repeated in-image content preflight are retained. Bounded background hosted-evidence downloads/hashing during the diagnostic are disclosed separately.

`resource_binding.jsonl` precedes sampling and records actual distinct service/container identities, start times, root PID/start ticks, cgroup paths/membership, CPU quota/period and memory/swap limits. A mismatch fails before samples. Round cgroup CPU counters include short children. RSS is sampled every 100ms by PID/start time, separating Go root and children; short peaks can be missed and summed RSS includes shared pages. Cgroup memory includes page cache and differs from RSS. Missing `memory.peak` is recorded null/unavailable; sampled `memory.current` is not a fabricated lifetime peak. OOM/oom_kill must remain zero through final liveness and stop checks. The completed pilot independently bound service cgroups; the idle verifier's requested limits were recorded in Docker calls, without a separate ephemeral-tool cgroup snapshot.

HTTP enqueue intervals and 100ms read-only persisted-lease observations provide queue lower/upper bounds, including request/query uncertainty and a claim before HTTP acknowledgement. They are not exact claim instants. Owner-list polling, preview semaphore waits and observer PostgreSQL CPU remain disclosed.

## Budgets, cleanup and running

Descriptive execution requires an externally SHA-bound explicit `execution_budget`; it fails before evidence/scratch/Docker creation without one. The current main-only proposal uses 1,800/14,400-second guards and initial free space of 14,949,123,912 bytes: 8GiB reserve plus 1.25×projected retained evidence and a separate private-work allowance. The 1.25 factor is a planning margin, not statistical confidence. Initial free space is checked on both scratch/evidence filesystems; the 8GiB remaining reserve is checked before cells and measured batches and after complete batch validation. Resource quotas, output oracles and 120-second operation budgets cannot be changed by this field. N20 still needs independent frozen-byte review and explicit run authorization.

Completed N3 retained 802,443,359 bytes. Scaling all raw bytes by 21/4 gives 4,212,827,634.75 projected bytes; a 25% allowance gives 5,266,034,544 bytes after rounding up. Private-work allowance is 1,093,154,776 bytes, twice the low-frequency observed N3 maximum; that maximum is not an exact lifetime peak. Existing failed runs, original/input proofs and image context remain preserved and already consume current free space. The separate proposal records per-cell phase/disk costs, uncertainty and the narrow current headroom; it cannot authorize cleanup of earlier evidence.

Cookies remain in memory. Environment files and PostgreSQL files are excluded from public evidence. Failed/partial owned media are retained within bounds; preservation failure retains private work. Scratch is removed only after exact-label discovery/removal confirms no remaining owned containers/networks. Cleanup refusal cannot erase mounted PostgreSQL/media and makes the run unsuccessful.

The reviewed original is [Pycnonotus goiavier by Joaquim Baeta (Spontaneous Symmetry)](https://commons.wikimedia.org/wiki/File:Pycnonotus_goiavier.webm), [CC BY4.0](https://creativecommons.org/licenses/by/4.0/): 21,473,668 bytes, SHA256 `c2ba89f5bfa1a7c69c78b04eff8de0c6fce3736e335499f76ee7dbe2006ea7b8`. The derivative selects its first 20 seconds, converts labeled SMPTE170m to BT.709/CFR30/H.264, replaces sound with pulsed stereo 440/880Hz tones and adds a 256×64 RGB witness strip without upscaling. Attribution, original-page revision, exact generation/tool identities and changes must accompany redistribution. Complete 20-second PCM input proof covers all 20 pulse windows. This single scene does not establish broad natural-media performance.

Run pure bounded controls without Docker, PostgreSQL, Go or encoding:

```sh
python3 -m unittest discover -s scripts -p media_capacity_test.py -v
```

After independent archive/image/fixture/config review **and explicit authorization**, use absolute paths and a new evidence/private scratch directory:

```sh
python3 scripts/media-capacity.py \
  --config /absolute/CONFIG-main-N20-proposed.json \
  --expected-config-sha256 <independently-reviewed-SHA256> \
  --evidence-dir /absolute/new-evidence-folder \
  --scratch-dir /absolute/owned-scratch \
  --docker /absolute/docker \
  --mode descriptive
```

Verdicts/descriptors derive from `summary.json`, exact JSONL and deterministic gzip copies, plus final `MANIFEST.json`. All failed and successful observations remain available; partial or historical runs never substitute for a fresh matched final matrix.
