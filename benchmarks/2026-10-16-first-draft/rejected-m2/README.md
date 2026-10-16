# M2: faster first drafts, failed memory acceptance

This candidate did **not** qualify for release. Baseline is released core
`0a5d6cd0fd7257294a39806b23af63b05e91ac88` (1.4.0); candidate is
`5f3014be8b5f9253677d97b5f5646e6b45e1f9a0`. Both use the same released
1.4.0 Python SDK, exact interview and ordered 15-clip plan, native media tools,
encoding settings, service quotas and admission limits. Each complete timed run
finishes all 15 drafts and then all 15 finals. First-output time includes the
complete download and independent file checksum, following source preparation.

Six balanced fresh-process pairs completed. Median matched candidate/baseline
ratios are 0.287203 for first draft, 0.855958 for all drafts, 0.855436 for final
phase and 0.853159 for the complete draft-to-final cycle. First draft was faster
in all six pairs. These are observations from a rejected candidate, not release
acceptance or a general latency guarantee.

Actual kernel peak memory failed the registered zero-increase gates: matched
medians rose 0.7174% for the common workload parent, 1.1608% for the worker and
1.9447% for PostgreSQL. The maximum-peak guards also failed for those three
scopes and the SDK client. The client maximum was 108,736,512 B versus
108,679,168 B. API and client matched memory medians passed. Endpoint memory
snapshots do not establish the cause of any peak difference.

The [independent witness](fresh-futility-witness.json) retains all 12 measured
rows, six matched ratios, actual per-scope peaks, original gate decisions and
receipt digests. Every complete run independently passed database counts for
30 successful jobs/artifacts, configured resource bounds, no host OOM change,
unchanged production identity and owned-resource cleanup. Source correctness
and complete decoded-content acceptance for this cohort remain pending; the
separate pilot's successful content checks do not qualify this incomplete
cohort.

The original registration was frozen on 2026-10-16 before this series, SHA-256
`8f408fee2296afeba50a7aa48fc4cca2c031acbb467c746fdca4d9547d75a7e7`.
It requires 24 runs across fresh and primed process strata. A **post-registration,
negative-only** futility amendment was adopted after five runs were known,
SHA-256 `fbb50bfd6a94ea97fb7f53cd6e1d0ede92c8aa2251caf5706bd5072409690a45`.
It permits stopping only after all six valid fresh pairs are complete and an
independent recomputation fails at least one original fresh-stratum gate; it
never permits positive early acceptance.

After the complete fresh-stratum negative witness, the coordinator stopped the
exact controller with SIGINT at 11:15:24 UTC. The already started first primed
sample and all failures remain retained privately; its unfinished observations
are not counted as a valid pair. Automatic cleanup removed its containers and
network but failed its ownership check for an already stopped, garbage-collected
client unit. Separate positive checks confirmed that unit and its cgroup absent,
then stopped only the owned anchor and empty slice. The original failure receipt
was preserved. The isolated window closed at 11:16 UTC with production unchanged.

No remaining primed pairs were run, and no result is pooled with another
candidate or with later instrumented diagnostics. A subsequent candidate needs
its own separately frozen, complete acceptance series and content verification.
