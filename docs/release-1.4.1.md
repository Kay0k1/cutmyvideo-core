# 1.4.1: first drafts from interview collections

HTTP collection validation and submission now inspect a ready local source once per
request and query each required FFmpeg capability table at most once. A fresh
request performs fresh checks. Source and configured-tool file identities are
verified around reuse; cancelled requests, changed files and per-clip diagnostics
retain their existing behavior. The batch remains bounded to 128 clips.

Every pending clip is checked before any new job is admitted. Retained jobs
recover without media reinspection; remote sources retain deferred checks. Range order,
audio selection, crop, geometry and explicit deferred checks stay per clip.
This is request-local sharing, with no persistent capability cache. Single-job
preflight remains fresh, and worker export settings and output verification
are preserved.

On Linux, native media tools receive a trusted glibc arena bound derived from
the configured Go CPU budget (between 2 and 32). The environment still excludes
service credentials, loader overrides and caller-provided allocator settings.
This changes neither codec arguments nor resource limits; other allocators may
ignore the bound. It does not establish a memory improvement on every platform.

Accurate joined exports use one video-input decoder thread when the normalized
codec budget is one or two threads. Encoder and filter budgets stay unchanged,
as do explicit larger thread budgets and separate audio inputs. MP3, stream copy,
single-range exports and probes keep their existing thread policy. Native tests
render and fully decode the supported cases while checking the actual arguments.

Database pools retire released idle connections after 5 seconds with a 1-second
health check by default. Explicit connection-string settings override each default
independently. Capacity and minimums are preserved; acquired transactions and the
worker's separate notification connection remain outside idle retirement.

On Linux, a fully written draft MP4 attachment can release its file pages from
the kernel cache after an error-checked response flush. Classification uses the
original job request and the existing artifact owner check. Partial downloads,
cancelled or failed responses, final artifacts, source media and ZIP transfers
retain their existing transfer behavior. Other operating systems keep the original
download path. The file remains on disk for continuation and later download.
This is an advisory cache policy: repeated or concurrent readers may incur
additional disk reads, and the kernel need not reclaim every page.
A [separate synthetic repeated-download check](../benchmarks/2026-10-16-first-draft/supplemental-repeat-download/README.md)
records that cost: the three observed per-transfer averages for a 4 MiB payload
had medians of 6.10 ms on the ordinary path and 15.76 ms on the draft path.
These sequential, order-unbalanced observations exclude authorization and SQL;
they are neither an export result nor a download latency guarantee.

The supported workflow is an already prepared interview source followed by one
SDK collection run for all 15 drafts, receipt of the first complete file, and
completion of all 15 drafts and 15 finals in the same collection. Source upload,
transcription and selection of interesting moments are separate operations.
Existing collection continuation, project review, supplied subtitle sidecars and
signed SDK packages from [1.4.0](release-1.4.0.md) remain available.
The 1.4.1 SDK packages retain that client behavior and update release metadata.

## Validation and release boundaries

Native gates require request freshness, independent clip diagnostics, cancellation,
bounded capability retention and changed-source/tool checks on all five targets.
The native HTTP gate checks that a late invalid clip admits no earlier jobs.
Bookworm media gates exercise the batch checks with production-family tools;
native gates also execute real subprocess-environment isolation checks.
Full historical upgrade/restoration and signed native consumers remain required.

The [registered comparison](../benchmarks/2026-10-16-first-draft/README.md) passed
all gates in 24 full runs: six alternating matched pairs in each of the fresh
and primed process strata. Both sides used the same released 1.4.0 SDK,
interview, media tools, encoding settings, quotas and admission limits.

| Matched-median improvement | Fresh process | Primed process |
| --- | ---: | ---: |
| First downloaded and independently verified draft | 74.2% | 72.2% |
| All 15 drafts | 18.0% | 19.7% |
| All 15 finals, final phase | 20.3% | 20.9% |
| Complete 30-output cycle | 18.8% | 20.0% |
| Actual common-parent kernel peak memory | 6.4% | 6.7% |

The first draft was faster in all twelve matched pairs. Each stratum also passed
the maximum aggregate-peak guard and all five original role diagnostics. The
report retains every raw clock and kernel peak; individual role high-water marks
are not added together. This measures a ready 720p interview and controlled local
downloads, excluding upload, transcription, moment selection and public-network
latency. It is not a guarantee for every source, machine or request.
Two individual fresh-process final phases and one whole cycle were slower;
those rows remain visible, and the registered time gates apply to pair medians.

Independent quality verification freshly hashed all 720 files, checked all 720
current request/maps/track manifests and rehashed every file at its final boundary.
Their bytes matched previously fully decoded native certificates, so this run
reused 720 prior qualifications and performed zero new native decodes. All 180
paired final files had identical encoded SHA256 hashes; decoded video/audio,
packet timing/payloads and geometry matched, and the 15-final baseline repeat
passed. The original certificate and tool bindings remain in the report.

The measured candidate is immutable source `4531d7e2b63cf81421ca48da1d1f216b4e53bd00`.
Later release changes retain its application and SDK runtime bytes; the CI budget
and documentation are separate from that measured image. Tagged binaries have
their own release identity and signed build metadata. Signed native consumers
and production verification are additional release gates, rather than conclusions
of the performance report.

The [earlier rejected candidate](../benchmarks/2026-10-16-first-draft/rejected-m2/README.md)
remains rejected. The [M6 comparison](../benchmarks/2026-10-16-first-draft/rejected-m6-primary-v2/README.md)
also remains unaccepted: its six fresh pairs reduced memory by 1.414% but missed
the registered 2% margin. Neither series contributes samples to a later cohort.
