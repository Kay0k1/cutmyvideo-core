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
new checks do not retroactively validate them. No approved tag has exercised the
new signing/draft path yet; candidate tests cannot establish actual signing.
Website production deployment is separate and must preserve persistent volumes;
see [operations](operations.md).

## SBOM and release trust

The prepared protocol adds `SBOM.spdx.json`, `TRUST.json`, two retained Sigstore
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

These commands apply to a future tag containing the accepted protocol, not to
historical v0.2.1 or an unsigned candidate. Obtain the expected full source commit
from the reviewed release/tag independently of the downloaded JSON sidecars.
The prepared commands use the flags available in GitHub CLI 2.89.0; consult
[the CLI verification manual](https://cli.github.com/manual/gh_attestation_verify)
if using another version. Run the example in Bash with Git and GitHub CLI. A
certificate's tag ref records the signing event; it does not prove that the live
remote tag still points to the reviewed commit. Check the exact remote tag before
and after verification. Missing, moved, ambiguous or mismatched tag lookups fail;
they never select a new expected commit. Both annotated and lightweight tags are
supported. Example for Linux amd64, after downloading the archive:

```bash
set -euo pipefail
version=v0.3.0
expected_commit=REVIEWED_FULL_40_CHARACTER_COMMIT
archive="cutmyvideo-core_${version}_linux_amd64.tar.gz"

if [[ ! "$expected_commit" =~ ^[0-9a-f]{40}$ ]]; then
  printf '%s\n' 'Set expected_commit to the independently reviewed full commit.' >&2
  exit 1
fi
git check-ref-format "refs/tags/$version" >/dev/null || exit 1

read_release_tag() {
  local remote_refs oid ref extra tag_object='' peeled_commit='' commit
  if ! remote_refs=$(git ls-remote --exit-code --tags \
      https://github.com/Kay0k1/cutmyvideo-core.git \
      "refs/tags/$version" "refs/tags/$version^{}"); then
    printf '%s\n' 'Release tag lookup failed or the tag is missing.' >&2
    return 1
  fi
  while IFS=$' \t' read -r oid ref extra; do
    if [[ ! "$oid" =~ ^[0-9a-f]{40}$ || -z "$ref" || -n "$extra" ]]; then
      printf '%s\n' 'Invalid release tag response.' >&2
      return 1
    fi
    case "$ref" in
      "refs/tags/$version")
        if [[ -n "$tag_object" ]]; then
          printf '%s\n' 'Ambiguous release tag response.' >&2
          return 1
        fi
        tag_object=$oid ;;
      "refs/tags/$version^{}")
        if [[ -n "$peeled_commit" ]]; then
          printf '%s\n' 'Ambiguous release tag response.' >&2
          return 1
        fi
        peeled_commit=$oid ;;
      *)
        printf '%s\n' 'Unexpected release tag reference.' >&2
        return 1 ;;
    esac
  done <<< "$remote_refs"
  commit=${peeled_commit:-$tag_object}
  if [[ -z "$tag_object" || "$commit" != "$expected_commit" ]]; then
    printf '%s\n' 'Release tag does not identify the reviewed commit.' >&2
    return 1
  fi
  printf '%s %s\n' "$tag_object" "$commit"
}

release_tag_before=$(read_release_tag) || exit 1

gh attestation verify "$archive" \
  --repo Kay0k1/cutmyvideo-core \
  --signer-workflow Kay0k1/cutmyvideo-core/.github/workflows/release.yml \
  --source-digest "$expected_commit" --signer-digest "$expected_commit" \
  --source-ref "refs/tags/$version" --deny-self-hosted-runners \
  --predicate-type https://slsa.dev/provenance/v1 --format json

gh attestation verify "$archive" \
  --repo Kay0k1/cutmyvideo-core \
  --signer-workflow Kay0k1/cutmyvideo-core/.github/workflows/release.yml \
  --source-digest "$expected_commit" --signer-digest "$expected_commit" \
  --source-ref "refs/tags/$version" --deny-self-hosted-runners \
  --predicate-type https://spdx.dev/Document/v2.3 --format json

release_tag_after=$(read_release_tag) || exit 1
if [[ "$release_tag_after" != "$release_tag_before" ]]; then
  printf '%s\n' 'Release tag changed during verification.' >&2
  exit 1
fi
```

Apply the same before/after remote-tag boundary when verifying the other
archives and sidecars. Verify `SBOM.spdx.json` and `TRUST.json` with the same
provenance policy, then require their hashes and archive/SOURCE/MANIFEST hashes
to agree with TRUST. The boundary also refuses replacement of an annotated tag
object during verification even when its peeled commit is unchanged.
Verify SHA256SUMS, archive manifests and the extracted CLI's version/commit too.
A checksum downloaded from the same unauthenticated channel does not provide
independent authentication. Repeat the verification for each downloaded archive;
the publisher verifies all five. A changed file, a different expected commit or
the wrong signer workflow must fail verification.

The retained `provenance.sigstore.json` and `sbom.sigstore.json` can be selected
using `--bundle FILE`. Keeping a bundle alone does not establish offline trust:
the verifier also needs a separately trusted Sigstore root and timestamp/log
verification material. See [GitHub's offline verification instructions](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/verify-attestations-offline)
before using `--custom-trusted-root`. Do not treat ATTESTATIONS.json or TRUST.json
as a signature verifier; the signature policy above remains required.
