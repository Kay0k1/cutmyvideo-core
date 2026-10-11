# Changelog

Notable changes follow semantic versioning. The project is pre-1.0; the API is versioned under `/api/v1`.

## Unreleased

### Added
- Prepare migration 5 with transactional source job/exact-preview pin counts, indexed bounded source TTL selection and persisted bounded drift audits. This runtime/schema transition requires fresh PostgreSQL/native/upgrade acceptance and matched measurements; existing same-schema hosting evidence does not cover it.
- Verify all 13 downloaded signed-release assets with one portable consumer command, fixed GitHub certificate policy, exact subjects and source/version hashes, and remote tag object/commit checks before and after verification. No Go SDK, checkout or unpublished gate receipt is required; real signing still requires an approved new tag.
- Prepare a standalone public Linux amd64 hosting profile with HTTPS, separate administrative/migration/runtime roles, bounded containers, exact project-owned egress rules, immutable image/source identity and coordinated database/media upgrade/restore checks on a disposable CI host.
- Derive deterministic SPDX 2.3 and trust sidecars from the five verified CLI binaries, with exact Go module checksums and shipped notice hashes. Prepare separate tag-only GitHub provenance/SPDX attestations and require certificate identity, every signed subject and the original archive/verdict identity before draft creation; signing still needs an approved new release tag.
- Verify one immutable source and reproducible five-target archive bundle before release: safe manifests/checksums, nonpublishable PR candidates, native execution of the extracted CLI/API/worker and a separate draft publisher gated by all five results.
- Add fresh SDK/CLI `display_width`/`display_height` inspection fields while preserving coded dimensions and persisted HTTP source responses; document shared stream, geometry, timing and color policies.
- Persisted storage scan/workspace progress and fair deletion retries, with a retryable `storage_initializing` admission diagnostic and an upgrade from the ordered migration predecessor.
- Immutable ordered database migrations with schema/checksum validation and a stable `schema_incompatible` startup diagnostic.
- Required real v0.2.1 upgrade, interrupted-job and coordinated PostgreSQL/media backup/restore acceptance checks.
- Native CI for every shipped Linux, macOS and Windows architecture, exercising actual media, exclusive publication, process-tree cancellation and the PostgreSQL API/worker path without skipped prerequisites.
- Public library error codes with preserved underlying causes, optional JSON CLI diagnostics, and documented exit codes.
- Executable HTTP/OpenAPI contract checks, including byte-length limits, preview priority, download range errors and database unavailability.

