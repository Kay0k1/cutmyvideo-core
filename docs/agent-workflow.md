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

## Retention, access and recovery

The saved server project, its source and the SDK's private local directory have
separate lifetimes. Creating, editing or sharing a project does not extend any
source or artifact deadline. A project or grant alone does not pin the source;
accepted jobs do, including terminal jobs until their rows are removed.

| Object | Lifetime and access | Cleanup and recovery |
| --- | --- | --- |
| Source preparation receipt | Owner/key/immutable intent; seven-day cleanup target. A ready receipt is retained while its source exists. Processing claims use `max(UPLOAD_TIMEOUT,SOURCE_TIMEOUT) + 1 minute`. | Same-key replay recovers retained success or permanent rejection; an interrupted claim can be reclaimed after its lease. Deleting the source does not authorize a silent replacement. Later preparation admission prunes up to 200 old receipts whose lease has expired and source is gone. |
| Source | `SOURCE_TTL`, default 24 hours, is a target for unpinned sources. Jobs and active media operations can retain it longer. | Maintenance removes eligible source metadata and schedules its files for durable deletion. Optional successful-export media cleanup keeps source metadata with `media_removed:true`; accepted jobs and artifacts remain. |
| Server project | Seven days from creation. Reads and scoped operations require an unexpired project plus its owner or a live recipient grant. Edits and replays do not renew it. | Expiry hides the project immediately; later project creation prunes up to 200 expired projects. Explicit project DELETE removes its metadata, job links, handoffs, grants and retry intents. It does not cancel or delete the underlying jobs, artifacts or source. |
| Handoff token | Ten minutes; first redemption binds one recipient. Only the owner issues tokens. | Token expiry prevents redemption, including lost-reply recovery; it does not invalidate an already issued grant. Issuing another token prunes expired tokens for that project. |
| Recipient grant | Up to 24 hours, capped by project expiry. It authorizes only scoped project routes. | Expiry hides the project from that recipient. Owner revocation deletes the project's grants and tokens immediately; it keeps the project, accepted jobs and outputs. Redemption prunes relevant expired grants. |
| Job and idempotency key | Accepted work belongs to the source owner. Project/grant expiry, revocation and project DELETE do not cancel it. | A renewable worker lease lasts 45 seconds; recovery permits up to three claims. Retention removes terminal jobs only after their artifacts are gone and `ARTIFACT_TTL` has elapsed since the last job update. Deleting a job also removes its project links and idempotency mapping. |
| Artifact | A publication-time `ARTIFACT_TTL` deadline, default 24 hours; active jobs pin their results. Access requires the owner or a live grant through an attached project job. | Maintenance removes expired registrations, then retries physical deletion through charged tombstones. Retained jobs/files can recover a lost reply; removed artifacts require explicit repair while the original source is still available. |
| SDK directory | Private owner capability, saved requests/keys, checkpoints, partial transfers and verified files; no automatic TTL. Python and Node formats are separate. | Resume uses the same directory, server and owner. Server expiry/revocation does not erase downloaded files or local state. A caller timeout stops that caller, while accepted remote jobs continue. |

Access expiry and physical removal are different events. Project, token, grant
and preparation metadata use the admission cleanup paths above, rather than
background retention. Expired metadata can therefore remain in PostgreSQL until
a later eligible admission or explicit deletion; that is not an extension of
the access or replay guarantee. Source and artifact bytes use the independent
worker/maintenance lifecycle. Their physical cleanup can lag the retention target.

The browser owner cookie also has its own seven-day maximum age; losing it does
not delete server work. SDK recovery keeps that capability in private local
state. A recipient whose grant has ended needs a new owner-issued handoff;
keeping an old token or project JSON does not restore access. The source owner's
ordinary job/artifact routes remain available under their own retention rules.

Explicit source DELETE refuses active jobs and protected media operations.
When allowed, it removes the source, its terminal jobs, artifacts and media.
An unexpired server project can still expose its saved edit plan and source snapshot,
but cannot recreate missing source bytes. Likewise, a retained project does not
preserve job history after normal job retention removes it.

See [source retention and media cleanup](source-retention.md),
[HTTP retention and ownership](api.md#jobs),
[resumable SDK recovery](sdk-collections.md#resume-in-another-process) and the
[upgrade/backup recovery gate](backup-recovery.md) for the detailed guarantees.

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
