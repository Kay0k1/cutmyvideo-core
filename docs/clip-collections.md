# Clip collections

A collection describes independent edits of **one** source. Each clip has a
stable ID, a label, ordered source intervals and optional overrides. A question
and answer can become one clip while another clip removes a pause by joining
two different intervals. The engine executes supplied times; it does not choose
moments or transcribe speech.

Collections extend the existing engine, CLI and owner-protected HTTP queue.
Existing version 1 cutting plans and their separate/joined behavior remain
supported. A collection is a distinct document and operation, not a change to
the original `Plan` layout.

## Plan and selection

Start with [the example collection](../examples/collection.json). `input` is a
local filename; relative paths resolve beside the collection JSON file.
HTTP requests omit `input` and use an existing `source_id`.

`version` is 1. A plan contains 1–128 clips. IDs match
`[a-z0-9][a-z0-9_-]{0,63}` and are unique. Windows device names `con`, `prn`,
`aux`, `nul`, `com1`–`com9` and `lpt1`–`lpt9` are rejected on every platform,
including for unselected clips, so plans remain portable. Names such as
`con-safe` and `com10` are allowed. Labels are bounded to 200 UTF-8 bytes.
Each clip contains ordered `[start_ms,end_ms)` intervals in original-source
milliseconds. Overlapping, repeated and reversed-order intervals are valid;
each interval's end must still follow its own start.

An empty selection means all clips. A selection of clip IDs keeps **plan order**;
it does not reorder clips. Unknown or repeated selected IDs are errors. IDs,
labels and structural bounds of the whole plan are checked, while media options
and interval semantics are checked for selected clips. This allows another
unselected edit to remain unfinished. The configured range and total-duration
budgets apply to the selected collection as a whole.

## Settings and inheritance

Plan `options` supply defaults; each clip's `options` override them. Empty
`format`, `quality`, `cut_mode` and `subtitle_mode` strings inherit. Effective
defaults are MP4, best quality, accurate mode and omitted subtitles. The existing
audio, copy-container, crop and subtitle restrictions still apply.

`no_audio` is optional: explicit false enables audio inherited as disabled.
An omitted `audio_track` inherits; `audio_track: -1` resets to automatic track
selection. Resetting the track does not implicitly enable audio.
An omitted crop inherits. `clear_crop: true` removes an inherited crop;
specifying both `crop` and `clear_crop` is an error. Crop geometry uses upright
normalized display coordinates, as described in [media selection](media.md#crop).
One effective crop applies to all fragments within that clip.

## Draft and final renditions

Final is the default and uses the selected export quality/profile. Drafts require
accurate MP4 and use a 720p displayed-height cap, ultrafast H.264 and CRF 28.
They use the same intervals, order, crop and audio selection as final exports.
Sources are not enlarged. A draft remains a real assembled export; it does not
promise a faster result on every source or machine.

Drafts explicitly refuse copy, MKV and MP3. Copy keyframe boundaries cannot be
faithfully represented by silently substituting an accurate preview.

Local output names use the clip ID and rendition, for example `opening.draft.mp4` and
`opening.final.mp4`. Changing labels or clip order does not rename an existing
ID. Existing files and symlinks are preserved, including publication races.
Use a fresh output directory for a new revision of an already exported clip.

## Validation and execution guarantees

Validation performs no video encoding and publishes no files. It checks the
plan, selected source bounds, available media/tool prerequisites and proposed
output names. Codec and path observations are snapshots: export checks its own
admission, source identity, actual output bytes and publication barriers.
Remote source media/tool checks may be deferred explicitly rather than certified.

Every completed clip includes actual source/output segment mapping and track
manifest. A later failure retains completed outputs. Callers must examine both
the error and partial result; an error does not imply that no file was produced.
The whole local operation shares a timeout and aggregate output-byte budget.

## HTTP collection adapter

`POST /api/v1/collections/validate` checks a request without creating export jobs.
`POST /api/v1/collections` submits the selected clips as ordinary joined jobs.
Both use the existing owner session, same-origin checks and private media access.
The request contains `source_id`, `plan`, optional `clip_ids` and `rendition`.

Submission requires `Idempotency-Key` (at most 128 UTF-8 bytes). Each clip's
effective request and ID are combined with that key to derive its job key.
Retrying an unchanged collection/key recovers accepted jobs. Changing one clip
under that key creates new work only for that changed effective clip request.
Use a fresh collection key to explicitly request another unchanged export.
This request-aware collection behavior differs from ordinary `/jobs`, whose
owner/key pair returns the original job regardless of body changes.

Retained jobs are recovered before current source-duration, source-media and
operator-budget checks. Recovery still requires source ownership, a valid plan
shape, supported options, unique IDs and a valid selection. It remains available
after successful source cleanup clears the local path, including explicit audio
track selections. Only newly pending clips are validated against the current
source and aggregate server budgets, then inspected and admitted. Relaxed
identity checks never authorize fresh rendering. If pending work fails,
already retained jobs remain in the response.

Each returned clip has its job or a diagnostic and retryability flag.
HTTP artifacts retain ordinary job filenames; use the returned clip-ID/job
mapping to associate them with the plan.
`complete` means every selected clip was admitted to the queue, **not** that
encoding succeeded. Poll admitted jobs through the existing job endpoints.
The queue's owner/global/storage limits still apply; a large collection can be
admitted incrementally. After capacity becomes available, resend the unchanged
request/key to recover admitted jobs and submit the remaining clips. Respect
retry/rate-limit diagnostics rather than polling in a tight loop.

Collection plans are portable JSON documents. This adapter does not introduce
server-side mutable projects or change retention. Keep the source while making
revisions and keep the session private. Downloaded files can be uploaded to the
next tool; delivery URLs continue to require the owner session.

See [the agent workflow](agent-workflow.md), [HTTP schemas](../api/openapi.yaml)
and [the original cutting plans](batch-cutting.md).
