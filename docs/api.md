# HTTP contract

Base path: `/api/v1`. Timestamps are integer milliseconds on the original source timeline. A range is `[start_ms, end_ms)`. Delivery URLs (`preview_url`, `thumbnail_url`, `download_url`) are relative to the same origin. Platform page and embed URLs are absolute HTTPS URLs. Enum names and error codes are stable machine-readable identifiers; user-facing messages can be localized by a client.

Initialize a session with `GET /session`. The HttpOnly cookie is a bearer capability: keep it private and send it with every subsequent request, including media and downloads. Each request checks ownership; foreign resources return the same 404 as absent ones. Source preparation currently blocks until metadata/staging completes, bounded by the source timeout. Export is asynchronous.

| Method | Route | Response |
|---|---|---|
| GET | `/session` | `{ok:true}` and cookie |
| POST | `/sources` | Source; request `{url}` |
| POST | `/uploads` | Source; multipart field `file` |
| GET | `/sources/{id}` | Source |
| GET | `/sources/{id}/media` | Authorized source bytes; Range supported |
| GET | `/sources/{id}/thumbnail` | Authorized, inspected PNG/JPEG; Range supported |
| POST | `/jobs` | Job, HTTP 202 |
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
| `provider` | `upload`, `direct`, `generic`, or one of the [15 recognized providers](providers.md) |
| `provider_video_id` | Actual extractor video identity, or null; Twitch VOD IDs may start with `v` |
| `source_url` | Platform page URL only; null for uploaded/direct files; never an extracted CDN URL |
| `preview_kind` | `native`, `youtube`, or `none`; `none` means manual timing with a source card |
| `preview_url` | Owner-protected native source endpoint, or null |
| `embed_url` | YouTube embed, or null |
| `thumbnail_url` | Owner-protected, bounded and inspected image endpoint, or null |

All nullable fields are always included. `width` and `height` are optional. A source describes the **entire original recording**, not a preselected fragment. Finite HLS duration is measured from the verified full `ENDLIST` manifest; progressive sources use provider metadata. Streams are resolved again for processing; their video identity and timeline are checked against the stored snapshot. Thumbnails expire with the source and require the same session cookie as media.

YouTube inputs support watch, short-link, Shorts, recorded-live, and embed forms on explicitly allowed hosts, including mobile/music and nocookie embeds. Recognized platform HTTP/schemeless pastes are upgraded to HTTPS before fetching. YouTube playlist, timestamp, tracking, and fragment parameters do not affect export intervals. Other platform access-essential page parameters are preserved; unknown/direct inputs retain their original signed query and require explicit HTTPS. Known malformed YouTube links return `400 invalid_youtube_url`; other known malformed platform URLs return `400 invalid_source_url` or `unsupported_collection`.

Twitch VODs/clips and finite unencrypted combined HLS recordings, including accessible Rutube videos, use bounded selected-segment staging. The worker downloads intersecting segments plus preceding decode context; FFmpeg sees only staged media bytes, never an untrusted playlist. Every manifest, init map, segment, redirect, and network address uses the guarded HTTPS/public-IP client and shared job transfer budget. Global-to-local timestamp conversion preserves presentation timing and returns artifact bounds on the original timeline.

Actual live/upcoming/in-progress recordings, DRM/encrypted keys, HLS byte ranges, low-latency parts, gaps, changing init maps/discontinuities inside the selected range, separate HLS audio/video renditions, and DASH fragments are unsupported. Their errors are explicit; no full long-video/live fallback is started. Separate progressive HTTPS video/audio streams remain supported.

## Jobs

Request:

```json
{"source_id":"src_...","ranges":[{"start_ms":10000,"end_ms":40000,"label":"Quote"}],"format":"mp4","quality":"1080p","cut_mode":"accurate"}
```

`format`: `mp4` or `mp3`. `quality`: `best`, `1080p`, `720p`. `cut_mode`: `accurate` or `copy`. MP3 requires accurate mode. Copy mode preserves source resolution: `best` means original quality. A source above an explicitly selected resolution cap returns `copy_quality_unsupported`; it is never silently resized or exported above the cap. Each range exports separately in request order. Optional `Idempotency-Key` replays the original submission for this owner; use a fresh key when changing the request.

Statuses: `queued`, `running`, `succeeded`, `failed`, `cancelled`. Stage and message describe actual work; no invented percentage is returned. Items have their own status and optional artifact. A failed batch can contain downloadable successful items.

Failed/cancelled items have an additive optional `error_code`, stored with the item without a schema migration. Older jobs can omit this field. Codes are fixed public diagnostics, not signed media addresses or raw subprocess stderr. Clients should localize known codes and use a safe generic fallback for unknown or absent diagnostics. Codes include missing audio (`audio_missing`), unsupported stream/copy settings, platform access/availability, transfer errors, changed/expired sources, source/job timeouts, storage/output limits and server failure; the complete vocabulary is in [OpenAPI](../api/openapi.yaml). `audio_missing` permits an MP4 retry with the same video and time range.

Worker shutdown and lost lease/database access keep unfinished work recoverable through the existing bounded lease attempts. A job's own processing timeout remains terminal. Explicit cancellation wins a concurrent failure for pending items while already finished items keep their results and diagnostics. Final worker database reads/writes are bounded to five seconds.

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

Common codes: `session_required`, `invalid_request`, `invalid_url`, `invalid_youtube_url`, `invalid_source_url`, `unsupported_collection`, `live_not_supported`, `platform_access_required`, `platform_unavailable`, `unsupported_stream`, `source_unavailable`, `source_timeout`, `unsupported_media`, `source_too_large`, `source_limit`, `invalid_export`, `job_limit`, `rate_limit`, `origin_rejected`, `not_found`, `expired`, `internal`.

Mutation requests are same-origin. Browser requests from another origin are rejected; non-browser clients without Origin can use the cookie API. There are no wildcard CORS grants. Polling, source admission, export creation, and per-IP mutation limits are enforced independently.

Known channel/collection URL rejections occur before source preparation: `400 unsupported_collection`. Twitch channel/player-channel URLs return `422 live_not_supported`; use `/videos/{id}` or a clip instead. Live, collection, access, availability and stream errors discovered during metadata/manifest inspection return 422 with their respective codes. Inspection deadlines return 504 `source_timeout`. Foreign or expired thumbnail/source resources return 404 without disclosing ownership.
