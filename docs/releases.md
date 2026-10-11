# CLI releases

Releases use semantic versions; the documented 1.x library, CLI and HTTP contracts are stable. User-visible changes belong
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
   `## 1.0.1 - 2026-10-11`, retaining Unreleased for future changes. Include known
   limitations and measured benchmark methods.
3. Merge the reviewed changes using the repository's allowed method. Require
   Core checks and Public self-hosting acceptance on that actual main commit,
   including its source admission after the merge. Verify that its tree matches
   the reviewed source, then tag that exact commit. Never move an existing tag.

From a clean reviewed checkout of that verified main commit:

```sh
git tag -a v1.0.1 -m 'cutmyvideo-core v1.0.1'
make release VERSION=v1.0.1
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
Checksums detect mismatched downloads. Their authenticity for a new release is
checked separately through the tag-only attestations described below.

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
evidence is retained even on failure. The next read-only stage derives the SPDX
SBOM and trust sidecar twice from those exact binaries. A separate tag-only signer
attests their hashes, and a publisher with repository write permission rechecks
the tag, verifies the retained signatures and uploads the same bytes as a
**draft**. Pull requests produce nonpublishable unsigned candidates.
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
new checks do not retroactively validate them. A release tag must pass the real
signing/draft path and downloaded-asset verification before final publication;
unsigned candidate tests do not establish signing.
Website production deployment is separate and must preserve persistent volumes;
see [operations](operations.md).

## SBOM and release trust

The 1.x protocol adds `SBOM.spdx.json`, `TRUST.json`, two retained Sigstore
bundles and `ATTESTATIONS.json` beside the original eight files. The sealed
archive directory remains exactly five archives plus SOURCE, MANIFEST and
SHA256SUMS; the original files are never repackaged to add these sidecars.

The deterministic SPDX 2.3 JSON describes the five actual executable hashes,
their embedded Go main module, runtime/standard-library contribution and selected
Go dependency paths, versions and `h1` sums. Each dependency must match the
immutable `go.sum` and an archived notice. The main module's embedded `(devel)`
version is recorded separately from the CLI release version. The document uses
the immutable source timestamp and source/manifest hashes for its namespace.
It includes the exact shipped LICENSE and THIRD_PARTY_NOTICES hashes. Only exact
reviewed module/version/notice sections receive MIT or BSD-3-Clause assertions;
unreviewed or changed sections receive `NOASSERTION` and their notice reference.
Go module `h1` values are identified as Go module hashes, not ordinary archive
file checksums.

This describes embedded CLI composition. External FFmpeg/ffprobe, yt-dlp,
Node.js, PostgreSQL, Docker and runner packages are excluded. The document is
neither a vulnerability scan nor a complete standard-library/dependency source
inventory. It does not invent separate shipped toolchain bytes.

TRUST binds the exact five archive hashes to SOURCE, MANIFEST, SPDX and the
successful native/recovery verdict, including GitHub repository, run, attempt,
event SHA and original packaging artifact ID/digest. The read-only `trust` job
runs on candidates and existing-tag manual checks without signing permission.
The `attest` job runs only for a push of the original publishable release tag,
after all native/recovery/source gates and deterministic sidecar generation.
It alone receives `id-token: write` and `attestations: write`; the draft publisher
alone receives repository contents write permission.

The pinned official [actions/attest](https://github.com/actions/attest/tree/1e69f48acb82d1966a394da916b4c1698aa569d6)
action produces SLSA provenance for seven explicit subjects: the five archives,
SBOM and TRUST. A second statement uses the exact SPDX document as the predicate
for all five archives. Explicit checksum inputs prevent automatic subject
discovery or unrelated file inclusion. The signer re-fetches the tested packaging
artifact by its ID/digest, freshly reads embedded metadata, compares the read-only
stage's SBOM/TRUST hashes and refuses a changed tag. It does not rebuild `cutmy`.

Before retaining signature bundles, the signer actually verifies every subject
with GitHub CLI certificate policy: repository, signer workflow, source and signer
commit, tag ref, predicate type and a refusal of self-hosted runners. The publisher
downloads the exact sidecar artifact by ID/digest, derives the expected documents
again and repeats every signature/subject/predicate check. Missing or changed
signatures, candidate/manual/PR signing, stale verdicts, dirty sources and moved
tags refuse a draft. Tags are re-fetched at these boundaries; a changed tag is
never used to select different build input. Publishing uploads an explicit
13-file asset list.

These attestations authenticate a claim by the GitHub workflow about hashes.
The trust model includes GitHub OIDC/hosted execution, repository administrators,
the reviewed workflow and compiler/dependencies. Workflow-controlled predicate
contents do not become independently true because they are signed. This protocol
does not claim a SLSA level, independent hermetic rebuilding, OS code signing,
macOS notarization or protection against a compromised authorized workflow.
See [GitHub's artifact attestation documentation](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations).

## Consumer verification of a new signed release

The [consumer verifier](../scripts/release-consumer.py) checks **all 13 already
downloaded assets** in one command. The 1.x publication process runs this real
verification on all five native targets before finalizing the same draft release
and asset set. Historical v0.2.1 and unsigned candidates are refused. Mocked
signature controls remain separate from actual Sigstore verification.

Install Git, GitHub CLI 2.89.0 (the reviewed JSON output/certificate contract), and CPython 3.10 or
newer on Linux, macOS or Windows. Obtain the full expected source commit and version
from the reviewed release independently of the downloaded JSON. Use a reviewed copy
of these five files in one directory, separate from the release assets:

- [release-consumer.py](../scripts/release-consumer.py)
- [release_consumer_process.py](../scripts/release_consumer_process.py)
- [release_manifest.py](../scripts/release_manifest.py)
- [release-trust.py](../scripts/release-trust.py)
- [release-fetch.py](../scripts/release-fetch.py)

A repository checkout, Bash, Go SDK, FFmpeg, `verdict.json`, GitHub run artifacts and
packaging artifact IDs are not required. The verifier never downloads release
assets, builds, signs, publishes or executes the archived programs. Git reads only
the exact public tag; GitHub CLI verifies the retained Sigstore bundles with its
trusted roots. This is not an offline verification recipe. See the
[CLI manual](https://cli.github.com/manual/gh_attestation_verify) and
[GitHub's offline trust requirements](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/verify-attestations-offline).

Place exactly the five platform archives plus `SOURCE.json`, `MANIFEST.json`,
`SHA256SUMS`, `SBOM.spdx.json`, `TRUST.json`, `provenance.sigstore.json`,
`sbom.sigstore.json` and `ATTESTATIONS.json` in the asset directory. Run this single
command, replacing the paths, version and reviewed full 40-character commit:

```sh
python3 /path/to/reviewed-verifier/release-consumer.py --directory /path/to/downloaded-release --version v1.0.1 --expected-commit REVIEWED_FULL_40_CHARACTER_COMMIT
```

On Windows use `python` and the corresponding Windows paths. Exit status zero
prints one JSON success report only after every check completes; any refusal
returns nonzero. Keep this report outside the asset directory. Missing, extra,
nonregular or changed files fail. The verifier snapshots the original bytes in
private temporary storage, bounds JSON and archive parsing, then checks the
original downloads again before success. It needs temporary disk space for the
13-file snapshot and one bounded expanded archive.

Refusals expose a fixed stage code, never raw command errors or credentials:

| Code | Check before trying again |
| --- | --- |
| `expected_identity` | Supply the reviewed full commit and valid release version. |
| `tool_unavailable` | Install Git and a GitHub CLI supporting the documented policy flags. |
| `tag_before`, `tag_after` | Confirm connectivity and the reviewed live tag; investigate movement rather than accepting a new commit automatically. |
| `assets`, `changed_assets`, `local_io` | Check the exact 13 regular files, stable directory, permissions and temporary disk space. |
| `metadata`, `archive_content`, `composition` | Obtain the correct complete release set and investigate malformed or inconsistent bytes. |
| `provenance`, `spdx` | Check GitHub CLI/trusted-root connectivity and investigate a failed signature, certificate policy or statement; never bypass the check. |

The fixed policy requires `Kay0k1/cutmyvideo-core`, its release workflow, the
reviewed source/signer commit, the exact tag ref, GitHub-hosted runners and the
expected predicate types. It actually invokes GitHub CLI for each of seven
provenance subjects and each of five SPDX subjects. Every verified statement must
contain exactly the expected hashes; the SPDX predicate must equal the downloaded
SBOM. The provenance workflow/source/run/attempt must agree with signed TRUST.
Every verified certificate must also carry the matching `runInvocationURI`
extension, binding that run and attempt to the OIDC certificate. Older or changed
CLI/certificate formats are refused until reviewed.
The same TRUST binds SOURCE, MANIFEST, SBOM and all archives. The consumer checks
archive contents, BUILD metadata, document hashes, checksums and the structured
SPDX graph. The plain `actual_signature_verification` receipt field cannot
substitute for any of these signature calls.

Exact remote tag object and peeled commit are read before and after verification.
Both annotated and lightweight tags are supported. Missing, moved, ambiguous or
unavailable tags fail; reannotation during verification fails even when the commit
is unchanged. The expected version and commit never change to follow downloaded
metadata or a moved tag. This observes two boundaries, not an atomic lock on GitHub:
a tag can change after success or move away and back between lookups. Verify again
before use if the reviewed release state has changed.

JSON controls are limited to 16 MiB each, compressed archives to 128 MiB each and
expanded tar data to 149 MiB, with separate member limits. Each Git/CLI operation
has a 60-second watchdog and must fit within a shared ten-minute deadline measured
from verification start. Local copying/archive parsing has byte bounds, rather
than an interrupting wall-clock watchdog; it can overrun that deadline, in which
case the required final tag lookup refuses success. Command output
is watched while written to private temporary files and refused above 32 MiB
(4 KiB for tag lookup). A writer can exceed the threshold between watchdog polls;
this is an observed output limit, not an instantaneous disk quota. Linux/macOS
use a private process group. Windows requires
CPython's process handle and a supported job object: the process starts suspended,
is attached before resuming, and descendants are closed with the job. Unavailable
containment refuses verification. CI includes tiny actual Python-child output,
flood and timeout/descendant controls on all five native targets, plus a hostile
caller Git configuration test. Commands run from a fresh empty private directory;
Git discovery and global/system/environment overrides are excluded so a caller
checkout's URL rewrite cannot redirect the fixed tag lookup. Cryptographic
controls use mocks and remain separate.

The public assets do not include `go.sum` bytes or the native/recovery verdict.
Recorded BuildInfo and the claims that modules matched immutable `go.sum`, native
checks passed and two builds agreed are authenticated publisher metadata. This
consumer does not freshly read Go BuildInfo, run native/recovery scenarios or
independently reproduce the build. SPDX prose comments are signed descriptive
claims; its structured file/module/license graph is checked against archived bytes
and recorded metadata. The existing workflow/admin/compiler trust model above
still applies. OS signing, notarization, an independent hermetic rebuild and a
SLSA level are not claimed.
