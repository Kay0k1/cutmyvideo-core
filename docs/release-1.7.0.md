# 1.7.0 scope and verification

Four local operations let a person, a script or an agent place and check media
without playing it:

| Operation | Result |
| --- | --- |
| `cutmy sync` | Offset and measured speed of a reference recording in up to 16 inputs, one placement per edited part; `ambiguous` or `no_match` instead of an untrustworthy offset |
| `cutmy analyze repeats` | Material that returns later in one recording |
| `cutmy sheet` | Labelled contact sheets of recordings or images with a JSON index from every cell to its source time; luma SSIM per pair when two recordings are compared |
| `cutmy qc` | Frame count and timing, drops, black/frozen picture, loudness, clipping, audio/video offsets and MP4 fast start of a finished file; `--expect` failures exit 1 with `expectation_failed` |

[Audio alignment](sync.md), [contact sheets](sheet.md) and
[finished-file checks](qc.md) describe flags, results and limits. Go exposes
`SyncAudio`, `AnalyzeRepeats`, `GenerateSheet` and `QC` with error codes
`no_match` and `expectation_failed`. None of them writes media; `sheet` writes
image pages.

## Fixes

- A media read aborted while its reader lease was being committed left the
  lease for up to 60 seconds, so `DELETE /sources/{id}` answered 409
  `source_in_use` without an active export. The lease release takes the
  storage fence first, as preview and poster admissions do; the 409 message
  names both exports and reads.
- Track inspection waits for an active poster decoder instead of exceeding the
  shared decoder pool.
- A collection clip releases its audio-replacement binding on every error path.
- The Caddy example sends HSTS, `nosniff` and referrer-policy headers.
- Builds and releases use Go 1.27.4 (GO-2026-6809 in `crypto/x509`,
  GO-2026-6859 in `net/http`).

## Compatibility and known limitations

The `/api/v1` contract, SDK methods, durable plan/project formats and database
schema 10 are unchanged; no migration or coordinated upgrade is required. The
OpenAPI document keeps version 1.6.0. SDK packages carry version 1.7.0.

The four operations run only through the CLI and Go library. HTTP routes, SDK
methods and browser controls are not part of this release. `sync` needs three
seconds of shared material with audible attacks, reports speed only between
0.98 and 1.02 and analyses 30 minutes per window; `qc` measures at most 30
minutes per request; a sheet is limited to 2,048 cells and 256 pages. `qc` and
`sheet` requests beyond a limit fail with `output_limit`; nothing is truncated.

## Verification

The new operations are tested with synthetic media and real FFmpeg/ffprobe:
`pkg/engine`, `cmd/cutmy` and `internal/localmedia` in the native media job on
all five targets, `internal/audiosync` and `internal/mediaqc` in the Linux full
check. The reader-lease fix has a PostgreSQL regression test in
`internal/app`.

Release acceptance follows the 1.6.0 policy without new tiers: native source
and packaged CLI checks on five targets, installed Python/TypeScript consumers
including the delivery workflows, production-family media, historical
upgrade/restore and public self-hosting. Publication requires signed CLI/SDK
assets and consumers of the same immutable release bytes; see the
[release procedure](releases.md). Packages remain GitHub release assets;
PyPI/npm publication is separate.