### Fixed
- Batch storage scheduling and cursor/known-path reads while retaining the storage fence, binary timestamp decoding, rollback and acknowledged directory progress; use smaller local reads when little deadline remains. Require the new cancellation, permission, non-ISO timestamp and false-preview-pin checks on every native target.
- Allow PostgreSQL to simplify the exact-preview pin existence check without changing the 200-source candidate limit or authoritative pin protection. The matched warm 1,000-row query experiment improves; global preview membership cost and memory at larger sizes remain unmeasured.
- Move the unchanged 32-by-five-millisecond progress test to the explicit `storagecapacity` benchmark tag. Its recorded failures and original gates remain visible; ordinary CI requires deterministic storage correctness, and a separate 1,000-row observation checks the five-second API and ten-second one-shot maintenance budgets.
- Check physical storage headroom on macOS and Windows as well as Linux, including outstanding reservations and safety bytes; refuse failed measurements and invalid paths, and measure the nearest existing parent before data-directory creation.
- Recover retained export submissions by their session/idempotency key before changed admission limits or the new-export allowance can reject a replay; new keys still use current admission checks.
- Warn that an uncertain source registration may already be saved when reporting database failures or request/source deadlines, preserving existing status codes and private commit causes.
- Accept either iptables serialization order of the exact `ESTABLISHED,RELATED` conntrack return rule while still refusing changed states, flags, targets and rule priorities. Report fixed egress refusal codes without exposing private exception text.
- Wait for PostgreSQL's final TCP listener rather than its temporary initialization socket. Bound lifecycle subprocess groups and rediscover only verified owned helper containers after a failed or interrupted client.
- Select the first playable video excluding attached cover art, and default audio otherwise first audio, across local export/preview and HLS staging. Normalize sample aspect ratio and display orientation for accurate video, retain variable export cadence, and convert known PQ/HLG HDR to BT.709 SDR with a fixed `hdr_conversion_unavailable` capability diagnostic; compatible copy preserves selected-stream metadata.
- Preserve rotated HLS display matrices on FFmpeg 5.1 through MP4-compatible staging; refresh historical local-source dimensions before decoder admission and invalidate old preview renditions while retaining their storage charges until eviction. Avoid a reproduced mono AAC encoder hang, with original synthetic inputs and matched decoded-media verification.
- Reuse bounded directory readers within one maintenance call after acknowledged database commits, while verifying persisted prefixes on restart and closing readers before workspace removal. Bound metadata-cache expiry to 200 unlocked rows per query without letting a blocked cache stop committed media deletion; preserve existing progress/retry/accounting records through migration 4.
- Keep interrupted storage accounting batches across restarts; separate retention, reconciliation and workspace-cleanup budgets, and schedule source/artifact/work scan classes fairly. Fence physical deletion against live references, synchronize parent directories before releasing charged bytes, and defer failed paths so they cannot block healthy files.
- Publish uploaded/direct source files, thumbnails and window previews from private staging names; synchronize files and newly created parent directories before database acknowledgement, retaining uncertain commits for recovery.
- Contain Windows media tools in a private process job before they can spawn children and cancel the complete descendant tree. Open Windows directory handles with the write access required for synchronization.
- Honor `LISTEN_ADDR` in API health checks and accept version/revision/build-time metadata in Docker builds.
- Bound API database stages, including connection-pool acquisition and SQL lock waits, without shortening media uploads, exports or downloads. Report unavailable database operations with a stable HTTP 503 diagnostic.
- Synchronize completed output data and required directory entries before acknowledging local exports or registering server artifacts. Preserve exclusive output publication and uncertain database commit recovery.
- Check required filesystem publication capabilities before local encoding rather than discovering unsupported hard links after processing.

## 0.2.1 - 2026-10-10

