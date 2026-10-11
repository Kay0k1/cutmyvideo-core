#!/usr/bin/env bash
# Package an immutable source twice; never creates tags or publishes anything.
set -euo pipefail
project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
exec python3 "$project_dir/scripts/release_manifest.py" build --repo "$project_dir" "$@"
