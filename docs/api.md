# HTTP contract

Base path: `/api/v1`. Timestamps are integer milliseconds on the original source timeline. A range is `[start_ms, end_ms)`. URLs in responses are relative to the same origin. Enum names and error codes are stable machine-readable identifiers; user-facing messages can be localized by a client.

Initialize a session with `GET /session`. The HttpOnly cookie is a bearer capability: keep it private and send it with every subsequent request, including media and downloads. Each request checks ownership; foreign resources return the same 404 as absent ones. Source preparation currently blocks until metadata/staging completes, bounded by the source timeout. Export is asynchronous.

| Method | Route | Response |
|---|---|---|
| GET | `/session` | `{ok:true}` and cookie |
| POST | `/sources` | Source; request `{url}` |
| POST | `/uploads` | Source; multipart field `file` |
| GET | `/sources/{id}` | Source |
| GET | `/sources/{id}/media` | Authorized source bytes; Range supported |
| POST | `/jobs` | Job, HTTP 202 |
| GET | `/jobs/{id}` | Job; clients may poll once per second |
| POST | `/jobs/{id}/cancel` | Job; running cancellation settles asynchronously |
| GET | `/artifacts/{id}/download` | Authorized attachment; Range supported |

Health routes `/healthz` and `/readyz` are outside the prefix; readiness checks PostgreSQL. They require no session.

## Sources

`kind`: `upload`, `direct`, `youtube`, or `platform`. `preview_url` is present for staged files. `embed_url` may be present for YouTube; `thumbnail_url` is optional. These nullable fields are always included. `width` and `height` are optional. A source describes the current media snapshot; platform streams are resolved again for processing and their provider ID/duration are checked for changes.

## Jobs

Request:

```json
{"source_id":"src_...","ranges":[{"start_ms":10000,"end_ms":40000,"label":"Quote"}],"format":"mp4","quality":"1080p","cut_mode":"accurate"}
```

`format`: `mp4` or `mp3`. `quality`: `best`, `1080p`, `720p`. `cut_mode`: `accurate` or `copy`. MP3 requires accurate mode. Copy mode does not resize local input. Each range exports separately in request order. Optional `Idempotency-Key` replays the original submission for this owner; use a fresh key when changing the request.

Statuses: `queued`, `running`, `succeeded`, `failed`, `cancelled`. Stage and message describe actual work; no invented percentage is returned. Items have their own status and optional artifact. A failed batch can contain downloadable successful items.

Copy artifacts report the chosen preceding video keyframe as `actual_start_ms`. `actual_end_ms` is this start plus the measured output-container duration. Packet timing, audio priming, and frame granularity can add small differences. These values describe the output, not a promise of sample-exact audio boundaries. Accurate mode validates duration with a 350 ms tolerance to accommodate codecs/containers; normal video differences are much smaller.

A download capability stays protected by the cookie; artifact IDs alone do not grant access. A removed/expired resource returns 404. Source files may remain pinned while related jobs/results are retained.

## Errors

```json
{"error":{"code":"invalid_export","message":"range must be within the source and end after its start"}}
```

Common codes: `session_required`, `invalid_request`, `invalid_url`, `source_unavailable`, `source_timeout`, `unsupported_source`, `unsupported_media`, `source_too_large`, `source_limit`, `invalid_export`, `job_limit`, `rate_limit`, `origin_rejected`, `not_found`, `expired`, `internal`.

Mutation requests are same-origin. Browser requests from another origin are rejected; non-browser clients without Origin can use the cookie API. There are no wildcard CORS grants. Polling, source admission, export creation, and per-IP mutation limits are enforced independently.
