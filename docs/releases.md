# CLI releases

Releases use semantic versions during 0.x development. User-visible changes belong
in [CHANGELOG](../CHANGELOG.md); `/api/v1` is the server contract. Additive fields
should preserve clients. Incompatible changes need an explicit migration note and
an appropriate version/API decision. A tag does not guarantee third-party source
availability.

Current-source library error codes, CLI JSON diagnostics/exit codes and HTTP
schemas are public contracts. Preserve known codes and successful response
fields; clients must tolerate additive fields and unknown error codes. Human
diagnostic text is not a compatibility promise. An intentional incompatible
change requires a migration note and explicit review of the
[contract baseline](../api/COMPATIBILITY.md); passing its conservative structural
checks is not proof of every semantic behavior or platform combination.

## Prepare

1. Run `make check-full` with pinned tools and isolated PostgreSQL. Verify CLI and
   server export behavior with synthetic media. Install the pinned Python
   [contract tooling](api.md#executable-contract-checks-current-source) in an
   isolated environment; full checks include actual HTTP/OpenAPI fixtures.
   Require all five [native runtime jobs](platforms.md) and run
   `make recovery-check` with disposable PostgreSQL and complete repository
   history. Database/media changes must pass real historical upgrade and
   coordinated backup restoration, with redacted evidence retained.
2. Move completed Unreleased notes under a dated heading such as
   `## 0.3.0 - 2026-10-10`, retaining Unreleased for future changes. Include known
   limitations and measured benchmark methods.
3. Commit the reviewed changes and tag that exact commit. Push the commit/tag when
   release publication is intended.

From a clean reviewed checkout:

```sh
git tag -a v0.3.0 -m 'cutmyvideo-core v0.3.0'
make release VERSION=v0.3.0
```

The script refuses dirty/untracked files, malformed versions and tags that do not
identify HEAD. It resolves an immutable commit, tree and all tracked input blobs
into `SOURCE.json`, then builds from a private materialized copy of those blobs.
Ignored or concurrently edited working files cannot become compiler input. The
original checkout is checked again before and after building. It requires the
exact `toolchain` version from go.mod (Go 1.27.2), readonly modules, disabled CGO,
trimmed paths, fixed target baselines and commit-time metadata. Caller GOFLAGS and
workspaces are disabled. It does not deploy anything.

Before a tag exists, use the same packaging path in explicit candidate mode:

```sh
candidate_sha="$(git rev-parse HEAD)"
scripts/release.sh --candidate "$candidate_sha" /tmp/cutmy-candidate
```

Candidate versions contain the full source SHA and have `publishable=false` in
both source and build manifests. Publication verification rejects them. A final
release tag must pass a new archive gate: its version changes executable bytes.
All output paths must be outside the checkout and either absent or empty.

## Artifacts and reproducibility

Five targets: Linux amd64/arm64, macOS amd64/arm64, Windows amd64. Linux/macOS use
`.tar.gz`; Windows uses `.zip`. Archives contain the executable, project license,
third-party notices, changelog, installation note and `BUILD.json` with revision,
toolchain, target and binary checksum. `SHA256SUMS` covers exactly five archives.
`SOURCE.json` records immutable inputs; `MANIFEST.json` records every archive,
the six files inside it, their sizes, permissions and checksums, embedded Go
build information, builder tools and the two-build comparison.
FFmpeg/ffprobe remain external; server platform import also needs the pinned
extractor and Node runtime. Docker bundles server dependencies.

One invocation builds every target twice and refuses any archive or manifest
disagreement. To verify an independently supplied source and manifest identity:

```sh
python3 scripts/release_manifest.py verify \
  --directory /tmp/cutmy-release \
  --source /tmp/resolved-source/SOURCE.json \
  --expected-source-sha256 EXPECTED_SOURCE_SHA256 \
  --expected-manifest-sha256 EXPECTED_MANIFEST_SHA256
```

Get expected digests from the resolver and packaging job, not from an untrusted
download. The verifier refuses missing, extra, duplicate or unsafe members,
mixed source/document/binary identities and checksum disagreement. It bounds
decompression and archive metadata before parsing. Embedded dependency modules
from all five binaries must have entries in the shipped notices.

File names, permissions, owners, gzip headers and timestamps are normalized.
Build time is commit time, not wall-clock time. The two builds use separate
outputs on one runner and may reuse its Go cache. This does not establish an
independent hermetic rebuild or identical archives across toolchain/zlib versions.
Checksums detect mismatched downloads; they are not independent signatures or
provenance attestations.

## Automation

The [release workflow](../.github/workflows/release.yml) resolves source once,
passes that commit to the existing source CI, packages it twice in parallel and uploads one
sealed bundle. Five native jobs download that exact artifact ID, verify its
workflow run and ZIP digest, safely extract their archive and execute its
absolute binary path. They require its version/commit/build-time/native target,
local inspect/MP4/copy/MP3 diagnostics, descendant-process cancellation and a real
API/worker upload/preview/export/download/deletion flow against disposable
PostgreSQL. They do not rebuild `cutmy`. A separately built process-test helper
has its own source and binary receipts and cannot replace the archived program.

The aggregate gate requires all five targets and every required scenario, with
matching source, manifest, archive, binary and run identities. Missing reports,
tools or scenarios, skipped work and changed binaries fail. Raw execution
evidence is retained even on failure. A separate publisher with repository write
permission rechecks the tag and uploads the same verified bytes as a **draft**;
it runs only on tag pushes. Pull requests produce nonpublishable candidates.
Evidence artifacts are scoped to the run and attempt. Retry the entire workflow
when a new attempt is needed; partial reruns cannot reuse an earlier attempt
under a new identity.

Manual dispatch verifies an existing tag containing this archive protocol without
publication; older tags are refused rather than rebuilt with newer scripts. Neither path
deploys production. Inspect the draft before publishing it.

Current-source CI uses Go 1.27.2 and requires runtime/media/cancellation plus
the PostgreSQL API/worker path on every shipped Linux, macOS and Windows target.
These source jobs gate final archive acceptance along with upgrade/recovery acceptance. The
historical upgrade/restore source gate remains required. A separate Linux amd64
job uses the captured executable from this same archive bundle for all nine
upgrade/restore scenarios and eleven decoded media observations. It builds only
the immutable historical v0.2.1 program. Current archive bytes, source and run
receipts are checked before and after recovery; the aggregate requires this
result too. This does not imply native historical restore acceptance on every OS.
Release checks include the full Ubuntu/FFmpeg 6.1 suite and targeted copy/HLS
timeline regressions in the pinned Bookworm runtime base with FFmpeg 5.1.
Published v0.2.1 archives predate this sealed-bundle and extracted-binary gate;
new checks do not retroactively validate them. Archive identities and checksums
are receipts, not independent provenance attestations or OS signing/notarization.
Website production deployment is separate and must preserve persistent volumes;
see [operations](operations.md).
