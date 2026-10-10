# HTTP contract

Base path: `/api/v1`. Media timestamps are integer milliseconds on the original source timeline. Retention and waiting deadlines are nullable RFC3339 strings. A range is `[start_ms, end_ms)`. Delivery URLs (`preview_url`, `thumbnail_url`, `download_url`) are relative to the same origin. Platform page and embed URLs are absolute HTTPS URLs. Enum names and error codes are stable machine-readable identifiers; user-facing messages can be localized by a client.

Initialize a session with `GET /session`. The HttpOnly cookie is a bearer capability: keep it private and send it with every subsequent request, including media and downloads. Each request checks ownership; foreign resources return the same 404 as absent ones. Source preparation currently blocks until metadata/staging completes, bounded by the source timeout. Export is asynchronous.

`SOURCE_TIMEOUT` bounds platform metadata preparation. `UPLOAD_TIMEOUT` covers large uploads and direct-file downloads, including admission and media inspection. A stalled upload returns `504 source_timeout` and removes its partial file. Increase the upload budget when allowing large files over slow connections. An interrupted upload returns `400 invalid_upload`; exceeding the byte limit returns `413 source_too_large`.

JSON request bodies have a separate ten-second read budget (or an earlier request deadline); stalled bodies return `408 request_timeout`. Rejected bodies are not drained indefinitely. GET/HEAD requests with bodies return `400 invalid_request`. Fully consumed requests retain normal connection reuse.

| Method | Route | Response |
|---|---|---|
| GET | `/session` | `{ok:true,limits:{max_source_bytes,max_output_bytes,max_fetch_bytes,max_ranges,max_range_ms,max_job_ms}}` and cookie |
| GET | `/sources` | `{sources:[Source...]}`; newest 20 owned sources |
| POST | `/sources` | Source; request `{url}` |
| POST | `/uploads` | Source; multipart field `file` |
| GET | `/sources/{id}` | Source |
| DELETE | `/sources/{id}` | HTTP 204; source, terminal jobs and outputs removed; active exports return 409 `source_in_use` |
| GET | `/sources/{id}/media` | Authorized source bytes; Range supported |
| GET | `/sources/{id}/preview?start_ms=…` | Aligned 30-second H.264/AAC MP4 window, no-store; source time in X-Preview-Start-MS / X-Preview-End-MS; X-Preview-Cache is hit/miss; optional priority=background yields rendering capacity to foreground seeks |
| GET | `/sources/{id}/thumbnail` | Authorized, inspected PNG/JPEG; Range supported |
| POST | `/jobs` | Job, HTTP 202 |
| GET | `/jobs` | `{jobs:[JobSummary...]}`; up to 30 owned exports, active jobs first |
| GET | `/jobs/{id}` | Job; clients may poll once per second |
| POST | `/jobs/{id}/cancel` | Job; running cancellation settles asynchronously |
| GET | `/artifacts/{id}/download` | Authorized attachment; Range supported |

Health routes `/healthz` and `/readyz` are outside the prefix; readiness checks PostgreSQL. They require no session.

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
| `provider` | `upload`, `direct`, `generic`, or one of the [15 recognized providers](providers.md) |
| `provider_video_id` | Actual extractor video identity, or null; Twitch VOD IDs may start with `v` |
| `source_url` | Platform page URL only; null for uploaded/direct files; never an extracted CDN URL |
| `preview_kind` | `native`, `youtube`, `window`, or `none`; `window` uses short server-rendered intervals |
| `preview_url` | Owner-protected native source endpoint, or null |
| `embed_url` | YouTube embed, or null |
| `thumbnail_url` | Owner-protected, bounded and inspected image endpoint, or null |

All nullable fields are always included. `width` and `height` are optional. A source describes the **entire original recording**, not a preselected fragment. Finite HLS duration is measured from the verified full `ENDLIST` manifest; progressive sources use provider metadata. Streams are resolved again for processing; their video identity and timeline are checked against the stored snapshot. Thumbnails expire with the source and require the same session cookie as media.

YouTube inputs support watch, short-link, Shorts, recorded-live, and embed forms on explicitly allowed hosts, including mobile/music and nocookie embeds. Recognized platform HTTP/schemeless pastes are upgraded to HTTPS before fetching. YouTube playlist, timestamp, tracking, and fragment parameters do not affect export intervals. Other platform access-essential page parameters are preserved; unknown/direct inputs retain their original signed query and require explicit HTTPS. Known malformed YouTube links return `400 invalid_youtube_url`; other known malformed platform URLs return `400 invalid_source_url` or `unsupported_collection`.

