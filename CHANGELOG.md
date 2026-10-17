# Changelog

Notable changes follow semantic versioning. The documented 1.x Go library, CLI and `/api/v1` contracts are stable.

## Unreleased

### Fixed
- Admit scoped collection jobs and their project bindings in one transaction. A full project history returns `project_limit` without leaving hidden queued work; existing jobs and partial submissions remain recoverable.
- Bound project and source-preparation transaction rollback independently of the request deadline, including stalled database responses.
- Resolve HLS variants, segments and initialization maps against the final playlist URL after guarded HTTP redirects.
- Recover TypeScript source-preparation journals interrupted between checkpoint hard-link publication and temporary-link removal, retaining the existing private-file checks.
- Preserve valid `crop: null` inheritance in Python and TypeScript portable project imports and exports.
- Show fixed refusal codes in the self-hosting lifecycle wrapper while retaining exit status 1 and redacting private exception details.

### Changed
- Extract supplied subtitle retiming and shared cue indexing from the server package, preserving public Go aliases, text formats, limits and diagnostics.
- Separate external-tool execution and platform process containment from app configuration, retaining output bounds, cancellation precedence and private error handling.
- Require standalone media-component dependency and compatibility checks on every native target alongside the existing media, HTTP and process-tree acceptance.
- Group existing local source and verification-tooling checks, with separate recovery controls and a release trigger/acceptance map.
- Run native crop and display-geometry engine checks once per platform while preserving their required package/report identities and all media acceptance boundaries.
- Share synthetic release-test fixtures without importing executable test suites or invoking another test case's setup and cleanup.
- Separate pure edit models and shared validation from server configuration, preserving public Go shapes, JSON and diagnostic behavior.
- Move project persistence and transaction handling out of HTTP handlers, retaining authorization, revision checks, lock ordering and database deadlines.
- Check free SDK verification space before builds and remove only successful runs' disposable installations, preserving packages and verification evidence.
- Freeze the 1.4.1 HTTP contract alongside the original baseline, with mandatory compatibility checks for collections, projects and the other existing API operations.
- Share versioned collection and portable-project examples between Go, OpenAPI and both source/installed SDK test suites, preserving their distinct validation responsibilities.
- Document independent project, access, source, job, artifact and local-state lifetimes; require project expiry, revocation and retained-work regression checks on every native target.
- Remove unused legacy SQL schema literals; ordered embedded migrations remain the schema source.
- Exclude local SDK dependencies, build output and generated evidence from the Docker build context.
- Refresh release/SDK support documentation and add a map of development, verification and release commands.

## 1.4.1 - 2026-10-16

### Changed
- HTTP collection validation and submission share one fresh source inspection and a bounded parsed FFmpeg capability snapshot within each request. Every clip retains its own settings, track and geometry checks before any new job is admitted.
- Reused checks verify the original source and configured FFmpeg/FFprobe file identities before and after reuse, preserve cancellation and error ordering, and retain explicit deferred diagnostics when tool capabilities cannot be verified. The single-job preflight remains fresh for each call.
- Native release gates require the batch freshness, clip diagnostics, bounded capability cache, cancellation and changed-file regressions on every supported platform. Production-family media gates also require the batch checks, and native HTTP tests verify that a later clip failure admits no earlier work.
- Bound native glibc allocator arenas on Linux using a trusted CPU-budget-derived value. Preserve the minimal subprocess environment, export settings and output verification; require real-child environment and arena/platform regression gates on every native target.
- Use one video-input decoder thread for accurate joined exports at normalized codec budgets of one or two threads. Preserve encoder/filter budgets, explicit larger budgets, separate audio inputs, MP3, stream copy and single-range paths; require real native export and decoding regressions.
- Retire released idle database-pool connections after 5 seconds, checking every second by default. Preserve explicit connection-string durations, pool capacity and minimums, acquired transactions and the separate worker notification connection; require native service lifecycle regressions.
- On Linux, allow kernel file-cache reclamation after a complete, error-checked draft MP4 attachment response. Preserve the durable file, ownership, ranged continuation, final/source/ZIP serving and other platforms; repeated readers can require disk I/O.
- Advance Python and TypeScript release metadata and package installation links to 1.4.1 while preserving existing SDK APIs and durable project formats.

