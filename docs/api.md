# HTTP contract

[Analysis, loudness, canvases, CSV/chapters and external audio (1.5.0)](media-analysis-and-rendering.md)
describe the new opt-in operations. Their exact request/response shapes are in
[OpenAPI](../api/openapi.yaml). Analysis accepts owned uploaded sources; external
audio requires a second upload owned by the same session.

[Saved plans, joined outputs, selected audio and MKV/subtitles](batch-cutting.md).

[Python and TypeScript SDKs](sdk.md) wrap the local CLI and these HTTP workflows.

Base path: `/api/v1`. Media timestamps are integer milliseconds on the original source timeline. Retention and waiting deadlines are nullable RFC3339 strings. A range is `[start_ms, end_ms)`. Delivery URLs (`preview_url`, `thumbnail_url`, `download_url`) are relative to the same origin. Platform page and embed URLs are absolute HTTPS URLs. Enum names and known error codes are stable machine-readable identifiers; user-facing messages can be localized by a client. Ignore unknown response fields and use a safe generic fallback for unknown additive error codes.

Initialize a session with `GET /session`. The HttpOnly cookie is a bearer capability: keep it private and send it with every subsequent request, including media and downloads. Each request checks ownership; foreign resources return the same 404 as absent ones. Source preparation currently blocks until metadata/staging completes, bounded by the source timeout. Export is asynchronous.

`SOURCE_TIMEOUT` bounds platform metadata preparation. `UPLOAD_TIMEOUT` covers large uploads and direct-file downloads, including admission and media inspection. A stalled transfer returns `504 source_timeout` and removes its partial file. A timeout during final source registration can instead leave a complete source saved; recover a keyed preparation through its receipt, or reconcile the owner source list for an unkeyed legacy call. Increase the upload budget when allowing large files over slow connections. An interrupted upload returns `400 invalid_upload`; exceeding the byte limit returns `413 source_too_large`.

JSON request bodies have a separate ten-second read budget (or an earlier request deadline); stalled bodies return `408 request_timeout`. Rejected bodies are not drained indefinitely. GET/HEAD requests with bodies return `400 invalid_request`. Fully consumed requests retain normal connection reuse.

Current source adds a separate five-second budget to each database operation,
including pool acquisition. Database connectivity/pool failures and this budget
return `503 database_unavailable`; an earlier incoming request deadline returns
`408 request_timeout` on ordinary endpoints. Source/upload/preview processing
retain their distinct outer budgets and `504 source_timeout`. Sequential stages
can take more than five seconds overall; failed source admission has its own
bounded cleanup stage. These changes were introduced in 1.0.2; v0.2.1 used `500 internal`
for ordinary database failures. Readiness retains `503 not_ready`.

