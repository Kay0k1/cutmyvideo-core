# First downloaded draft: benchmark evidence index

**M7 passed its registered controlled benchmark gates.** The complete
[formal-m7-v1 report](formal-m7-v1/README.md) and
[lossless summary](formal-m7-v1/summary.json) retain one 24-run cohort, all 12
matched pairs and independent timing, memory and content/provenance checks.
Earlier unsuccessful cohorts and the adverse repeat-download observation remain
separate. This is benchmark acceptance, not signed-release or production readiness.

The table shows medians of matched candidate/baseline ratios; lower is better.
It does not assert that every individual export became faster. All raw rows,
including adverse pairs, remain in the report.

| Stratum | First draft | All 15 drafts | Final phase | Whole 30-job cycle | Common-parent peak |
|---|---:|---:|---:|---:|---:|
| ready-fresh-process | 0.257745 | 0.820415 | 0.797100 | 0.811803 | 0.936326 |
| ready-primed-process | 0.278291 | 0.803490 | 0.791114 | 0.799709 | 0.933166 |

The measured M7 candidate is commit
`4531d7e2b63cf81421ca48da1d1f216b4e53bd00` (`perf-first-draft-M7`), compared with
released core `0a5d6cd0fd7257294a39806b23af63b05e91ac88` (1.4.0). The later
CI-only commit `70c6cb6e4ffcb89f2f39a7968770a4b229b0ad39` increases the installed-SDK
HTTP job's time allowance; it changes neither the measured application nor SDK
runtime. CI, signed-release verification and production checks are separate
requirements, not outcomes established by this index.

## Fixture and exact-byte scope