The [registered 24-run interview comparison](benchmarks/2026-10-16-first-draft/README.md)
meets the first-draft goal in both fresh and primed process strata. By matched-pair
medians, the first
downloaded, independently verified draft is 72.2–74.2% faster, all drafts finish
18.0–19.7% faster, and the final phase finishes 20.3–20.9% faster. The complete
30-output cycle improves by 18.8–20.0%; actual common-parent kernel peak memory
falls 6.4–6.7%, with no higher maximum. All 180 paired final files have identical
encoded SHA256 hashes. These controlled results start with a ready 720p interview;
they exclude upload, transcription and public-network latency. Repeat full draft
downloads can cost additional disk I/O; the separate adverse observation is retained.

## 1.4.0 - 2026-10-16

### Added
- Complete resumable HTTP collections in Python and the Node SDK: validation, bounded incremental admission, saved exact idempotent submissions, polling/backoff and verified streamed downloads. Private owner/state checkpoints recover process interruption without repeating completed work.
- Genuine ranged transfer continuation with prefix hashes, immutable artifact receipts and exclusive publication recovery. Completed files are rehashed; conflicting scope or modified files are refused. Retained revisions count toward project output budgets.
- Collection types, low-level validation/submission, local CLI collection wrappers, selected revisions/drafts and public progress/partial diagnostics in both SDKs. The browser TypeScript entry stays separate from the Node durable runner.
- Required installed-package acceptance for 15 clips, lost committed replies, interrupted transfers, OS-killed consumers, isolated failed-clip repair, scoped review, replay, two revised clips and selected drafts, with independent media/backend-byte verification. See the [1.4.0 release scope](docs/release-1.4.0.md).
- The [real-interview verification](verification/2026-10-15-sdk-collections/README.md) records both installed SDKs completing 15 clips after interrupted transfer/processes, preserving server retry delays and producing exactly 19 render attempts after two revisions/two drafts. Independent comparisons verify source frames and speech; private state and media stay outside Git.
- The [agent-project interview verification](verification/2026-10-16-agent-projects/README.md) binds an immutable candidate and installed packages to lost source/repair replies, an actual worker failure, two browser edits, thirteen retained clips and fifteen supplied subtitle sidecars in each SDK. Source-frame and speech comparisons are independent of SDK maps.
- Durable opt-in source preparation saves the owner and upload/import intent before submission; owner-scoped preparation receipts recover lost committed replies. Full uploaded-byte fingerprints distinguish content identity from provider metadata.
- Scoped agent projects preserve native collection plans and stable clip IDs across SDK/browser review. Single-use temporary handoffs grant a separate browser session access only to the chosen project; revision checks, expiry and revocation preserve owner isolation.
- Explicit failed-clip retry with saved attempt identities and trusted project synchronization retains completed media and adopts reviewed jobs without duplicate rendering. Collection reports retain safe worker causes, job IDs and processing progress.
- Supplied SRT/WebVTT can be transformed into per-clip subtitle sidecars from actual segment maps through Go, CLI and both SDKs. Reordered/repeated cuts, cue clipping, output hashes and bounded expansion are supported without transcription or caption burn-in.
- Installable Python wheel/source and Node package assets with a separate signed SDK manifest join the versioned GitHub release; package-registry publication remains separate. The signed consumer requires all 18 assets for 1.4.0 while retaining the 13-asset contract for earlier releases.

### Changed
- Run complete PR source checks once through the immutable archive resolver instead of starting a duplicate standalone workflow. Preserve all five native/SDK targets, production-family media, race checks and archive recovery.
- Cancel superseded PR archive/hosting runs and stale main source/hosting pushes; retain tag publication and manual verification runs.
- Migration 008 adds bounded preparation receipts, source fingerprints, scoped projects, grants and retry records; migrations 001–007 remain immutable.
- Public self-hosting acceptance now requires the explicit 007→008 transition, including restricted-role CRUD, old-binary refusal and coordinated restoration in both directions; historical transition modes retain their original pins and algorithms.

### Fixed
- Collection cancellation bounds stalled Python JSON reads and TLS handshakes even when socket shutdown does not wake the operation. High-resolution elapsed deadlines keep expired collection waits consistent on Windows Python 3.10; valid fragmented responses remain supported.
- Python stops on an expired completed artifact instead of polling until the collection deadline. Repeated revisions no longer save history that subsequent restoration refuses.
- Node local cancellation bounds and reaps its owned Windows tree-termination helpers before settling, preserving the primary error and earlier completed outputs even when a helper stalls.

## 1.3.0 - 2026-10-15