Twitch VODs/clips and finite unencrypted combined HLS recordings, including accessible Rutube videos, use bounded selected-segment staging. The worker downloads intersecting segments plus preceding decode context; FFmpeg sees only staged media bytes, never an untrusted playlist. Every manifest, init map, segment, redirect, and network address uses the guarded HTTPS/public-IP client and shared job transfer budget. Global-to-local timestamp conversion preserves presentation timing and returns artifact bounds on the original timeline.

Initialization-map changes and timestamp discontinuities are supported for up to 64 continuity periods per selected interval when codec configurations match. Periods are remuxed locally and joined using the manifest timeline. Actual live/upcoming/in-progress recordings, DRM/encrypted keys, HLS byte ranges, low-latency parts, gaps, incompatible codec changes, separate HLS audio/video renditions, and DASH fragments are unsupported. Their errors are explicit; no full long-video/live fallback is started. Separate progressive HTTPS video/audio streams remain supported.

## Jobs

`GET /jobs` lists up to 30 exports from the current session, placing active jobs first and sorting newest first within each group. Each summary includes `id`, `source_id`, `title`, `status`, `stage`, `created_at`, `total`, and `files` (retained artifact records with owner-protected download URLs). Opening a different source does not cancel an export or hide it from this history. Deleting a source removes its terminal jobs and associated files; active jobs continue to prevent source deletion. Polling can pause while the history is closed, hidden, or offline.

Request:

```json
{"source_id":"src_...","ranges":[{"start_ms":10000,"end_ms":40000,"label":"Quote"}],"format":"mp4","quality":"1080p","cut_mode":"accurate"}
```

`format`: `mp4` or `mp3`. `quality`: `best`, `1080p`, `720p`. `cut_mode`: `accurate` or `copy`. MP3 requires accurate mode. Copy mode preserves source resolution: `best` means original quality. A source above an explicitly selected resolution cap returns `copy_quality_unsupported`; it is never silently resized or exported above the cap. Each range exports separately in request order. Optional `Idempotency-Key` replays the original submission for this owner; use a fresh key when changing the request.

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

Failed/cancelled items have an additive optional `error_code`, stored with the item without a schema migration. Older jobs can omit this field. Codes are fixed public diagnostics, not signed media addresses or raw subprocess stderr. Clients should localize known codes and use a safe generic fallback for unknown or absent diagnostics. Codes include missing audio (`audio_missing`), unsupported stream/copy settings, platform access/availability, transfer errors, changed/expired sources, source/job timeouts, storage/output limits and server failure; the complete vocabulary is in [OpenAPI](../api/openapi.yaml). `audio_missing` permits an MP4 retry with the same video and time range.

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

Source admission returns distinct `429` codes: `source_limit` for the session's source count, `source_busy` for another preparation by the same owner, `server_busy` for all preparation slots being occupied, and `storage_limit` for disk/session storage admission. Database failures remain `500 internal`. Owner preparation is reserved before reading quota state, and unsuccessful admission releases its reservation.

Mutation requests are same-origin. Browser requests from another origin are rejected; non-browser clients without Origin can use the cookie API. There are no wildcard CORS grants. Polling, source admission, export creation, and per-IP mutation limits are enforced independently. Session-protected requests additionally share a ceiling of 1,200 requests per minute per IP, before owner state is allocated; clients behind one NAT share that ceiling. Per-owner request allowance remains 180 per minute.

Known channel/collection URL rejections occur before source preparation: `400 unsupported_collection`. Twitch channel/player-channel URLs return `422 live_not_supported`; use `/videos/{id}` or a clip instead. Live, collection, access, availability and stream errors discovered during metadata/manifest inspection return 422 with their respective codes. Inspection deadlines return 504 `source_timeout`. Foreign or expired thumbnail/source resources return 404 without disclosing ownership.

## Durable storage admission and explicit deletion

Source preparation uses a shared PostgreSQL reservation, rather than a process
mutex alone. Four preparations globally and one per owner may be admitted.
Uploads reserve the known body size (capped at `MAX_SOURCE_BYTES`) or `MAX_SOURCE_BYTES` for unknown sizes; metadata-only platform preparations reserve only the bounded
2 MiB thumbnail in both the global and owner source budgets. Completed sources
convert the reservation into actual registered file sizes atomically. An unknown
commit outcome retains files and its reservation until safe reconciliation.

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
file tombstones keep actual bytes charged until physical deletion succeeds.
Failures are retried by periodic maintenance. Deletion and job admission take the
same advisory lock before resource row locks, so concurrent submissions cannot
create dangling or accidentally deleted sources. Results copied to a user's
computer are outside server retention.
