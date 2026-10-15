# 1.4.0 candidate: resumable SDK collections

This candidate completes the Python and Node collection workflow: supplied
source/timecodes become verified local files with saved progress. An agent can
resume a killed process, retain ready clips, revise selected clips and produce
separate draft/final outputs. See [the complete workflow](sdk-collections.md).

The runner validates before submission, bounds outstanding work, honors retry
delays and recovers uncertain submissions using the original owner, key and exact
body. Downloads retain synchronized prefixes, verify genuine ranged responses
and acknowledge exclusively published files with artifact IDs and SHA256.
Prior revisions count toward the output budget and remain available. Private
state/credentials stay separate from public progress and results; conflicting
scope and modified files are refused.

Both SDKs expose low-level collection methods and local CLI collection wrappers.
The browser TypeScript entry remains free of Node imports; durable recovery lives
in `@cutmyvideo/sdk/collections`. Neither SDK has runtime dependencies. Source
upload/import remains explicit and is never automatically retried. Durable local
CLI recovery is outside this candidate.

Required installed-package acceptance uses the actual API, disposable PostgreSQL
and worker: 15 clips, a lost committed submission reply, an interrupted download,
OS process kills at a saved prefix and eight completed files, fresh-process
resume, unchanged replay, two revised clips and two selected drafts. It requires
exactly 15, 17 and 19 render attempts, independently compares downloaded backend
bytes, fully decodes media and checks geometry, ranges, selected tracks and hashes.
An opt-in real interview run uses the same consumers and retains receipts and
verified media outside Git. CI uses generated media only.

[The real-interview acceptance](../verification/2026-10-15-sdk-collections/README.md)
records both final installed consumers, frozen inputs and independent frame/audio
comparisons. Supplied timecodes are used; no semantic moment ranking is claimed.

No production Go behavior, HTTP schema or migration changes are needed: the runner
uses the core 1.3.0 API. Python `1.4.0.dev0` and TypeScript `1.4.0-dev.0` remain
unpublished development packages. This is release preparation, not a published
tag, package registry release or deployment. The latest published core is 1.3.0.

Release preparation requires full source checks, installed SDK/native checks on
all five supported targets, archive/recovery preparation and the existing public
self-hosting gate. After review and merge, verify the actual main commit before
tagging. Final tagged archives require signing and downloaded-consumer acceptance
before publication. Follow [the release protocol](releases.md).
