# Reproduction, provenance and availability

The measured before archive is v0.2.1. The after archive is an exact **locally packaged 06bb candidate**. This publication contains identities, raw selected timings and recipes, not the archived binaries/tools/images/fixture or the complete 4.26 GB raw corpus. No hosted merge artifact can substitute for the measured after binary. A self-rebuild has not been performed for this publication; SHA equality must be checked rather than promised.

## After archive from the exact source

Use a fresh clean detached checkout of the public core repository at 06bb74bca90a79fbf399c01cf71f7f44af186431 and actual Go 1.27.2. Keep all generated manifests/artifacts outside the checkout. Do not add these frozen benchmark files to that release checkout. SOURCE epoch 1791678826 and build_time 2026-10-11T00:33:46Z derive from the immutable commit, without overrides.

```sh
git clone https://github.com/Kay0k1/cutmyvideo-core.git /absolute/clean-core
git -C /absolute/clean-core checkout --detach 06bb74bca90a79fbf399c01cf71f7f44af186431
cd /absolute/clean-core
python3 scripts/release_manifest.py resolve --repo /absolute/clean-core --candidate 06bb74bca90a79fbf399c01cf71f7f44af186431 --output /absolute/reproduction/after.SOURCE.json
sha256sum /absolute/reproduction/after.SOURCE.json
scripts/release.sh --source /absolute/reproduction/after.SOURCE.json --expected-source-sha256 41a9af4320344e7f894b8db18a5e7fa6a403d6e7eef1dc189ec45431bacea3f6 /absolute/reproduction/after-artifacts
python3 scripts/release_manifest.py verify --directory /absolute/reproduction/after-artifacts --source /absolute/reproduction/after.SOURCE.json --expected-source-sha256 41a9af4320344e7f894b8db18a5e7fa6a403d6e7eef1dc189ec45431bacea3f6 --expected-manifest-sha256 337d592649ec7e7b61164b187e53e1ce56cdc77e9e4e8fa88df94a13659a64c7 --repo /absolute/clean-core
```

Compare resolved SOURCE SHA256 with `41a9af4320344e7f894b8db18a5e7fa6a403d6e7eef1dc189ec45431bacea3f6`. The exact public copies are [after.SOURCE.json](after.SOURCE.json) and [after.MANIFEST.json](after.MANIFEST.json). The standard release.sh calls release_manifest.py build, materializes exact SOURCE, performs two five-target builds with pinned Go/trimpath/buildvcs=false/buildid=, and packages via package-release.py. Finally compare the Linux/amd64 archive hash `f009fb6c5f28ea7481ada95fbc69557f17a581065913f410b7178e09a60d3395` and extracted cutmy hash `b22e024425ab8ac43eba19d07e693c8e8eec2dc57061ff5020c597d364851a9c`. A mismatch is a different artifact; investigate it before attributing any rerun to the original binary.

## Before legacy archive

Use a separate clean detached f2be62c42a6a0d3d7a017b40bd683596bc578b95 checkout with its exact v0.2.1 tag and Go 1.27.2:

```sh
scripts/release.sh v0.2.1 /absolute/reproduction/before-artifacts
```

That source uses its legacy release.sh/package-release.py and commit SOURCE_DATE_EPOCH. It has no new release_manifest.py. Its measured archive has no standalone SOURCE/MANIFEST; [before.BUILD.json](before.BUILD.json) is the exact embedded metadata and [before.ARCHIVE-IDENTITY.json](before.ARCHIVE-IDENTITY.json) binds all six actual payloads. Compare Linux archive `89e7832aaa29e25e6794a9d6e9e14eb9027549bdfc92270eecfd983aac1a8a21` and binary `a80cf0504fc4c9c9719c7be8fcd291375dd66637316b7a06b3242187fed8dbf8`. No equality is asserted without doing those checks.

## Separate workload rerun

`harness-frozen/scripts/` contains the exact measured harness, fixture helper, 68-control source and historical preparation README. The SHA-bound original config/fixture manifest identities are in INPUTS; [CONFIG.template.json](CONFIG.template.json) and [FIXTURES.template.json](FIXTURES.template.json) normalize only local paths. Their hashes therefore differ from original evidence. Replace all `/absolute/` paths, rehash the fixture JSON and put its new hash into config, then hash/review the new config. Complete input/source/tool proofs still need independent review; templates are not fresh proofs. Exact numeric guards, quotas, worker/client matrix, output checks and 120-second operation budgets must stay unchanged for comparison.

Requirements: Linux/amd64, existing verified rootful local Docker/cgroup-v2 images, exact externally archived executables and FFmpeg/ffprobe bytes, the licensed exact fixture and a fresh owned 0700 scratch directory. Images must already exist: the harness does not build or pull them. Exact existing image IDs and tool SHA256s are in INPUTS. Check scratch/evidence free space at least 14,949,123,912 B and retain 8 GiB; guard 1800 s/cell and 14400 s/pair. Obtain a resource window appropriate to the shared host before any operational rerun; do not run against production data.

After reviewing all identities and the new path-normalized config, this is the command shape (not executed for publication):

```sh
python3 harness-frozen/scripts/media-capacity.py \
  --config /absolute/new-reviewed-CONFIG.json \
  --expected-config-sha256 <new-independently-reviewed-config-SHA256> \
  --evidence-dir /absolute/new-evidence-folder \
  --scratch-dir /absolute/new-owned-scratch \
  --docker /absolute/docker \
  --mode descriptive
```

Every rerun is a separate experiment. Preserve every failed attempt and raw result, apply all 168 workflows / 504 exports / 168 previews and exact owned cleanup, and compare actual binary/input/tool/resource identities before interpreting new timings. Never replace a timeout or failed output with a successful observation.
