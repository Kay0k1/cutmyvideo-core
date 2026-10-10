# User and library guide

## Choosing an export

`accurate` decodes/re-encodes for precise requested boundaries. MP4 uses H.264/AAC;
MP3 re-encodes audio and needs a source with audio. `copy` preserves compatible
streams in MP4 and begins at a preceding keyframe. Check `actual_start_ms` and
`actual_end_ms`. Copy does not resize: use `best` for original resolution, or
accurate mode when downscaling.

`fast` favors CPU time; `compact` favors smaller video files. Both retain the
selected resolution cap and frame cadence, without upscaling. Long accurate
exports use bounded bitrates when needed to fit the output ceiling, which can
affect quality. Time, size, storage and transfer limits still apply.

## CLI

Install FFmpeg/ffprobe and build or unpack `cutmy`, then:

```sh
./cutmy version
./cutmy inspect --input interview.mp4
./cutmy clip --input interview.mp4 --start 10 --end 40 --output quote.mp4
./cutmy clip --input interview.mp4 --start 10 --end 40 --output quote.mp3
./cutmy clip --input interview.mp4 --start 10 --end 40 --mode copy --output copy.mp4
./cutmy clip --input interview.mp4 --start 10 --end 40 \
  --quality 720p --profile compact --threads 2 --timeout 1h --output compact.mp4
```

Use `--help` per command. Arguments are named flags; stray positional arguments
are rejected. Timestamps accept nonnegative decimal seconds, `MM:SS` or
`HH:MM:SS`, with an optional fraction on final seconds (`01:23:10.250`). Signs,
exponents and fractional hours/minutes are rejected. End must follow start and
be within the source.

Output JSON contains `path`, `size_bytes`, `actual_start_ms`, `actual_end_ms`.
`inspect` returns `duration_ms` and video dimensions when present. Existing files
and symlinks are never replaced. Failed exports remove their private workspace.
Local exports allow 24-hour intervals, with a default 10 GiB output ceiling.
Inspection defaults to two minutes; export defaults to 30 minutes. `--timeout`
changes the deadline. Unix interruption cancels/reaps the media process group;
native macOS/Windows behavior is not yet verified in CI.

## Go library

Add the module to your application:

```sh
go get github.com/Kay0k1/cutmyvideo-core@v0.2.0
```

```go
package main

import (
    "context"
    "encoding/json"
    "errors"
    "log"
    "os"
    "time"

    "github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func main() {
    cutter := engine.New(engine.Config{
        EncodeProfile: "fast", FFmpegThreads: 2, ExportTimeout: time.Hour,
    })
    result, err := cutter.Export(context.Background(), "interview.mp4", "quote.mp4",
        engine.Range{StartMS: 10_000, EndMS: 40_000},
        engine.Options{Quality: "720p", CutMode: "accurate"})
    if errors.Is(err, engine.ErrOutputExists) {
        log.Fatal("Choose a new output filename")
    }
    if err != nil {
        log.Fatal(err)
    }
    if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
        log.Fatal(err)
    }
}
```

`Inspect` and `Export` accept local regular files. Omitted format comes from
`.mp4`/`.mp3`; quality/mode/profile default to `best`/`accurate`/`fast`. Positive
`InspectTimeout`, `ExportTimeout`, `MaxOutputBytes` override defaults; earlier
caller deadlines win. Invalid config is reported by the operation. Library calls
do not share server quotas/queue: integrate your application's admission and
isolation layer. The [package source](../pkg/engine/engine.go) documents the contract.

## HTTP API

Start the [Compose example](../deploy/compose.example.yml). This complete example
uses Bash, curl and jq, and an input lasting at least 40 seconds. Keep the cookie
private: it authorizes access, while source/job IDs alone do not.

```sh
set -euo pipefail
umask 077
api_origin=http://localhost:8080
cookie_file=$(mktemp)
job_file=$(mktemp)
trap 'rm -f "$cookie_file" "$job_file"' EXIT
curl --fail --silent --show-error -c "$cookie_file" "$api_origin/api/v1/session" >/dev/null
source_id=$(curl --fail --silent --show-error -b "$cookie_file" \
  -F file=@interview.mp4 "$api_origin/api/v1/uploads" | jq -er .id)
request_json=$(jq -nc --arg source_id "$source_id" \
  '{source_id:$source_id,ranges:[{start_ms:10000,end_ms:40000,label:"Quote"}],format:"mp4",quality:"720p",cut_mode:"accurate"}')
job_id=$(curl --fail --silent --show-error -b "$cookie_file" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: tutorial-export-1' \
  -d "$request_json" "$api_origin/api/v1/jobs" | jq -er .id)
while true; do
  curl --fail --silent --show-error -b "$cookie_file" \
    "$api_origin/api/v1/jobs/$job_id" > "$job_file"
  job_status=$(jq -er .status "$job_file")
  case "$job_status" in succeeded|failed|cancelled) break ;; esac
  sleep 1
done
cat "$job_file"
download_path=$(jq -er '.items[0].artifact.download_url // empty' "$job_file")
curl --fail --show-error -b "$cookie_file" \
  "$api_origin$download_path" --output quote.mp4
```

Use a fresh `Idempotency-Key` when changing the request. A failed job may contain
successful items; inspect every item in multi-range batches. Cancel with
`POST /api/v1/jobs/{id}/cancel`, list history with `GET /api/v1/jobs`, and delete
unused sources with `DELETE /api/v1/sources/{id}` after user confirmation.
Active exports prevent source deletion.

Import a source page with `POST /api/v1/sources` and JSON `{ "url": "https://…" }`.
Direct files download in full; platform progressive/HLS reads select bounded
intervals. See the [provider catalogue](providers.md) before relying on a source.

For `preview_kind=window`, request
`/api/v1/sources/{id}/preview?start_ms=10000` with the same cookie. Use
`X-Preview-Start-MS`/`X-Preview-End-MS` to map local playback time back to source
positions. Nearby seeks share aligned 30-second windows. `priority=background`
prefetch yields to foreground seeks. `X-Preview-Cache` reports `hit`/`miss`.
HTTP uses `no-store` even though the server retains a private authorized cache.

## Troubleshooting

| Result | What to check |
|---|---|
| `copy_quality_unsupported` | Use `best` for original resolution or accurate mode for resizing |
| Missing audio / incompatible copy codec | Choose an input with audio or a supported accurate export |
| `platform_access_required` / `platform_unavailable` | Public accessibility, region and provider restrictions; cookies are not supported |
| `unsupported_stream` | Finite unencrypted supported streams are required; separate HLS/DASH fragments are refused |
| `waiting_storage` | Free storage or delete unused sources; the job has a finite waiting deadline |
| `source_timeout` | Check import/upload/preview and proxy deadlines |
| HTTP 404 for existing ID | Use the original cookie; expired/deleted/foreign resources are indistinguishable |
| Job remains queued | Worker must run, share database/media with API and pass its health check |

Detailed statuses, diagnostics and retention are in the [API guide](api.md).
Operators should follow the [runbook](operations.md).
