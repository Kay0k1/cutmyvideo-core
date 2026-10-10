#!/usr/bin/env bash
set -euo pipefail

for program in ffmpeg ffprobe python3; do
    command -v "$program" >/dev/null || { printf 'Required program missing: %s\n' "$program" >&2; exit 1; }
done
if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
    printf '%s\n' 'Set TEST_DATABASE_URL to a dedicated PostgreSQL test database.' >&2
    exit 1
fi
extractor="${CUTMY_TEST_YTDLP:-yt-dlp}"
if ! command -v "$extractor" >/dev/null; then
    printf '%s\n' 'Install yt-dlp 2026.08.19 or set CUTMY_TEST_YTDLP to its executable.' >&2
    exit 1
fi
if [[ "$("$extractor" --version)" != "2026.08.19" ]]; then
    printf '%s\n' 'Full checks require the runtime-pinned yt-dlp 2026.08.19.' >&2
    exit 1
fi
