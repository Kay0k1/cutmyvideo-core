# Supplemental mixed pipeline proof

This is one finite controlled workload, separate from the 65 local/embedding/HLS
cases and six process-isolated server scenarios. It uses real HTTP sessions,
imports, uploads, ordered job admissions, worker claims and complete downloads.
Four distinct owners each submit one job before the worker starts: two remote
HLS jobs with four 1.1-second cuts each, followed by two uploaded 1080p60 jobs
with two six-second cuts each. Worker concurrency and FFmpeg threads remain two;
the candidate varies only preparation concurrency between zero and one.

Every fixture HTTPS request waits 250 ms before its fixed response. A reviewed
Go overlay replaces the guarded client's transport and supplies validated
synthetic extractor JSON for exactly two known page URLs. URI checks, transfer
budgets, HLS selection/staging, exports and storage accounting remain runtime
code. The JSON fixture uses `extractor_key`, matching `platformInfo`. Registration
v2 corrects that fixture field before any build or execution; the earlier draft
registration remains preserved and must not be used.

Three baseline P0 reference operations run first. Five alternating candidate
P0/P1 process pairs follow. All twelve outputs per operation are independently
probed and fully decoded, including exact packet clocks, then compared against
the baseline. Verified outputs are deleted; failures keep their media/evidence.
A native tool wrapper records process start/end bounds without arguments. Each
operation must observe at most two simultaneous FFmpeg/FFprobe intervals;
every P1 operation must overlap a local FFmpeg input with at least 50 ms of a
remote request wait. Claimed-job samples cannot exceed two plus preparation,
and work reservations must be zero after shutdown. This proves controlled
overlap and bounded admission; report the measured P0/P1 speed ratio whether it
improves or regresses.

The API and worker have independent Stores/pools but share one process. Resource
observations cover the entire helper tree, imports/uploads/schema cleanup and
the constant wrapper overhead. The primary burst interval runs from worker
start to every completed HTTP download. It excludes setup and independent
decoding. There is no per-role cgroup or public-platform/network inference.

Build outside timed windows; all commands run from the candidate checkout:

```sh
python3 scripts/core-performance-mixed.py build \
  --manifest /root/video_cut/reports/universal-go-core-20261014/frozen-protocol.json \
  --supplement /root/video_cut/reports/universal-go-core-20261014/mixed-pipeline-supplement-v2.json \
  --baseline-root /root/video_cut/core \
  --candidate-root /root/video_cut/core-go-performance \
  --output /root/video_cut/reports/universal-go-core-20261014/mixed-witnesses
```

A binary-load smoke check lists the opt-in witness without media or database
work; the first reference operation in the full run is the functional check:

```sh
/root/video_cut/reports/universal-go-core-20261014/mixed-witnesses/baseline-mixed.test \
  -test.list '^TestCorePerformanceMixedPipelineObservation$'
/root/video_cut/reports/universal-go-core-20261014/mixed-witnesses/candidate-mixed.test \
  -test.list '^TestCorePerformanceMixedPipelineObservation$'
```

Use only the coordinator-owned disposable database, never production. Loading
its environment must not print credentials. After the coordinator grants the
runtime window:

```sh
set -a
source /root/.cache/cutmy-go-performance/database.env
set +a
python3 scripts/core-performance-mixed.py run \
  --manifest /root/video_cut/reports/universal-go-core-20261014/frozen-protocol.json \
  --supplement /root/video_cut/reports/universal-go-core-20261014/mixed-pipeline-supplement-v2.json \
  --provenance /root/video_cut/reports/universal-go-core-20261014/mixed-witnesses/provenance.json \
  --output /root/video_cut/reports/universal-go-core-20261014/mixed-pipeline \
  --idle-window COORDINATOR_WINDOW
```

Build receipts include original source hashes, embedded schema inputs, exact
overlay adaptations/config adapters, toolchain, wrapper and binary identities.
Fixture hashes come from the original frozen manifest and the separate mixed
supplement. Retain the full thirteen-operation raw report and paired results.