| Method | Route | Response |
|---|---|---|
| GET | `/session` | `{ok:true,limits:{max_source_bytes,max_output_bytes,max_fetch_bytes,max_ranges,max_range_ms,max_job_ms}}` and cookie |
| GET | `/sources` | `{sources:[Source...]}`; newest 20 owned sources |
| POST | `/sources` | Source; request `{url}` |
| POST | `/uploads` | Source; multipart field `file` |
| GET | `/sources/{id}` | Source |
| GET | `/sources/{id}/tracks` | Verified local `{tracks:[Track...]}` with absolute indices and language/title metadata; requires owner, returns `tracks_unavailable` for unstaged platform media |
| DELETE | `/sources/{id}` | HTTP 204; source, terminal jobs and outputs removed; active exports return 409 `source_in_use` |
| GET | `/sources/{id}/media` | Authorized source bytes; Range supported |
| GET | `/sources/{id}/preview?start_ms=…` | Aligned 30-second H.264/AAC MP4 window, no-store; source time in X-Preview-Start-MS / X-Preview-End-MS; X-Preview-Cache is hit/miss; optional priority=background yields rendering capacity to foreground seeks |
| GET | `/sources/{id}/storyboard?start_ms=…` | Private 960×90 JPEG search strip for the aligned 30-second window; six 160×90 cells sampled every 5 seconds; X-Storyboard-Frames gives used cells and X-Storyboard-Step-MS is 5000; same interval/cache headers, source cleanup and shared cache bounds as previews; always background admission |
| GET | `/sources/{id}/waveform?start_ms=…` | Private aligned 30-second selected-audio peaks, one per 100 ms; source timestamps, at most 300 peaks and less than 64 KiB; always background admission and bounded preview cache |
| GET | `/sources/{id}/thumbnail` | Authorized, inspected PNG/JPEG; Range supported |
| POST | `/jobs` | Job, HTTP 202 |
| POST | `/jobs/preflight` | Read-only job report with passed/deferred checks; no job or output reservation |
| GET | `/jobs` | `{jobs:[JobSummary...]}`; up to 30 owned exports, active jobs first |
| GET | `/jobs/{id}` | Job; clients may poll once per second |
| POST | `/jobs/{id}/cancel` | Job; running cancellation settles asynchronously |
| GET | `/artifacts/{id}/download` | Authorized attachment; Range supported |
| POST | `/collections/validate` | Read-only normalized per-clip checks; `{source_id,plan,clip_ids?,rendition?}` |
| POST | `/collections` | Per-clip admission/jobs/diagnostics; same body and required `Idempotency-Key` |
| GET | `/source-preparations/{key}` | Owner-scoped durable preparation receipt; 404 if absent |
| POST | `/projects` | Create/recover `{source_id,plan,clip_ids?,rendition?,collection_key,clip_jobs?}`; legacy `job_ids` remains supported |
| GET | `/projects/{id}` | Authorized project, normalized selected requests and attached current/historical jobs |
| PUT | `/projects/{id}` | Save `{revision,plan,clip_ids?,rendition?}`; stale revision gives 409 |
| POST | `/projects/{id}/collections` | Submit saved project selection; optional `{revision,clip_ids}` subset |
| POST | `/projects/{id}/collections/validate` | Read-only scoped validation; same selection/revision shape |
| POST | `/projects/{id}/collections/retry` | Explicit selected terminal repair; `{revision,clip_ids}` and saved `Idempotency-Key` |
| POST | `/projects/{id}/handoffs` | Owner issues `{project_id,token,expires_at}` for a recipient browser |
| POST | `/projects/handoffs/redeem` | Recipient's own session redeems `{token}` into `{project}` |
| DELETE | `/projects/{id}/grants` | Owner revokes project grants and outstanding handoff tokens |
| DELETE | `/projects/{id}` | Owner removes project and its access grants |
| POST | `/subtitles/retime` | Pure supplied SRT/VTT + actual segment maps → sidecar texts and SHA256 |

Health routes `/healthz` and `/readyz` are outside the prefix; readiness checks PostgreSQL. They require no session.

## Recoverable source preparation and scoped projects (1.4.0)

`POST /uploads` and `POST /sources` accept an optional owner-scoped
`Idempotency-Key` of 1–128 visible ASCII bytes (33–126, no spaces); exact `.` and
`..` are reserved path segments. A keyed upload additionally
requires `Upload-SHA256` (64 lowercase hex) and `Upload-Size` (positive bytes).
The server verifies the complete actual upload against this immutable intent.
Unkeyed calls retain their earlier behavior. Initial success is 201 `Source`;
same-key retained replay is 200 `Source`, before another transfer, extractor or
admission. Changed intent gives 409 `source_preparation_conflict`.

`GET /source-preparations/{key}` returns
`{version:1,key,state,source_id?,source?,fingerprint?,retry_after_ms?,error?}`.
States are `processing`, `ready`, `retryable` and `failed`. `processing` includes
a retry delay; `retryable` permits the same POST/key/intent to reclaim the stable
source ID. A crashed in-flight claim can require waiting for
`max(UPLOAD_TIMEOUT,SOURCE_TIMEOUT) + 1 minute`; a committed ready receipt recovers
immediately. Permanent errors expose bounded public diagnostics. A deleted
source is `source_unavailable`, never permission to create a duplicate silently.
Ready receipts remain retained while their source exists.

A source fingerprint is either `{kind:"content",sha256,size_bytes}` for complete
inspected bytes, or `{kind:"provider",provider,provider_id,canonical_url_sha256}`
for metadata-only remote identity. The latter is not a content hash. Keep
preparation keys and private SDK journals out of portable edit documents/logs.

Project creation is idempotent by `(owner,collection_key)`: 201 for creation,
200 for retained same-source replay with the latest edits, 409 if a different
source is requested. Explicit `clip_jobs:[{clip_id,job_id}]` binds each attached
job to the selected clip, including distinct IDs with identical effective cuts.
Each pair must match owner, fixed source and normalized request. Legacy
`job_ids` still requires an unambiguous clip match.
The DTO includes `version`, `id`, `revision`, fixed `source_id`/`source`, `plan`,
selection/rendition, `clips:[{clip_id,request}]` in selected plan order and
`jobs:[{clip_id,request,current,job}]`. Jobs outside current selection or with an
older request may be attached with `current:false`. The server never returns
the private collection key. Updates use revision compare-and-swap;
`project_conflict` refuses stale changes.