### Added
- Editable one-source clip collections with stable IDs, independent ordered assemblies, per-clip crop/audio overrides, read-only validation, selected-ID exports and actual source/output mappings in Go and CLI.
- Explicit assembled draft MP4 renditions share final edit decisions while using a 720p height cap and lower-quality H.264 encoding; draft and final filenames remain separate and existing outputs are preserved.
- Owner-protected collection validation/submission adapts to existing durable jobs, with partial queue admission and per-clip request-aware idempotency for safe retries and revisions.
- Reproducible 15-clip agent workflow, independent full-media acceptance and required collection execution of extracted archives on all five native release targets.
- Dependency-free Python and TypeScript SDK candidates for local CLI plans and owner-scoped HTTP workflows: inspection, preflight, explicit idempotent submission, bounded waiting/cancellation and streamed artifact downloads. Typed errors preserve partial results and recovery context. Installed wheel/sdist/npm consumers exercise real media and the API/worker; SDK registry publication remains separate.
- Read-only job preflight through Go `Engine.PreflightPlan`, CLI `cutmy preflight` and owner-protected `POST /api/v1/jobs/preflight`. Shared reports expose normalized settings, selected tracks, output geometry/count and explicit deferred checks; local plans also predict output names without writing files. Inspection checks fresh media and bounded configured-tool capabilities, with independent HTTP rate/admission limits and temporary reader protection.

### Fixed
- Pin SDK distribution tooling to patched setuptools 83.0.0 and wheel 0.46.2 with verified SHA256 hashes; isolated package builds and CI use matching versions. The SDK clients retain no third-party runtime dependencies.

## 1.2.0 - 2026-10-14

### Added
- Normalized upright crop for accurate MP4 in Go advanced options, CLI, saved cutting plans and HTTP jobs; crop precedes the resolution cap and accounts for rotation and sample aspect ratio.
- Private selected-audio waveform windows with bounded streaming peak aggregation, source timestamps, silent-source handling and background preview admission. Stereo channels cannot cancel each other's waveform.
- Opt-in successful-export original-media cleanup, preserving source metadata, jobs and completed artifacts. Migration 6 persists cleanup requests and bounded renewable media-reader leases; active operations settle before physical deletion.
- Owner-protected streaming ZIP downloads of ready files, including prior successful outputs from partial retries of the same source; selected order and fragment names are preserved without recompression or retained archives.
- Optional six-second foreground preview windows with independent cache identity and a bounded wait for overlapping source inspection; the ordinary thirty-second API remains compatible.

### Changed
- Local singleton/batch exports reuse verified input probes, bounded keyframe windows, exact repeated results and parsed subtitle tracks; final MP4 faststart is omitted from temporary join components.
- HLS downloads use a bounded rolling pool and charged job-local segment/init-map reuse; worker input probes can be reused across jobs without skipping output verification.
- Metadata resolution releases PostgreSQL connections during upstream work. Additive migration 7 persists expiring resolving leases; older migration-6 binaries require coordinated recovery rather than an image-only rollback.
- Optional thumbnails load on first authorized GET. Worker preparation overlap is opt-in, and progress updates retain per-item lease fencing.
- The completed [universal-core comparison](benchmarks/2026-10-14-universal-go-core/README.md) records 627 operations with production-family media tools, workload-specific gains, slower joined cases and resource costs. Preparation remains experimental and disabled by default.
- Local batches reuse fresh input inspection and bounded per-track subtitle staging within one operation. Source replacement is rejected, while keyframe, staged-input and output verification remain fresh.
- Accurate joins reserve only actual media producers; shared source-subtitle staging is reserved once per batch and cleaned before releasing its last reservation.
- Fresh bounded structural verification avoids an extra ffprobe process for recognized small owned MP3 and fast-profile MP4 outputs; unsupported, changed or corrupt files retain native verification. Encoding parameters and quality gates are unchanged.
- The [joined-video comparison](benchmarks/2026-10-14-joined-video/README.md) records 132 exports with matched full decoded-media checks. Small joined MP4 and SDK cases improved; heavy 1080p60 encoding remains a bottleneck without a consistent end-to-end speed gain.
- Preview windows carry their original display dimensions in a charged MP4 metadata box, including tool-free cache hits, so clients can position crop presets before preview downscaling.
- A binary supporting only migration 5 refuses migration 6; a migration-6 binary refuses migration 7. Recovery across these upgrades requires a compatible binary or coordinated database and media restoration; image-only rollback does not reverse them.

### Fixed
- Range-count errors show the configured maximum and submitted count, with a correction for splitting a larger selection into multiple jobs.

## 1.1.0 - 2026-10-12

