# 1.3.0: editable clip collections

This minor release adds editable clip collections and read-only export
preflight. Release archives follow the signing and downloaded-asset protocol
described in [release instructions](releases.md).

An agent or editor can save a collection of independent clips from one source,
assemble each from ordered fragments, inspect the plan, produce draft edits and
export selected clip IDs after a revision. Per-clip overrides preserve crop and
audio decisions; JSON results map outputs back to original source intervals.

Existing CLI, Go and `/api/v1/jobs` workflows remain supported. New collection
operations use the existing media engine and durable queue. HTTP batches expose
partial queue admission and safe request-aware retries instead of exceeding
owner concurrency limits. Drafts are explicitly lower-quality accurate MP4
exports and have separate filenames.

The release also includes read-only preflight for existing saved cutting plans
and HTTP jobs, with capability checks and explicit deferred prerequisites.

The Python and TypeScript SDK foundation supports installed local cutting plans
and self-hosted HTTP jobs. Its packages retain development versions and are
available from this source tree; this release does not publish them to PyPI or
npm. Collection operations are available through Go, the CLI and HTTP.

SDK distribution builds pin patched setuptools and wheel versions in both the
hashed builder requirements and Python build-system configuration.

No database migration is required for this stage: collection submissions are
ordinary jobs and the additive draft rendition is persisted in their existing
request JSON. Deploy matching API and worker binaries together. Old workers do
not implement the new draft encoding policy.

The first release covers one source, static crop per clip and supplied timecodes.
It does not generate transcripts, rank moments, add music, burn styled captions
or track faces. Draft/final exports can differ in codec quality, dimensions and
frame-granularity durations; their requested edit decisions remain identical.

Release approval requires full local checks, actual media verification, all five
native source/archive targets, recovery and public self-hosting acceptance.
After merge, verify the actual main commit before tagging; final tagged archives
must pass the existing signing and downloaded-asset protocol. Follow
[release instructions](releases.md).