Scoped submission accepts only a subset of saved selection and adopts attached
jobs matching the current effective request, including repaired jobs. Explicit
retry persists its immutable attempt before admission and replays a saved key
before checking terminal status. It permits current failed/cancelled jobs or
succeeded jobs with no unexpired ready artifacts, and refuses a succeeded clip
that still has ready media. This does not change legacy `/jobs` idempotency.

A project retains at most 1,024 job bindings. A new scoped job and its binding
commit together; reaching that limit returns `project_limit` with
`retryable:false` before admitting new work. Already attached jobs remain
recoverable at the limit. A partially admitted collection still returns its
accepted jobs and diagnostics for the remaining clips.

A handoff token expires in ten minutes. Its first recipient redeems it with its
own ordinary session into a 24-hour grant. That recipient can recover a lost
redeem reply with the same token until token expiry; another session receives
404. The owner can revoke grants/tokens. Projects expire after seven days and
do not extend source retention. A grant authorizes only the fixed project's
source preview/media/tracks and explicitly attached job/artifact routes under
`/projects/{id}/...`; it grants no ordinary owner identity, source import/delete
or access to unrelated resources. Scoped ZIP downloads contain only the anchor
job's ready artifacts; arbitrary `artifact_id` overrides are refused.

The subtitle endpoint accepts
`{version:1,format:"srt"|"vtt",text,output_format?,outputs:[{clip_id,segments}]}`
and returns `{version:1,outputs:[{clip_id,format,text,cue_count,sha256}]}`. Maps
come from verified actual artifact receipts. It queues no jobs and renders no
media; a session is still required. JSON request cap is 4 MiB, supplied text
512 KiB UTF-8 and combined output text 8 MiB, with additional bounded cue/map
work. Other API request limits remain unchanged. See
[subtitle details](subtitle-sidecars.md), [SDK recovery examples](sdk-collections.md)
and [OpenAPI](../api/openapi.yaml) for complete schemas and limits. These
endpoints require database schema 8.

Binary routes support `206` byte ranges and `304` conditional responses. Failed
`If-Match`/`If-Unmodified-Since` preconditions return `412` with an empty body.
Malformed or unsatisfiable ranges return `416 text/plain`, rather than the JSON
error envelope; unsatisfiable ranges include `Content-Range: bytes */size`, while
malformed ranges may omit it. This applies to media, window previews, thumbnails
and downloads. HEAD has no transported entity. Preview responses after successful
preparation include `X-Preview-Cache`, `X-Preview-Start-MS` and `X-Preview-End-MS`,
including range/precondition responses. Conditional responses still require
owner authorization; headers do not grant a cache ownership bypass.

## Sources

Platform inspection may reuse an owner's verified private metadata for up to
five minutes, bounded by known signing expiry. Reopening does not extend that
deadline. Export verifies source identity/timeline and refreshes a rejected
cached address at most once with the same network budget. Extracted addresses
and headers are never returned to clients.

`kind`: `upload`, `direct`, `youtube`, or `platform`. Additive source fields:

| Field | Meaning |
|---|---|
| `expires_at` | Nullable RFC3339 retention target; null while linked jobs pin the source |
| `retention_blocked` | Whether retained jobs currently protect this source |
| `media_removed` | Optional true after successful-export cleanup; metadata and retained outputs survive, but original media/analysis and new exports return `410 source_removed` |
| `provider` | `upload`, `direct`, `generic`, or one of the [15 recognized providers](providers.md) |
| `provider_video_id` | Actual extractor video identity, or null; Twitch VOD IDs may start with `v` |
| `source_url` | Platform page URL only; null for uploaded/direct files; never an extracted CDN URL |
| `preview_kind` | `native`, `youtube`, `window`, or `none`; `window` uses short server-rendered intervals |
| `preview_url` | Owner-protected native source endpoint, or null |
| `embed_url` | YouTube embed, or null |
| `thumbnail_url` | Owner-protected, bounded and inspected image endpoint, or null |