### Added
- Versioned JSON cutting plans and local batch CLI/Go APIs with named intervals and corrective range diagnostics.
- Separate files or one joined result in submitted order, with actual source/output segment manifests; accurate encoding and compatible packet copy remain explicit choices.
- Inspected indexed audio tracks with language/title/channel/rate metadata, selected audio or silent video in both export and selection-aware private previews.
- MKV original-stream copy and clipped/rebased plain text subtitles for local MP4/MKV copy; output manifests explain every included/excluded track.

### Changed
- Idle workers listen for committed queue notifications, retaining periodic claims and reconnect fallback. End-to-end preview/export observations include receipt of the complete output.

### Fixed
- Restrict every media entry in an ordered join independently from its trusted concat plan; changed remote content cannot open nested playlists or bypass file/network restrictions.
- Separate current installation/upgrade/restore acceptance from the immutable historical schema-transition experiment, retaining its original identity and negative controls.

## 1.0.2 - 2026-10-11

### Added
- Migration 5 adds transactional source job/exact-preview pin counts, indexed bounded source TTL selection and persisted bounded drift audits. Release checks require fresh PostgreSQL/native/004→005 upgrade and coordinated restore acceptance.
- Verify all 13 downloaded signed-release assets with one portable consumer command, fixed GitHub certificate policy, exact subjects and source/version hashes, and remote tag object/commit checks before and after verification. No Go SDK, checkout or unpublished gate receipt is required. Publication requires real signature verification of the downloaded assets on all five native targets.
- Add a standalone public Linux amd64 hosting profile with HTTPS, separate administrative/migration/runtime roles, bounded containers, exact project-owned egress rules, immutable image/source identity and coordinated database/media upgrade/restore checks on a disposable CI host.
- Derive deterministic SPDX 2.3 and trust sidecars from the five verified CLI binaries, with exact Go module checksums and shipped notice hashes. Generate tag-only GitHub provenance/SPDX attestations and require certificate identity, every signed subject and the original archive/verdict identity before draft creation; only the original release tag push can sign.
- Verify one immutable source and reproducible five-target archive bundle before release: safe manifests/checksums, nonpublishable PR candidates, native execution of the extracted CLI/API/worker and a separate draft publisher gated by all five results.
- Add fresh SDK/CLI `display_width`/`display_height` inspection fields while preserving coded dimensions and persisted HTTP source responses; document shared stream, geometry, timing and color policies.
- Persisted storage scan/workspace progress and fair deletion retries, with a retryable `storage_initializing` admission diagnostic and an upgrade from the ordered migration predecessor.
- Immutable ordered database migrations with schema/checksum validation and a stable `schema_incompatible` startup diagnostic.
- Required real v0.2.1 upgrade, interrupted-job and coordinated PostgreSQL/media backup/restore acceptance checks.
- Native CI for every shipped Linux, macOS and Windows architecture, exercising actual media, exclusive publication, process-tree cancellation and the PostgreSQL API/worker path without skipped prerequisites.
- Public library error codes with preserved underlying causes, optional JSON CLI diagnostics, and documented exit codes.
- Executable HTTP/OpenAPI contract checks, including byte-length limits, preview priority, download range errors and database unavailability.

### Fixed
- Give the five native signed-release consumer jobs the write-scoped GitHub token required to read an unpublished draft. Their commands remain read-only, with no signing permission; publication still requires actual downloads, signature checks and native execution on all five targets.
- Accept a squash-merged installation revision only when its complete 173-file application inventory exactly matches the reviewed candidate. Record its actual ancestry and retain refusal of changed source, invalid references and dirty checkouts.
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

## 1.0.1 - 2026-10-11

This tag and its signed draft were not published. All five native consumers
received HTTP 403 while reading draft release metadata with a read-scoped GitHub
token. They downloaded no assets, verified no signatures and ran no archived
binary; this was an authorization refusal, not an observed binary failure. The
original tag remains unchanged. Version 1.0.2 corrected draft read access for
the first public 1.x release. The superseded unpublished draft was archived and
removed from the GitHub release page during repository cleanup on 2026-10-15;
the original failure history and tag are retained.

## 1.0.0 - 2026-10-11

This tag was not published as a release. The required main self-hosting check
refused the repository's squash merge because the reviewed candidate was no
longer an ancestor, although all application files matched. Its pipeline was
cancelled before publication. The original tag remains immutable; 1.0.1 corrects
the installation admission check, and 1.0.2 is intended as the first published
1.x release.

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
