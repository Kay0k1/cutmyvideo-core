# 1.5.0 scope and verification

Six explicit operations follow the 1.4.2 maintenance release.
[Usage and limits](media-analysis-and-rendering.md) describe CLI, plan and HTTP behavior.

| Operation | Result |
| --- | --- |
| Silence analysis | Detected pauses and an editable existing-format cut plan |
| Loudness | LUFS/true-peak/LRA report and measured two-pass normalization |
| Canvas | Explicit portrait/square/landscape dimensions, contain and solid padding |
| Cut-list interchange | CSV and FFmetadata import/export; actual output segment clocks |
| Visual analysis | Scene-change scores and black intervals on the source timeline |
| External audio | Owned uploaded/local replacement, track and source-clock offset |

Go, CLI, HTTP and both SDKs expose these operations; existing plans keep their
defaults. No transcription, semantic editing, subtitle feature or browser
control is included. SDK packages remain GitHub release assets; npm/PyPI
publication is a separate operation.

Schema 9 tracks primary and replacement sources of an accepted job. Admission
checks both in one transaction; retention, active-job protection, reader leases
and bounded counter audits cover both. Explicit deletion of a completed job's
replacement preserves its primary job and published artifacts. Active work
prevents deletion. Successful-export cleanup applies only to the primary source.

Take a coordinated database/media backup before upgrade. An older binary must
refuse schema 9 without writes. Schema 8 rollback uses its matching pre-upgrade
database/media snapshot while writers remain stopped; never rewind acknowledged
traffic after reopening the new release. See [migrations](migrations.md) and
[recovery](backup-recovery.md).

Verification requires known-boundary media fixtures, independent loudness and
decoded picture/audio observations, owner/project-grant isolation, cancellation,
admission cleanup, actual worker publication and schema upgrade/restore. All
historical mandatory native/archive/SDK/API/public-hosting checks remain required.
New observations run on five native targets and production-family FFmpeg.

Publication requires signed CLI/SDK assets, consumers of the same immutable
draft bytes on all five targets, and public download/signature verification.
[Release procedure](releases.md) documents the trust boundary and evidence.