All nullable fields are always included. `width` and `height` are optional. A source describes the **entire original recording**, not a preselected fragment. Finite HLS duration is measured from the verified full `ENDLIST` manifest; progressive sources use provider metadata. Streams are resolved again for processing; their video identity and timeline are checked against the stored snapshot. Thumbnails expire with the source and require the same session cookie as media. A platform source may advertise `thumbnail_url` before the image is downloaded; this is an optional lazy endpoint, not a promise that an upstream image is available. Import does not wait for image transfer. The first authorized GET has a two-second preparation budget, may refresh expired private metadata, and publishes only a fully inspected PNG/JPEG. An absent, unsupported, oversized or unavailable optional image returns `404 preview_unavailable`; foreign/expired source IDs return 404, and removed original media returns `410 source_removed`. Reader-admission/database failures retain the endpoint's existing error behavior. Failed optional preparation does not remove the source or its exports.

YouTube inputs support watch, short-link, Shorts, recorded-live, and embed forms on explicitly allowed hosts, including mobile/music and nocookie embeds. Recognized platform HTTP/schemeless pastes are upgraded to HTTPS before fetching. YouTube playlist, timestamp, tracking, and fragment parameters do not affect export intervals. Other platform access-essential page parameters are preserved; unknown/direct inputs retain their original signed query and require explicit HTTPS. Known malformed YouTube links return `400 invalid_youtube_url`; other known malformed platform URLs return `400 invalid_source_url` or `unsupported_collection`.

Twitch VODs/clips and finite unencrypted combined HLS recordings, including accessible Rutube videos, use bounded selected-segment staging. The worker downloads intersecting segments plus preceding decode context; FFmpeg sees only staged media bytes, never an untrusted playlist. Every manifest, init map, segment, redirect, and network address uses the guarded HTTPS/public-IP client and shared job transfer budget. Global-to-local timestamp conversion preserves presentation timing and returns artifact bounds on the original timeline.

Initialization-map changes and timestamp discontinuities are supported for up to 64 continuity periods per selected interval when codec configurations match. Periods are remuxed locally and joined using the manifest timeline. Actual live/upcoming/in-progress recordings, DRM/encrypted keys, HLS byte ranges, low-latency parts, gaps, incompatible codec changes, separate HLS audio/video renditions, and DASH fragments are unsupported. Their errors are explicit; no full long-video/live fallback is started. Separate progressive HTTPS video/audio streams remain supported.

## Jobs

`GET /jobs` lists up to 30 exports from the current session, placing active jobs first and sorting newest first within each group. Each summary includes `id`, `source_id`, `title`, `status`, `stage`, `created_at`, `total`, and `files` (retained artifact records with owner-protected download URLs). Opening a different source does not cancel an export or hide it from this history. Deleting a source removes its terminal jobs and associated files; active jobs continue to prevent source deletion. Polling can pause while the history is closed, hidden, or offline.

Request:

```json
{"source_id":"src_...","ranges":[{"start_ms":10000,"end_ms":40000,"label":"Quote"}],"format":"mp4","quality":"1080p","cut_mode":"accurate"}
```

`format`: `mp4` or `mp3`. `quality`: `best`, `1080p`, `720p`. `cut_mode`: `accurate` or `copy`. MP3 requires accurate mode. Copy mode preserves the selected source stream: `best` means original quality. A selected video above an explicitly selected displayed-height cap returns `copy_quality_unsupported`; it is never silently resized or exported above the cap. Each range exports separately in request order. Optional `Idempotency-Key` replays the original submission for this owner; use a fresh key when changing the request.

Current-source media selection, display geometry, variable-frame-rate export and
HDR conversion follow the [shared media policy](media.md). Outputs contain at
most one playable video and one audio stream: default audio, otherwise first
audio. MP3 exports the selected audio only. HTTP source `width`/`height` remain
persisted coded dimensions
for local files and provider format metadata for platforms. Only fresh SDK/CLI
inspection and the verified local `/tracks` response expose additive display dimensions; metadata reads do not re-probe. Local operations may reuse bounded, owner-specific probe metadata for five minutes after checking the original file identity, size and modification time; changed or removed files never use that cache. Preview headers `X-Source-Display-Width` and `X-Source-Display-Height` describe the upright original before preview scaling, including cache hits.

Optional `crop: {x,y,width,height}` uses normalized upright square-pixel display
coordinates. It requires accurate MP4, a rectangle inside the image and at least
two displayed pixels per dimension. Crop runs before the normal resolution cap;
both edges round to the nearest even pixel boundary by at most one displayed pixel.
Other modes fail explicitly instead of silently discarding the crop.

