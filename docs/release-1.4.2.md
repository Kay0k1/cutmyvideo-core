# 1.4.2: core audit and compatibility fixes

This maintenance release publishes the completed 1.4.1 audit and the original-audio
fix already verified in production. It adds no new editing operations. Existing
Go library shapes, CLI JSON, `/api/v1`, SDK methods and durable project formats
remain compatible; ordered database migrations 001–008 are unchanged.

## User-visible fixes

- Preserve Python request and job-wait deadline diagnostics, including the last
  job report, when the next request or poll cannot start before the deadline.
- Recover TypeScript source preparation interrupted between checkpoint hard-link
  publication and temporary-link removal; preserve private-file validation.
- Preserve explicit `crop: null` inheritance in both portable-project SDKs.
- Admit scoped collection jobs and project bindings atomically. Full project
  history returns `project_limit` without leaving hidden queued work; retained
  and partial submissions remain recoverable.
- Bound project and source-preparation transaction rollback independently of an
  expired request, including stalled database responses.
- Resolve HLS children against the final playlist URL after guarded redirects.
- Prefer original platform audio over automatic dubs for supported inputs,
  preserve extractor selection metadata during cache refresh and invalidate
  previews produced under the previous audio-selection policy. Provider access
  and availability remain external dependencies.
- Reap owned Unix descendants even when their parent exits first, including
  redirected standard streams. Existing cancellation and output limits remain.
- Redact private self-hosting lifecycle failures behind fixed refusal codes.

## Internal boundaries and verification

The local media engine no longer depends on the server package. Pure edit and
subtitle timing, metadata and geometry, tool/process execution, interval and
joined export coordination, preflight and operation-local sessions have their
own boundaries. Service adapters retain existing diagnostic ordering, identity
checks, stream/copy timing, resource budgets and publication rules.

HTTP handlers, project persistence, previews, store operations and worker
lifecycle code are split into focused files. Unused SQL literals are removed;
embedded ordered migrations remain authoritative. Frozen API baselines and
shared project examples cover the existing interfaces and lifecycle rules.

`golang.org/x/sys` advances to 0.44.0 with matching notices and license checks.
SDK verification checks free space before builds and removes only successful
runs' disposable installations while retaining packages and receipts. Native
metadata fixtures receive a version- and checksum-pinned extractor. Existing
mandatory media, storage, lifecycle and process checks remain required on all
five platforms.

Release acceptance follows the [signed release protocol](releases.md): fresh
source and installed-package checks, five-platform native and packaged CLI
acceptance, production-family media, historical upgrade/restore and public
self-hosting, followed by actual downloaded-asset signature checks and offline
SDK installation on every supported target. A source version or unsigned PR
candidate alone is not a signed release.

## Installation and scope

Use the immutable `v1.4.2` GitHub release after its publication gates complete.
It contains five CLI archives and the Python wheel/source and Node package,
with manifests, checksums and retained Sigstore bundles. See
[SDK installation](sdk.md#install-versioned-packages) and
[consumer verification](releases.md#consumer-verification-of-a-new-signed-release).
SDK publication here refers to these GitHub assets; PyPI/npm publication is a
separate process. FFmpeg/ffprobe remain external CLI dependencies.

The 1.4.1 performance comparison remains a result for its recorded candidate,
inputs and tools. This maintenance release does not claim a new speedup or
retroactively replace that evidence. Supplied subtitle retiming/sidecars remain
available as before; caption rendering and transcription are outside this release.
Production website deployment is separate from publishing these packages.
