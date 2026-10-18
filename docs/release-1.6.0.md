# 1.6.0 scope and verification

The release adds controlled delivery to the existing media engine:

| Operation | Scope |
| --- | --- |
| Audio formats | PCM24 WAV, FLAC24 and explicit compatible AAC-to-M4A packet copy |
| Constant speed | Rational 0.5–2×, preserved pitch, actual source/output clock maps |
| Target size | Bounded two-pass final H.264 MP4 with a verified per-file byte ceiling |
| Output poster | One inspected JPEG/PNG at an explicit finished-output timestamp |

[Usage and limitations](controlled-export.md) cover Go, CLI, HTTP and both SDKs.
Plans and collections retain the new options and explicit inheritance resets.
Legacy exports keep their defaults. Source crop, thumbnails and collections
already existed and are not counted as new features. Watermarks, multiple-source
editing, transitions and new browser controls are outside this release.

Schema 10 adds separate image artifacts and artifact-reader leases. Take a
coordinated database/media backup with writers stopped before upgrade.
[The upgrade guide](poster-upgrade.md) describes the schema-9 rollback boundary.
Local engine and CLI execution do not require PostgreSQL.

Acceptance requires actual CLI output and installed Python/TypeScript package
consumers on the five native targets. The new installed delivery workflows
independently inspect and fully decode output media, compare PCM24 samples and
AAC packet payloads, verify pitch and exact reordered/repeated output clocks,
opt into version-2 affine subtitle mapping while preserving version-1 results,
enforce file-size ceilings and check output-poster pixels. HTTP checks use a
real disposable database and worker, canonical idempotent request replay,
portable roundtrips and collection inheritance/reset/durable audio downloads.
Image ownership, parent leases, quotas, expiry and deletion need their own
service tests; pure SDK response tests do not replace these observations.

Historical mandatory native, archive, SDK, API and public-hosting checks remain
required. The two pinned compatibility snapshots remain unchanged; a narrowly
reviewed caller-selected format-echo extension is documented in
[HTTP compatibility](../api/COMPATIBILITY.md).

Publication requires signed CLI/SDK assets and independent consumers of the
same immutable release bytes. SDK 1.6 acceptance adds both installed delivery
workflows; signed 1.5 and earlier contracts retain their original test sets.
Packages remain GitHub release assets. PyPI/npm registry publication is separate.