Recorded attribution is in [source.json](source.json):
[Interview WMF.ogv](https://commons.wikimedia.org/wiki/File:Interview_WMF.ogv),
Wikimedia Deutschland (WMDE),
[CC BY-SA 3.0](https://creativecommons.org/licenses/by-sa/3.0).
The original OGV is 60,337,298 bytes, SHA256
`cf63145d228d669e76934686d80590ac5848c43a6a3361817a0ae2fa7e599c14`.
The prepared interview is 82,242,740 bytes, SHA256
`0f9e167eea8a4c503396cf819edd5e356c94858befef12a8e3667cea22d27fca`.
Its recorded conversion is Theora/Vorbis to H.264/AAC, CRF20/veryfast, retaining
1280×720 geometry and variable frame timing, without cuts or synthetic speech.

The prepared file's exact bytes define this comparison. This public packet does
not contain a complete pinned preparation argv/toolchain receipt, so it does not
claim byte-for-byte regeneration of that MP4 from the original OGV. Reproduction
requires the matching prepared-file identity; a fresh conversion is not assumed
equivalent. Media files are not redistributed in this repository.

[requests.json](requests.json), SHA256
`686005efd644b86a0ddf41442458c10b223e58b5c1950c59915bdd9c3fc90ae5`,
contains the ordered plan and earlier functional variants. The timed driver uses
**`initial.plan`**, all 15 clips with two ordered ranges totalling 9 seconds each
(135 seconds per rendition), including the fixed crop. It submits all 15 drafts
without `clip_ids`, then all 15 finals: 30 jobs, artifacts and completed downloads.
The fixture's separate `draft`/`revised` examples are not the timed selection.
The same authenticated released 1.4.0 SDK and media tools serve both sides.

## Registered M7 scope

M7 uses V6 registration SHA256
`a95b0a5ff7dc409d936b7fdb66ce2c8f65836307ff48b204b595a9675d33872c`:
24 isolated runs, six matched alternating baseline/candidate pairs in each of
`ready-fresh-process` and `ready-primed-process`. Priming is exactly one untimed
full-15-draft validation, without encoding or output reuse. There is no cold OS
or page-cache claim. Ready-source preparation and that validation prime precede
the primary clocks but remain within the measured lifetime kernel peaks.

The first-draft clock starts before the full collection SDK invocation and ends
only after a complete downloaded output is independently SHA-verified in its
callback. The same run continues through all drafts and all finals. Upload,
transcription and moment selection are outside the primary latency claim.

In **each** stratum, acceptance requires a first-draft matched-median
candidate/baseline ratio at most 0.80 and at least five of six faster pairs;
all-draft, final-phase and whole-30-cycle matched medians at most 1.00; and actual
common-parent kernel peak matched median at most 0.98 with no higher candidate
maximum. Original caps, admission/poll rules, no-OOM checks and the full workload
remain fixed. All five parent/API/worker/PostgreSQL/client peak observations,
matched medians, maxima and original zero-increase outcomes remain visible as
diagnostics. Independently timed role high-water marks are not added together.

The [explicit post-M2 protocol correction](protocol-correction-v6.json) preserves
M2's rejection under its original rules. The predeclared negative-only futility
rule may stop after all six fresh pairs and 12 valid stopped samples if a V6
acceptance gate fails. It never permits positive acceptance at 12, sample
replacement, resumption or pooling with another cohort.

After timing, the quality runner freshly hashed all 720 target outputs,
checked all 720 current request/map/track manifests and rehashed the files at its
final boundary. It reused 720 prior native-qualified output certificates and
performed **zero new complete output decodes**, with no current-run certificate
reuse. All 180 logical final comparisons passed with **180/180 encoded whole-file
SHA256 matches**; the 15-final baseline-repeat qualification also passed.
Certificates bind identical bytes, source intent, tools and provenance. These are
720 qualified-output reuses, not 720 new native decode executions. The independent
report binds this quality evidence to all 24 actual timing/backend receipts.

## Retained results and reproduction tools

| Packet | Status and boundary |
|---|---|
| [formal-m7-v1](formal-m7-v1/README.md) | Complete 24-run controlled benchmark and independent qualification passed; release and production readiness remain separate. |
| [M2 negative result](rejected-m2/README.md) | Faster drafts observed; original strict memory gates failed. |
| [Interrupted M6 primary-v1](interrupted-m6-v1/README.md) | Infrastructure failure after three admissible rows; incomplete, never pooled. |
| [M6 primary-v2 negative result](rejected-m6-primary-v2/README.md) | Six fresh pairs retained; registered 2% memory-improvement margin missed. |
| [Repeated-download observation](supplemental-repeat-download/README.md) | All six synthetic observations retained, including slower draft-path I/O; no export, RAM, p50-request or SLA claim. |
| [Frozen original tools and methods](../tools/first-draft-v6/) | Preserved source/guard/control records, including historical V4/V5 and prospective V6; source presence is not measured acceptance. |
| [Backend lifecycle supplement](tools-infra-v2/README.md) | Separately frozen infrastructure repair used by a new registration; earlier failed evidence stays unchanged. |

The [V6 controller method](../tools/first-draft-v6/controller-v6-method.md),
[quality method](../tools/first-draft-v6/quality-reuse-v6-method.md) and
[independent report method](../tools/first-draft-v6/render-v6-report-method.md) describe the checks.
The historical `quality-reuse-method.md` cited by the V6 note is not included in this packet; the [base](../tools/first-draft-v6/quality-reuse.py) and [V6](../tools/first-draft-v6/quality-reuse-v6.py) implementations are included.
Their original preparation-time wording and frozen hashes are retained; the
linked cohort report establishes the actual benchmark execution. Private owner/session
state, operational resource identities, production configuration and media are
excluded from the published evidence.

The unchanged source bundle is stored in the shared benchmark-tools directory,
separately from dated diagnostic payloads. Historical frozen notes may refer to
its earlier `tools/` location; the linked bundle retains all 33 files (32
inventoried payloads plus their exact SHA256 inventory). The dated-report artifact-size policy is unchanged.
