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
   `## 0.2.1 - 2026-10-10`, retaining Unreleased for future changes. Include known
   limitations and measured benchmark methods.
3. Commit the reviewed changes and tag that exact commit. Push the commit/tag when
   release publication is intended.

From a clean reviewed checkout:

```sh
git tag -a v0.2.1 -m 'cutmyvideo-core v0.2.1'
make release VERSION=v0.2.1
```

The script refuses dirty/untracked files, malformed versions and tags that do not
identify HEAD. It requires the exact `toolchain` version from go.mod (Go 1.27.2), readonly modules, disabled CGO,
trimmed paths, fixed target baseline and commit-time metadata. Caller GOFLAGS and
workspaces are disabled. It does not deploy anything.

## Artifacts and reproducibility

Five targets: Linux amd64/arm64, macOS amd64/arm64, Windows amd64. Linux/macOS use
`.tar.gz`; Windows uses `.zip`. Archives contain the executable, project license,
third-party notices, changelog, installation note and `BUILD.json` with revision,
toolchain, target and binary checksum. `SHA256SUMS` covers all archives.
FFmpeg/ffprobe remain external; server platform import also needs the pinned
extractor and Node runtime. Docker bundles server dependencies.

Rebuild the same tag with the same Go and Python/zlib toolchain:

```sh
scripts/release.sh v0.2.1 /tmp/cutmy-release-first
scripts/release.sh v0.2.1 /tmp/cutmy-release-second
cmp /tmp/cutmy-release-first/SHA256SUMS /tmp/cutmy-release-second/SHA256SUMS
cd /tmp/cutmy-release-first
sha256sum --check SHA256SUMS
```

Use empty output directories; archives are written exclusively. File names,
permissions, owners, gzip headers and timestamps are normalized. Build time is
commit time, not wall-clock time. Release CI checks two builds in the same runner;
this does not imply identical archives across different toolchain/zlib versions.
Checksums detect mismatched downloads; they are not independent signatures or
provenance attestations.

## Automation

The [release workflow](../.github/workflows/release.yml) reruns full CI, builds
twice, compares checksums and saves verified artifacts. A `v*` tag push creates a
**draft** GitHub release using that version's changelog entry. Prerelease tags
receive the prerelease flag. Inspect artifacts before publishing the draft.
Manual dispatch rebuilds an existing tag and saves artifacts without publication.
Failed checks prevent packaging/publication.

Current-source CI uses Go 1.27.2 and requires runtime/media/cancellation plus
the PostgreSQL API/worker path on every shipped Linux, macOS and Windows target.
These native jobs gate packaging along with the upgrade/recovery acceptance.
Release checks include the full Ubuntu/FFmpeg 6.1 suite and targeted copy/HLS
timeline regressions in the pinned Bookworm runtime base with FFmpeg 5.1.
Website production deployment is separate and must preserve persistent volumes;
see [operations](operations.md).