Optional `cleanup_source: true` requests removal of the original file, thumbnail,
preview caches and preparation workspaces after the entire export succeeds and
accepted concurrent operations settle. Omission/false retains legacy behavior.
A concurrent failed/cancelled export cancels that cleanup request. Renewable
reader leases protect in-flight media responses; physical deletion remains
charged and retryable until durable completion. Source metadata, jobs and ready
artifacts retain their own existing retention deadlines; cleanup does not delete
them or extend their lifetime. Explicit `DELETE /sources/{id}` still removes all
terminal outputs. This API policy is distinct from the website's default-on
cleanup checkbox.

Waveform JSON is `{start_ms,end_ms,step_ms:100,has_audio,audio_track?,peaks}`.
`start_ms` is aligned down to 30 seconds and `end_ms` is bounded by the source
end. Peak values are finite numbers in `[0,1]`; silent sources use zeros and
`has_audio:false`. Audio selection matches export/preview: optional absolute
`audio_track` or `no_audio:true`, never both. It shares background admission,
owner authorization, no-store delivery and quota/expiry with preview windows.

The source URL is bounded to 8192 UTF-8 bytes both before and after normalization;
normalization can percent-encode Unicode and expand its byte size. An idempotency
key is bounded to 128 UTF-8 bytes. Labels are bounded to 200 UTF-8 bytes after
trimming Unicode whitespace. OpenAPI expresses these with `x-maxBytes` and the
label's `x-trimSpace`; standard `maxLength` counts characters and cannot express
the byte bound. Schema validity alone does not guarantee admission: source
duration, configured quotas and cross-field export semantics are also checked.

