# Agent workflow: supplied times to editable clips

Use this workflow when a user already has source timecodes, or when the agent
obtains them through another transcription or analysis tool. The collection
engine joins the selected source intervals and applies explicit crop/audio
settings. Keep the collection JSON as the editable source of truth.

For Python or Node agents, the [resumable SDK workflow](sdk-collections.md) handles
recoverable source preparation, validation, incremental admission, downloads,
explicit failed-clip repair and process interruption. It also covers a scoped
browser handoff and adoption of the editor's current jobs without rerendering
matching clips. Existing timestamped SRT/VTT can become
[per-clip subtitle sidecars](subtitle-sidecars.md) using actual export maps.
The CLI/HTTP steps below remain useful when implementing another client.

This workflow needs a binary containing clip-collection support, FFmpeg/ffprobe
and a finite local media file. For the current checkout:

```sh
go build -trimpath -o bin/cutmy ./cmd/cutmy
bin/cutmy version
bin/cutmy collection --help
```

## Complete local cycle

Copy [the example](../examples/collection.json) beside `interview.mp4` or update
its `input`. The example needs at least 9.5 seconds of source media. Prepare an
existing output directory, then check the selected plan without encoding:

```sh
mkdir -p clips/revision-1
cutmy --json-errors collection validate \
  --plan collection.json --output clips/revision-1 --rendition draft
```

Produce draft **assembled** clips to inspect their boundaries and crops:

```sh
cutmy --json-errors collection export \
  --plan collection.json --output clips/revision-1 --rendition draft
```

Inspect the resulting files. Use their segment maps to translate output time
back to the original recording. If a sentence is cut short, adjust the source
interval; the engine has no transcript and cannot infer sentence boundaries.

After changing only the `opening` clip, create a fresh revision directory and
export that ID. The other clip is not rendered:

```sh
mkdir -p clips/revision-2
cutmy --json-errors collection validate \
  --plan collection.json --output clips/revision-2 --clips opening
cutmy --json-errors collection export \
  --plan collection.json --output clips/revision-2 --clips opening
```

Names use IDs, not labels or positions: `opening.final.mp4`. Keep IDs across
edits. Empty selection means all clips; selected IDs still follow plan order.
Draft and final names coexist. An existing filename is never silently replaced.

## Reading results and failures

Successful stdout is JSON. `outputs` identifies each completed clip by `id` and
contains its file path, byte size, requested ranges, actual source/output segment
mapping and track-selection manifest. A final file is ready only after output
verification and exclusive publication.

With `--json-errors`, stderr contains the terminal error. Collection diagnostics
can include `clip_id` and `field`. Correct the named field rather than guessing
from subprocess logs. Do not parse the human message as a stable interface.

A later failure can produce stdout with `incomplete:true` and ready outputs,
plus a nonzero exit code and stderr error. Preserve those ready files. Retry only
the remaining IDs or use a fresh directory. `publication_uncertain` means a
final path may already exist: inspect it before requesting another export.

Validation observes current prerequisites and proposed names. It does not reserve
space, guarantee future tool/source availability or certify media decoding.
Inspect deferred checks and handle execution errors.

## HTTP cycle

Create a session and source with the existing API. Submit a JSON document shaped
as follows; the HTTP plan has no local `input`:

```json
{
  "source_id": "src_REPLACE",
  "plan": {
    "version": 1,
    "options": {"format": "mp4", "quality": "best", "cut_mode": "accurate"},
    "clips": [{
      "id": "opening",
      "label": "Question and answer",
      "ranges": [
        {"start_ms": 1000, "end_ms": 2500, "label": "Question"},
        {"start_ms": 4000, "end_ms": 6500, "label": "Answer"}
      ]
    }]
  },
  "clip_ids": ["opening"],
  "rendition": "draft"
}
```

Send it to `POST /api/v1/collections/validate`, then to
`POST /api/v1/collections` with an `Idempotency-Key`. Preserve the key for
retries. Read every clip in the response: accepted clips have ordinary jobs;
unaccepted clips carry diagnostics and retryability. Poll accepted jobs and
respect `Retry-After` before resubmitting pending work.

Retained jobs recover even after source cleanup or operator limits change.
Only pending clips must pass current source and budget checks. Keep the original
source available for new revisions.

`complete` describes queue admission. It is not an encoding success flag.
After editing one clip, the same collection key recovers unchanged effective
clip requests and creates a new job for the changed one. Changing the rendition
also creates distinct work. A fresh key requests a new run of unchanged clips.

Download ready artifacts using the same private owner session. Upload the
downloaded media to another editing tool as needed; the artifact URL alone is
not public access. Keep the original source available until revisions are done.

## Reproducible demonstration

The acceptance script generates a real synthetic audiovisual source, validates
a 15-clip collection, exports draft/final files, decodes every result and checks
a selected revision. Optional matched measurements compare one collection call
with equivalent legacy joined-plan calls. Output remains outside the checkout:

```sh
python3 scripts/collection-acceptance.py \
  --binary "$PWD/bin/cutmy" --output /tmp/cutmy-collection-evidence
```

See [collection semantics](clip-collections.md) and
[the HTTP contract](../api/openapi.yaml) for fields and limitations.
