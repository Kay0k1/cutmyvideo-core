# 1.4.0: recoverable agent projects and subtitle sidecars

This release connects an agent's clipping run with a browser review project.
Python and Node can prepare an interview source, create independent clips,
recover an interrupted process, repair selected failures, adopt browser edits
and retime an existing SRT/VTT transcript against the finished clips. See
[the complete SDK workflow](sdk-collections.md) and
[subtitle timing semantics](subtitle-sidecars.md).

Publication requires combined installed-package, real-interview,
native-platform and signed-release checks against the final source. Earlier
[1.4 SDK interview evidence](../verification/2026-10-15-sdk-collections/README.md)
covers the original durable collection cycle; it does not by itself verify the
new source-preparation/project/subtitle implementation.

## Agent and editor workflow

Source preparation now has an owner-scoped idempotency receipt. The SDK stores
its original owner, exact intent and key before uploading/importing. A lost ready
reply is immediately recoverable through the receipt, without another source.
An interrupted in-flight claim is recoverable only after its bounded lease:
`max(UPLOAD_TIMEOUT, SOURCE_TIMEOUT) + 1 minute`. It must not be described as
instant upload recovery. Upload retries verify saved SHA256/size and can restart
the transfer; byte-range upload resume is not promised. Provider identities for
metadata-only imports are distinguished from content hashes.

Durable collection runners continue to recover saved exact submissions, honor
admission/poll delays, resume verified artifact prefixes and preserve finished
files. A succeeded job with expired artifacts becomes an explicit
`artifact_expired` failure. `retry_failed` / `retryFailed` creates a new saved
attempt only for chosen failed, cancelled or expired clips; other verified
outputs remain intact. Bounded worker diagnostics include job IDs, stages,
progress and safe error codes. History/receipt limits refuse new state before
it becomes unreadable on the next resume.

An owner can create a server project and issue a short-lived, scoped browser
handoff. The recipient uses its own session; it never receives the agent's owner
cookie or collection key. Browser edits retain stable clip IDs and normalized
per-clip decisions. Explicit SDK project sync verifies source/request identity
and adopts matching current jobs, including repaired attempts. Revision conflicts
and unresolved submissions fail explicitly. Portable edit documents contain
decisions and optional source fingerprints, not access authority.

Supplied SRT/VTT cues are clipped, reordered and rebased using actual exported
segment maps. The pure Go operation, CLI, HTTP endpoint and both SDKs return
deterministic sidecar text with SHA256. They neither decode media nor render
captions. Sidecars are written explicitly and exclusively outside the private
media receipt journal.

## Compatibility and delivery

This release adds Go/CLI/HTTP behavior and database schema **8**, including
source-preparation receipts, agent projects, scoped grants and retry attempts.
Legacy unkeyed upload/import, job idempotency and ordinary owner routes retain
their existing behavior. Follow the migration and recovery checks in
[the release protocol](releases.md); this is no longer an SDK-only update on the
1.3.0 schema.

Python `1.4.0` and `@cutmyvideo/sdk` `1.4.0` have no runtime dependencies. The
browser main entry has no Node imports; filesystem recovery is in the Node-only
`collections` and `sources` entries. The public delivery target is the GitHub
release, with **18 assets**: the existing 13 CLI/trust files plus a wheel, Python
source archive, npm tarball, `SDK-MANIFEST.json` and
`sdk-provenance.sigstore.json`. A separate SDK attestation binds four subjects:
the three distributions and their SDK manifest. The existing CLI archive/trust
protocol remains separate. Package-registry publication is not a prerequisite
and is not claimed here.

## Verification and boundaries

Required installed consumers use the actual API, disposable PostgreSQL and
worker. The existing interruption cycle remains required: 15 clips, lost submit
reply, a real ranged download, OS kills at a saved prefix/eight ready files,
fresh-process recovery, replay, two edits and two selected drafts: 15 → 17 → 19
render attempts. Independent checks decode outputs and compare hashes, geometry,
ranges and selected tracks.

The added project acceptance requires lost source-reply recovery, 15 clips with
one real worker deadline failure, an interrupted explicit repair (16 attempts),
scoped browser/project review with two edits, SDK adoption (18 attempts total),
and 15 supplied subtitle sidecars. The real-interview run additionally exercises
browser review; CI uses generated media. Final evidence must bind the frozen
implementation and installed distributions, rather than source-tree imports.
Generated-media acceptance runs in CI; the real-interview browser run is a
separate acceptance record bound to its frozen implementation and packages.

For 1.4.0 and later, each of the five signed-download consumers also installs
the authenticated wheel and npm tarball offline into clean private environments.
It checks installed versions/public APIs and exact Unicode SRT text/SHA256 via
the authenticated native CLI with deliberately unavailable FFmpeg/ffprobe paths.
All original/snapshot package hashes and verification receipts are checked again
after execution, and private installations are removed. This consumes the
existing signature-verification result; it does not substitute a local smoke
for the actual four-subject SDK attestation.

Transcription, semantic moment ranking and music remain external to the core.
There is no burned-caption renderer, NLE timeline export or durable local CLI
runner. Local collection wrappers and pure subtitle retiming are supported;
the saved orchestration loop is HTTP-only. Scoped grants do not extend source
retention, and expired/unavailable media can still require explicit repair.
Release readiness still requires full source/native/SDK checks, migration and
public self-hosting acceptance, final-main verification, signed tagged archives
and downloaded-consumer verification before publishing/deploying.