A failed mutation can already have committed before its acknowledgement was
lost. This includes `503 database_unavailable` and earlier caller/source
deadlines reported as `408 request_timeout` or `504 source_timeout`.
Repeat job submission with the **same** `Idempotency-Key` to recover its retained
original job; changing the key can create duplicate work. Recovery happens
before current source/admission limits and the per-owner new-export allowance;
session, origin, JSON/key bounds and ordinary IP/session request guards still apply. The key
retains its return-original meaning even if the request body changes, so use a
fresh key for different work. Once retention cleanup or source deletion removes
the original job, its key no longer recovers it.
For an uncertain source registration, check `GET /sources` in the same session
before deciding whether to repeat the upload/import. That list is bounded to 20
sources. Unkeyed uploads/imports require caller reconciliation; keyed source
preparations use the retained owner/key/intent receipts described above and in the
[lifecycle map](agent-workflow.md#retention-access-and-recovery). Other failed
mutations are not promised to be unapplied; a timeout or connection loss grants
no blanket permission to retry them blindly.

Statuses: `queued`, `running`, `waiting_storage`, `succeeded`, `failed`, `cancelled`. Stage and message describe actual work; no invented percentage is returned. Items have their own status and optional artifact. A failed batch can contain downloadable successful items.

`waiting_storage` is active, consumes both owner/global job slots, and can be
cancelled immediately. `storage_wait_until` is null before the first wait and
then carries a persisted RFC3339 deadline (`STORAGE_WAIT_TIMEOUT`, default
30 minutes). Workers retry admission without occupying an encoding slot; expiry
settles remaining items with `storage_timeout`. A job whose worst-case reservation
cannot fit the configured media budget is rejected up front with HTTP 413
`storage_limit`; reducing the number of fragments can make it admissible.

Artifacts contain nullable `expires_at`. Active jobs pin their existing results
and expose null; after completion the publication-time deadline becomes visible.
Changing `ARTIFACT_TTL` affects newly published outputs, while existing stored
deadlines remain stable. Legacy results without a stored deadline use the current
TTL. Cleanup may lag a deadline by the maintenance interval.

Failed/cancelled items have an additive optional `error_code`, stored with the item without a schema migration. Older jobs can omit this field. Codes are fixed public diagnostics, not signed media addresses or raw subprocess stderr. Clients should localize known codes and use a safe generic fallback for unknown or absent diagnostics. Codes include missing audio (`audio_missing`), unsupported stream/copy settings, platform access/availability, transfer errors, changed/expired sources, source/job timeouts, storage/output limits and server failure; known examples are in [OpenAPI](../api/openapi.yaml), rather than a closed vocabulary. `audio_missing` permits an MP4 retry with the same video and time range. Current-source `hdr_conversion_unavailable` means the configured tools cannot perform required HDR-to-SDR conversion; repeating the same accurate export is not a recovery step. Compatible copy or MP3 avoids video-color conversion.

Worker shutdown and lost lease/database access keep unfinished work recoverable through the existing bounded lease attempts. A job's own processing timeout remains terminal. Explicit cancellation wins a concurrent failure for pending items while already finished items keep their results and diagnostics. Final worker database reads/writes are bounded to five seconds.

Job saves, heartbeats and artifact registration acquire the job row lock before checking the current lease against wall-clock time. A worker that waited past lease expiry cannot save or renew its old lease; unfinished work remains available for recovery.

Optional `items[].progress_ms` measures encoded media time within the requested
range. Legacy or not-yet-measured items omit it; a refreshed input can reset it.
It is not elapsed wall time or an ETA. Even full encoding progress still needs
output verification and atomic artifact/job-item publication; only a succeeded
item has a downloadable result. Duration-weighted clients should count succeeded
items fully and exclude failed/cancelled/queued partial counters.

Copy artifacts report the chosen preceding video keyframe as `actual_start_ms`. `actual_end_ms` is this start plus the measured output-container duration. Packet timing, audio priming, and frame granularity can add small differences. These values describe the output, not a promise of sample-exact audio boundaries. Accurate mode validates duration with a 350 ms tolerance to accommodate codecs/containers; normal video differences are much smaller.

A download capability stays protected by the cookie; artifact IDs alone do not grant access. A removed/expired resource returns 404. Source files may remain pinned while related jobs/results are retained.

## Errors

```json
{"error":{"code":"invalid_export","message":"range must be within the source and end after its start"}}
```

Common codes: `session_required`, `invalid_request`, `request_timeout`, `invalid_url`, `invalid_youtube_url`, `invalid_source_url`, `unsupported_collection`, `live_not_supported`, `platform_access_required`, `platform_unavailable`, `unsupported_stream`, `source_unavailable`, `source_timeout`, `unsupported_media`, `source_too_large`, `source_limit`, `invalid_export`, `job_limit`, `rate_limit`, `origin_rejected`, `not_found`, `expired`, `internal`.

Source admission returns distinct `429` codes: `source_limit` for the session's source count, `source_busy` for another preparation by the same owner, `server_busy` for all preparation slots being occupied, and `storage_limit` for disk/session storage admission. Current-source database unavailability returns `503 database_unavailable`; integrity/unclassified internal failures retain `500 internal`. Owner preparation is reserved before reading quota state, and unsuccessful admission releases its reservation.

Source preparation, upload and preview admission can return `503 storage_initializing`
with `Retry-After: 2` while a retained media directory is being accounted for.
Retry after that delay; completed accounting batches survive process restarts.
Rejected uploads leave their body unread and do not retain a reservation.
`/readyz` continues to report database readiness so the dependent worker can
start and finish accounting. Existing metadata and downloads remain available;
accepted queued jobs wait for their worker to complete storage initialization.

## Executable contract checks (current source)

Install the pinned tooling in an isolated Python environment and use a dedicated
PostgreSQL test database:

```sh
python3 -m venv .venv-contract
.venv-contract/bin/python -m pip install -r scripts/requirements-contract.txt
make api-check PYTHON=.venv-contract/bin/python
# TEST_DATABASE_URL must point to an isolated PostgreSQL database.
make contract-check PYTHON=.venv-contract/bin/python
```

`api-check` validates the OpenAPI document against the vendored official 3.1
schema, its JSON Schemas/local references, and a compatibility baseline.
`contract-check` also exercises actual local HTTP handlers and checks their
requests/responses, including metadata variants, job lifecycle, nullable fields,
Unicode limits, byte ranges, conditional responses, errors and database failure.
Full CI requires these tests instead of silently skipping absent tooling/database.
The checker has negative controls for client-breaking changes; its supported
scope and effective v0.2.1 baseline provenance are in the
[contract baseline guide](../api/COMPATIBILITY.md).

Mutation requests are same-origin. Browser requests from another origin are rejected; non-browser clients without Origin can use the cookie API. There are no wildcard CORS grants. Polling, source admission, export creation, and per-IP mutation limits are enforced independently. Session-protected requests additionally share a ceiling of 1,200 requests per minute per IP, before owner state is allocated; clients behind one NAT share that ceiling. Per-owner request allowance remains 180 per minute.

Known channel/collection URL rejections occur before source preparation: `400 unsupported_collection`. Twitch channel/player-channel URLs return `422 live_not_supported`; use `/videos/{id}` or a clip instead. Live, collection, access, availability and stream errors discovered during metadata/manifest inspection return 422 with their respective codes. Inspection deadlines return 504 `source_timeout`. Foreign or expired thumbnail/source resources return 404 without disclosing ownership.

## Durable storage admission and explicit deletion

Source preparation uses a shared PostgreSQL reservation, rather than a process
mutex alone. Four preparations globally and one per owner may be admitted.
Uploads reserve the known body size (capped at `MAX_SOURCE_BYTES`) or `MAX_SOURCE_BYTES` for unknown sizes. Metadata-only platform imports retain the existing transient
2 MiB admission reservation in the global and owner source budgets, even though
the image download is deferred. This admission requirement can still reject an
import when that headroom is unavailable. The reservation is released when the
metadata-only source commits. A first thumbnail GET obtains a separate bounded
reservation, capped at 2 MiB and the remaining enabled global/owner allowances;
disabled quota limits do not remove the hard image-size ceiling. Its actual
completed image size replaces the reservation atomically. Concurrent GETs wait
within the preparation budget and can reuse the published file. Completed sources
convert the reservation into actual registered file sizes atomically. An unknown
commit outcome retains files and does not proactively release an unresolved
reservation. Recovery consults the authoritative database state: a successful
commit may already have converted that reservation into registered file charges.

Export admission reserves a duration/mode-dependent byte ceiling for every
unpublished output, bounded by `MAX_OUTPUT_BYTES`. Platform jobs also reserve
twice the largest selected interval's staging ceiling, bounded by
`min(MAX_SOURCE_BYTES, MAX_FETCH_BYTES)`. Producers enforce the same ceilings;
publication replaces the corresponding output reservation with actual bytes.
This prevents a partly published batch from waiting for space that its own
retained results occupy without reserving the global file limit for every short
clip. The API exposes fixed diagnostics rather than a promise of estimated capacity.

`DELETE /sources/{id}` requires an explicit user confirmation in clients. It
returns 404 for absent/foreign IDs and 409 `source_in_use` while any related job is
queued, running or waiting for storage. Terminal metadata is deleted atomically;
file tombstones keep actual bytes charged until physical deletion and parent
directory synchronization are confirmed. Failures use persisted exponential
retry delays from 30 seconds to one hour; healthy due files can proceed while
failed paths wait. A repeated metadata deletion does not reset a pending file's
retry schedule. Deletion and job admission take the
same advisory lock before resource row locks, so concurrent submissions cannot
create dangling or accidentally deleted sources. Results copied to a user's
computer are outside server retention.

## Download a ready selection as ZIP

`GET /api/v1/jobs/{id}/download.zip` returns the job's currently ready files in item order. Optional repeated `artifact_id` query values choose up to 128 distinct ready artifacts in the supplied order, including earlier partial-retry successes belonging to the same owner and source. Every artifact is authorized and opened before response bytes are emitted. Missing, expired, foreign or different-source artifacts return an error.

ZIP entries use the fragment label, the original media extension and numeric order prefixes to make duplicate labels safe. Media bytes are streamed without recompression, a persistent archive or a browser-sized aggregate buffer. The original can already be removed. Individual downloads retain range resumption; an interrupted ZIP download restarts from the beginning.

Video preview accepts optional `window_ms=6000` or `window_ms=30000` (default). Position alignment follows the selected length, and short windows have a separate cache identity. The response's source-time headers remain authoritative. Storyboard and waveform windows retain their existing thirty-second contract.

## Core 1.6 delivery and image artifacts

Export requests and collection options add bounded `speed` fractions and `target_size_bytes`; collections add `clear_speed` and `clear_target_size`. WAV/FLAC are accurate audio outputs; M4A is compatible AAC copy. Poster creation uses `POST /artifacts/{id}/poster` on a terminal verified owned output and returns a separate `ImageArtifact`. Read, download and delete it through `/image-artifacts/{id}` and `/image-artifacts/{id}/download`. These operations require the owner session and retain parent/byte/reader bounds. See [controlled export](controlled-export.md) and [schema 9→10](poster-upgrade.md). Existing source thumbnails and media-artifact routes retain their behavior.