This is the first published CLI binary release. It includes the open-source
foundation documented in the [0.2.0 entry](https://github.com/Kay0k1/cutmyvideo-core/blob/v0.2.1/CHANGELOG.md#020---2026-10-10): local CLI/library/API, documentation, CI, reproducible
archives, dependency notices and measured metadata-cache optimization. The 0.2.0
source tag remains immutable; its automated media checks failed and no release
assets were published.

### Fixed
- Preserve the selected first keyframe and audio timeline in stream-copy exports on FFmpeg 5.1/6.1, including MKV files with a positive container start timestamp; seek against the absolute input clock and retain timestamps through output trimming.
- Include the first nearby keyframe when probing a zero-start MKV interval, and preserve tiny HLS continuity bridges without shifting or dropping their video/audio content. Synthetic content and audio regressions pass on FFmpeg 5.1.9, 6.1.1 and the current development build.

### Build
- Pin the Go 1.27.2 Docker build image by its verified manifest digest, matching the security-patched CI/release toolchain.
- Gate releases on existing copy/HLS timeline regressions with FFmpeg 5.1 in the pinned Bookworm runtime base, alongside full Ubuntu/FFmpeg 6.1 checks.

## 0.2.0 - 2026-10-10

### Added
- CLI `inspect`, JSON `version`/`--version` build information, usable command help, configurable local operation deadlines, and typed `engine.ErrOutputExists` for safe output handling.
- Reproducible tagged CLI archives for Linux/macOS amd64/arm64 and Windows amd64, embedded build metadata, SHA256 checksums and included Go/dependency license notices. Tag CI creates a draft release after full checks and a two-build reproducibility comparison.
- `make check` / `make check-full`, documentation-link and workflow validation, current RU/EN quick starts, library/API usage, complete configuration, operator backup/restore and contributor/release guides.
- Independent HTTPS API hosting example with a loopback-only Compose overlay and Caddy reverse proxy; the core does not depend on the private product UI repository.
- Bounded 30-second 480p H.264/AAC preview windows for platform sources and browser-codec fallback; a private owner-checked 30-minute idle cache with byte/entry limits, same-window render coalescing and foreground seek priority.
- Long export defaults (12 hours per interval, 24 hours total), separate upload and remote-transfer budgets, duration/mode-based storage reservations and bounded accurate-export bitrate.
- Codec-compatible HLS continuity-period joins preserve long recording timelines across timestamp resets and initialization-map changes.
- Reproducible HLS parsing/window-selection, storage admission and saturated rate-limit benchmarks with measured CPU/allocation results.
- Fast CPU encoding profile (ultrafast/CRF 18 with CABAC), configurable compact profile, bounded thread settings, CLI profile/thread options and a 128 MiB soft Go runtime memory limit in Docker; measured speed/file-size tradeoffs are documented.
- Optional measured `items[].progress_ms` during encoding, bounded/redacted progress parsing and safe per-stage timing logs.
- Private, owner-specific five-minute platform metadata cache with signed-expiry safety margins, database-backed miss coalescing and repeated-import reuse without extending the original deadline.
- Optional fixed `items[].error_code` export diagnostics, including absent audio, source changes, transfer failures, timeouts, output/storage limits and incompatible copy settings; legacy items remain readable and private subprocess output stays redacted.
- Independent `cutmy worker-healthcheck` based on successful queue/lease access within 30 seconds and a private, empty container-local marker; the deployment example checks the worker separately from the API.
- Recognized page adapters for 15 platforms, including Twitch VODs/clips, Rutube, TikTok, Instagram Reels and YouTube Shorts, with secure HTTP/schemeless normalization on known hosts and preserved access-essential parameters.
- Additive provider identity, source-page and preview-kind metadata, plus bounded owner-protected JPEG/PNG thumbnails with source retention.
- Finite unencrypted HLS range staging for combined MPEG-TS/fMP4 streams and audio-only export. Every manifest/variant/map/segment uses guarded HTTPS/public-IP checks, a shared byte budget and bounded parsing.
- Original-timeline HLS exports account for presentation timestamps after local remux; tests verify first decoded frames, nonzero keyframe-copy bounds and an intentional audio delay against original source samples.
- Explicit live/collection/access/unavailability/unsupported-stream errors; separate HLS renditions and unsupported tags fail without a long-download fallback.
- Initial cutmyvideo-core Go engine, local `cutmy clip` CLI, HTTP API, and independent media worker.
- Uploaded media, bounded full staging of direct HTTPS media, and supported progressive platform streams via yt-dlp and a guarded range relay.
- Multiple manual intervals with separate H.264/AAC MP4 or MP3 outputs.
- Accurate exports and keyframe-aligned stream-copy exports with measured output bounds.
- PostgreSQL jobs with idempotent submission, cancellation, leases, retry recovery, and worker write fencing.
- Anonymous capability-session ownership checks for sources, jobs, previews, and downloads.
- URL/destination validation, explicit extraction proxy, demuxer/protocol allowlists, safe MIME, mutation-origin checks, and bounded processing/storage admission.
- Retention cleanup for terminal jobs, artifacts, unpinned sources, abandoned workspaces, and old unregistered files after crashes.
- Russian and English documentation, OpenAPI contract, Docker deployment example, and automated Go/media/PostgreSQL integration checks.
- Canonical YouTube URL parsing for watch, short-link, Shorts, recorded-live, mobile/music, and embed forms; pasted HTTP/schemeless YouTube links are safely upgraded without playlist/tracking/timestamp parameters.
- Bounded private subprocess diagnostics classify failures into fixed categories without logging raw CDN URLs, credentials, or media paths; an unexplained nonzero exit is not automatically retried.

### Fixed
- Drain in-flight HTTP requests during shutdown, with a ten-second bound and forced connection close after the deadline; database access remains available until handlers settle.
- Reject stray CLI arguments and malformed timestamp forms before media processing; expose existing-output errors through `errors.Is` even when concurrent publication wins.
- Reuse bounded gzip encoder workspaces during repeated metadata-cache fills, reducing CPU/allocation cost while retaining byte-identical payloads, independent output ownership and existing corruption/size checks; before/after raw results are included.
- Preserve live HTTP connection contexts after fully consumed JSON and multipart bodies; close connections with unread bodies before sending a rejection, avoiding intermittent preview failures and false storage-limit errors behind a keep-alive proxy.
- Parse HLS playlists with one validated base URL and bounded preallocation; locate selected segments with binary search while preserving network and timeline validation.
- Read storage directories in bounded unsorted batches; continue quota scans after individual files disappear.
- Inspect only required FFprobe fields, avoiding large unused metadata and rejecting fewer otherwise valid media files.
- Bound default PostgreSQL pools to four connections per process while preserving explicit maximum/minimum settings.
- Cancel and reap CLI media processes on SIGINT/SIGTERM instead of leaving them running after the command exits.
- Reserve source preparation before quota reads and preserve distinct capacity, storage and database failures in API responses.
- Bound source admission, multipart transfer and inspection with the configured source timeout; remove stalled partial uploads and distinguish interrupted transfers from oversized media.
- Check worker lease expiry after acquiring the job row lock; reject stale saves, heartbeats and artifact registration even when database lock waits outlive the lease.
- Share connection time among ordered address attempts so a slow IPv6 address leaves time for IPv4 within the original DNS/dial deadline.
- Atomically register artifacts and completed job items under lease fencing; retain files on uncertain commit acknowledgements for database recovery.
- Refresh rejected cached media addresses once without reusing the cache, resetting transfer limits or repeating completed clips; preserve identity, timeline and inspection deadlines.
- Reuse fixed relay/HLS buffers, preparse destination rules, share a bounded DNS/dial fallback deadline and close active tunnels on cancellation.
- Separate bounded retention maintenance from queue claims, bound lease/claim access, and check actual Linux free space with cancellable, early-exit logical storage scans.
- Bound optional thumbnail fetches to two seconds and index retention joins on job sources/artifact jobs.
- Prepare recordings with large automatic-caption metadata by omitting unused caption URL matrices before the bounded extractor response; preserve complete stream metadata, collection envelopes and existing inspection limits.
- Rank complete compatible platform stream pairs before quality: high-bitrate video-only HLS no longer prevents an available progressive video/audio export at the requested resolution.
- Keep unfinished jobs recoverable when a worker shuts down during processing or artifact publication; completed fragments survive recovery and stale leases remain fenced. Job deadlines and user cancellation retain terminal states.
- Resolve cancellation concurrent with a failure using previously pending item identities, preserving already completed/failed items and assigning cancelled diagnostics only to pending work.
- Bound final worker database reads/writes to five seconds; interrupted lease access remains recoverable within the existing attempt limit.
- Enforce explicit resolution caps for copy exports without silent re-encoding or oversized-resolution results; detect output-size truncation before reporting malformed media.

### Security
- Updated CI/release/runtime builds to Go 1.27.2, module minimum to Go 1.26.9, `golang.org/x/text` to 0.41.0 and `golang.org/x/sync` to 0.22.0 after reachable vulnerability scanning.
- Bound rate-table growth and expiry-scan frequency; apply IP admission before owner allocation and preserve existing users at capacity.
- Limit JSON reads to ten seconds and stop unread request-body draining after rejection, including stalled multipart and early authentication/origin/rate failures.
- Limit upstream response headers to 64 KiB and exclude local environment variants/cookie files from build context.
- Updated pgx to 5.9.2 and golang.org/x/text to 0.39.0 after reachable vulnerability scanning; CI repeats the scan.
- Require nonempty authenticated proxy credentials for Python urllib compatibility; regression verifies that private destinations remain blocked after authentication.
- Reject malformed YouTube IDs, spoofed or unsupported YouTube hosts, embedded credentials, and nonstandard ports before source preparation or network access.

### Operational boundaries
- Public hosting uses one API with PostgreSQL-coordinated queue/storage state and local shared media storage; each worker has a bounded pool of 1–8 jobs. Multiple API instances require a shared IP limiter.
- Linux media/runtime behavior is verified in CI. macOS/Windows CLI archives are cross-built; native runtime behavior is not yet verified.
- Live capture, DASH fragments, encrypted/separate-rendition HLS, DRM, authenticated sources and S3 are not implemented. Source-provider recognition does not guarantee access to every video.
