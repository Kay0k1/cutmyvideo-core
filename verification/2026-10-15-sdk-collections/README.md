# Resumable SDK collection acceptance

This protocol checks installed SDK packages with the actual HTTP API, isolated
PostgreSQL and worker. It covers one source, supplied timecodes and explicit
draft/final edit decisions. It makes no transcript, ranking or performance claim.
The [1.4.0 candidate](../../docs/release-1.4.0.md) adds the project runner; the
media/API behavior comes from the existing core 1.3.0 implementation.

## Fixture and scope

The opt-in real fixture is Wikimedia Deutschland's
[Interview WMF.ogv](https://commons.wikimedia.org/wiki/File:Interview_WMF.ogv),
licensed [CC BY-SA 3.0](https://creativecommons.org/licenses/by-sa/3.0).
The complete 516.912472-second interview was converted from Theora/Vorbis to
H.264/AAC MP4 at original 1280×720 geometry and variable frame timing, without
cutting or replacing speech. The normalization used libx264 CRF20/veryfast,
two encoder threads, yuv420p, AAC160k and MP4 faststart. It does not force 25fps;
nominal rate is 25/1, measured average 316225/12902.

Original: 60,337,298 bytes, SHA256
`cf63145d228d669e76934686d80590ac5848c43a6a3361817a0ae2fa7e599c14`.
Frozen MP4: 82,242,740 bytes, SHA256
`0f9e167eea8a4c503396cf819edd5e356c94858befef12a8e3667cea22d27fca`.
Downloaded recordings, derived clips, private owner/state and raw diagnostics
remain outside Git. The complete upload is independently compared with the
frozen source, and that source is checked again after execution.

Fifteen stable IDs each join a six-second answer and a three-second fragment.
Alternating clips reverse their source order; clip-03 has a half-width crop.
Two 40ms boundary changes affect clip-01/clip-15. Selected drafts contain
clip-02/clip-03, followed by return to all revised finals.

## Required observations

- Validation creates no jobs. First submission uses an actual one-slot queue
  before the worker starts, guaranteeing partial admission at any encoder speed.
  Both SDKs honor its `Retry-After: 60` across process interruption while
  continuing accepted work. Subsequent calls use normal queue limits.
- A committed successful submission loses its response. Recovery retains the
  original owner, key and exact body, with no duplicate job or render attempt.
- An actual artifact response ends halfway through its body. The consumer saves
  and synchronizes its prefix, then the harness kills the process through the OS.
  A fresh process uses that exact offset/artifact, validated `If-Range`, genuine
  206 bounds and suffix bytes. The interrupted artifact has only one full GET.
- Another OS kill occurs after exactly eight independently hashed ready files.
  Resume completes all 15 without changing those files or creating new renders.
- Unchanged replay preserves ordered receipts. Two revisions increase actual
  jobs/artifacts/render attempts from 15 to 17, and two separate drafts to 19.
  Return to finals retains all 15 revised receipts with no further work.
- Every unique downloaded file matches backend-sealed bytes. Independent full
  decoding checks duration, geometry, selected audio, maps and SHA256 receipts.

The independent source-content oracle derives sample times and crop from the
caller's original requests, not SDK maps. It compares two frames and one speech
sample per source segment, with fixed RGB mean-error limit12 and audio correlation
minimum0.75. AAC alignment is allowed within ±60ms. Eight physical negative
controls reject wrong source passages, forged maps/hashes, wrong audio, changed
sources and selection errors; unavailable media tools fail instead of skipping.

## Reproduce

Use the pinned tools and a disposable database from
[CONTRIBUTING](../../CONTRIBUTING.md). Build/install the candidate outside the
checkout using [check-sdks.py](../../scripts/check-sdks.py):

```sh
python scripts/check-sdks.py --http --output /tmp/new-sdk-evidence
```

The script requires all local/HTTP consumer subtests for both languages. Its
receipt binds SDK/acceptance input hashes before and after verification; changed
inputs, missing tests, failed tests and skipped required consumers cannot pass.
CI runs generated fixtures only and checks installed local consumers on all five
supported native targets.

The Python local consumer forces a legacy Windows console encoding (`cp1252`)
even on UTF-8 hosts. Its JSON report must remain parseable with Cyrillic labels;
ASCII escaping preserves those labels when the report is decoded. This catches
reporting failures independently of successful media rendering.

The running-job deadline control interrupts at a durable output callback while
another job is still running. It requires the saved deadline to be in the future
before fresh-client resume and a measured request interval of at least 2950ms
for a configured 3000ms interval. Removing the persisted-deadline guard must fail
this check.

For the real run, set `CUTMY_REQUIRE_SDK=1`, `CUTMY_SDK_PYTHON` to the installed
consumer Python, `CUTMY_SDK_NODE` to Node, `CUTMY_SDK_BINARY` to its built CLI,
and `CUTMY_SDK_JS_ENTRY`, `CUTMY_SDK_LOCAL_JS_ENTRY`,
`CUTMY_SDK_COLLECTION_JS_ENTRY` to installed `dist/index.js`, `local.js`,
`collections.js`. Set `CUTMY_SDK_COLLECTION_INTERVIEW` to the exact frozen MP4
and `CUTMY_SDK_COLLECTION_EVIDENCE` to a new private directory outside Git:

```sh
go test -json -race -count=1 -timeout=15m ./internal/app \
  -run '^TestInstalledSDKCollectionHTTPWorkflow$'
```

For each language, run
[the independent verifier](../../scripts/sdk-collection-acceptance-verify.py)
with `--source`, `--expected-source-sha256`, the retained `--requests` and
`--receipt`, and a new external `--output` file. Retained receipts contain public
metadata; share media with its source attribution/license. Private project state
and diagnostics are not release assets.

The [completed bounded summary](summary.json) records the final installed packages
and both real-interview consumers: 45 Python tests, 49 TypeScript tests, eight
physical controls and all required installed Go consumers passed. Each real
consumer has 19 unique artifacts, 76 frame comparisons (maximum mean error0.7648)
and 38 speech comparisons (minimum correlation0.8892). Python/TypeScript media
hashes agree, and only the two revised clip hashes change. This protocol
does not substitute for native release gates or the signed tagged/downloaded
consumer checks required before publication.

The media record retains its original 43-input snapshot. A separate installed
49-test verification binds the final test-only update to the deadline control;
the other 42 inputs and installed SDK payloads match the media run. The summary
records this delta and the final input digest separately.
