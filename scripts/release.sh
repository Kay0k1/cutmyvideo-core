#!/usr/bin/env bash
# Build and package a tagged commit; never deploys or publishes anything.
set -euo pipefail

project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_dir"
version="${1:-}"
output_dir="${2:-$project_dir/dist}"
if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
    printf '%s\n' 'Usage: scripts/release.sh vX.Y.Z[-prerelease] [output-directory]' >&2
    exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
    printf '%s\n' 'Release builds require a clean checkout, including untracked files.' >&2
    exit 1
fi
revision="$(git rev-parse HEAD)"
if [[ "$(git rev-parse --verify "refs/tags/$version^{commit}")" != "$revision" ]]; then
    printf '%s\n' 'The requested version must tag the checked-out commit.' >&2
    exit 1
fi
for program in go python3; do
    command -v "$program" >/dev/null || { printf 'Required program missing: %s\n' "$program" >&2; exit 1; }
done
expected_toolchain="$(python3 -c 'from pathlib import Path; print(next(line.split()[1] for line in Path("go.mod").read_text().splitlines() if line.startswith("toolchain ")))')"
actual_toolchain="$(GOTOOLCHAIN=local GOENV=off go env GOVERSION)"
if [[ "$actual_toolchain" != "$expected_toolchain" ]]; then
    printf 'Release requires %s; active toolchain is %s.\n' "$expected_toolchain" "$actual_toolchain" >&2
    exit 1
fi
python3 scripts/update-notices.py --check
export SOURCE_DATE_EPOCH="$(git show -s --format=%ct HEAD)"
export CGO_ENABLED=0 GOENV=off GOTOOLCHAIN=local GOAMD64=v1 GOARM64=v8.0
# Caller build flags/workspaces must not silently alter a published executable.
export GOFLAGS= GOEXPERIMENT= GOWORK=off
build_time="$(python3 -c 'import datetime,os; print(datetime.datetime.fromtimestamp(int(os.environ["SOURCE_DATE_EPOCH"]), datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
staging_dir="$(mktemp -d)"
trap 'rm -rf -- "$staging_dir"' EXIT
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
    target_os="${target%/*}"
    target_arch="${target#*/}"
    binary=cutmy
    [[ "$target_os" != windows ]] || binary=cutmy.exe
    archive_name="cutmyvideo-core_${version}_${target_os}_${target_arch}"
    package_dir="$staging_dir/$archive_name"
    mkdir "$package_dir"
    GOOS="$target_os" GOARCH="$target_arch" go build -mod=readonly -trimpath -buildvcs=false \
        -ldflags "-s -w -buildid= -X main.version=$version -X main.commit=$revision -X main.buildTime=$build_time" \
        -o "$package_dir/$binary" ./cmd/cutmy
    cp LICENSE THIRD_PARTY_NOTICES.md CHANGELOG.md "$package_dir/"
    cp docs/release-artifact.txt "$package_dir/README.txt"
    python3 scripts/package-release.py "$package_dir" "$output_dir" "$version" "$revision" "$target_os" "$target_arch"
done
python3 - "$output_dir" "$version" <<'PY'
import hashlib
from pathlib import Path
import sys
directory, version = Path(sys.argv[1]), sys.argv[2]
archives = sorted(directory.glob(f'cutmyvideo-core_{version}_*'))
with (directory / 'SHA256SUMS').open('w', encoding='utf-8', newline='\n') as out:
    for archive in archives:
        if archive.is_file() and archive.name.endswith(('.tar.gz', '.zip')):
            out.write(f'{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n')
PY
printf 'Release archives and SHA256SUMS: %s\n' "$output_dir"
