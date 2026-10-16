# @cutmyvideo/sdk

TypeScript clients for local `cutmy` execution and the cutmyvideo-core HTTP API.
The package has no runtime dependencies. Node 20.19 or newer is required for the
Node entry; the main HTTP entry also works in browsers with modern Fetch APIs.
Release `1.4.0` includes a signed npm tarball as a GitHub release asset. Install
it when that release is published:

```sh
npm install https://github.com/Kay0k1/cutmyvideo-core/releases/download/v1.4.0/cutmyvideo-sdk-1.4.0.tgz
```

## Local files

Install `cutmy` and FFmpeg/FFprobe separately. The SDK does not download binaries.
Preflight requires the new core preflight command. Keep the output directory
present for separate files; joined exports take an explicit output filename.

```ts
import { LocalCutmy } from '@cutmyvideo/sdk/local';
import type { Plan } from '@cutmyvideo/sdk';

const cutmy = new LocalCutmy({ binary: '/usr/local/bin/cutmy', threads: 2 });
const info = await cutmy.inspect('./recording.mp4');
const plan: Plan = {
  version: 1,
  input: './recording.mp4',
  ranges: [
    { start_ms: 10_000, end_ms: 18_000, label: 'Opening' },
    { start_ms: 30_000, end_ms: 42_000, label: 'Answer' },
  ],
  options: { format: 'mp4', quality: 'best', cut_mode: 'accurate' },
  output_mode: 'joined',
};
const report = await cutmy.preflight(plan, './edit.mp4');
console.log(report.checks, report.warnings, report.output_paths);
const result = await cutmy.executePlan(plan, './edit.mp4', { timeoutMs: 600_000 });
console.log(result.outputs.map(file => ({ path: file.path, segments: file.segments })));
```

Relative input/output paths are resolved from the configured `cwd`, or the
process working directory. Plan files are bounded to 1 MiB and placed in private
temporary directories. Arguments are passed directly to the executable without a
shell. The wrapper waits for process termination on cancellation, first allowing
graceful termination and then killing the process tree. On Windows, tree
termination uses `taskkill`; graceful POSIX signal behavior is unavailable.
Owned termination helpers are reaped before the call settles. A stuck force
helper has a one-second cleanup budget after the configured grace period.
Custom Windows launchers must remain running until their descendants exit:
`taskkill` cannot reliably recover a tree after its launcher has already exited.

## Hosted API

```ts
import { CutmyClient, JobError, readyArtifacts } from '@cutmyvideo/sdk';
import { uploadFile, downloadArtifactToFile } from '@cutmyvideo/sdk/local';

const client = new CutmyClient({ baseUrl: 'https://cutmy.video' });
const source = await uploadFile(client, './recording.mp4');
// Alternatively: await client.importSource('https://example.com/recording.mp4');
const request = {
  source_id: source.id,
  ranges: [{ start_ms: 10_000, end_ms: 18_000, label: 'Opening' }],
  format: 'mp4' as const,
  quality: 'best' as const,
  cut_mode: 'accurate' as const,
};
const report = await client.preflight(request);
console.log(report.checks, report.warnings);

// Persist this key BEFORE submitting. Reuse it to recover an uncertain submission.
const submissionKey = 'saved-project-123-export-1';
const submitted = await client.createJob(request, { idempotencyKey: submissionKey });
try {
  const completed = await client.waitForJob(submitted.id, {
    timeoutMs: 600_000,
    onProgress: job => console.log(job.status, job.items.map(item => item.progress_ms)),
  });
  for (const artifact of readyArtifacts(completed)) {
    // Use a caller-chosen path. Do not trust remote filenames as local paths.
    await downloadArtifactToFile(client, artifact.id, `./${artifact.id}.mp4`, {
      maxBytes: 1_000_000_000,
    });
  }
} catch (error) {
  if (error instanceof JobError) console.log(error.job, error.artifacts);
  throw error;
}
```

`uploadFile` uses a disk-backed Blob and multipart streaming rather than reading
the complete recording into JavaScript memory. `client.upload(blob, filename)`
accepts a browser File/Blob. `client.downloadArtifact(id)` returns a Web
ReadableStream and a `cancel()` method: consume or cancel it. Downloads always
use the authenticated artifact ID route, never a returned `download_url`.
`downloadArtifactToFile` writes to a private temporary file beside the target,
synchronizes the bytes, and publishes exclusively; an existing file is preserved.
Its atomic publication does not promise directory durability across power loss.

Browser use should target the same origin, for example
`new CutmyClient({ baseUrl: window.location.origin })`. Browser Fetch manages the
HttpOnly capability cookie with `credentials: 'include'`; the SDK does not add
CORS support to the server. The main entry contains no Node imports. Node stores
the owner cookie privately in each client. After `initializeSession()`, explicitly
call `exportSessionCookie()` to save its value in a private credential store.
To resume that owner session, pass the value as `sessionCookie` to a new client;
never log that capability. Exporting cookies is unavailable in browsers.
All redirects are refused, so cookies are not forwarded to another endpoint.

## Clip collections

Each named clip contains one or more original-source ranges, joined in their given
order. Collection options inherit into clips; a clip can override them, reset
audio selection with `audio_track: -1`, or remove inherited crop with
`clear_crop: true`. Final and draft have separate server job identities. Draft is
accurate MP4 at up to 720p; music, transcription and topic selection stay in the
calling agent's workflow.

```ts
import { CutmyClient, CollectionError } from '@cutmyvideo/sdk';
import type { CollectionRequest } from '@cutmyvideo/sdk';
import { uploadFile } from '@cutmyvideo/sdk/local';
import { runCollection, resumeCollection } from '@cutmyvideo/sdk/collections';

const client = new CutmyClient({ baseUrl: 'https://cutmy.video' });
// Prepare the source explicitly. Recover an uncertain upload separately.
const source = await uploadFile(client, './interview.mp4');
const request: CollectionRequest = {
  source_id: source.id,
  plan: {
    version: 1,
    options: { format: 'mp4', quality: 'best', cut_mode: 'accurate' },
    clips: [
      { id: 'opening', ranges: [{ start_ms: 10_000, end_ms: 18_000 }] },
      { id: 'answer', ranges: [
        { start_ms: 30_000, end_ms: 40_000 },
        { start_ms: 45_000, end_ms: 51_000 },
      ] },
    ],
  },
};
try {
  const report = await runCollection(client, request, './interview-project', {
    timeoutMs: 600_000,
    maxOutputBytes: 2_000_000_000,
    onProgress: report => console.log(report.phase, report.outputs.length),
  });
  console.log(report.outputs.map(file => ({ path: file.path, sha256: file.sha256 })));
} catch (error) {
  if (error instanceof CollectionError) console.log(error.code, error.report);
  throw error;
}

// A fresh process restores this project's saved owner and original key.
const resumed = await resumeCollection(
  new CutmyClient({ baseUrl: 'https://cutmy.video' }), './interview-project',
  { timeoutMs: 600_000 },
);
// Revise the request, or request a selected draft. Prior output files survive.
const draft = await resumeCollection(
  new CutmyClient({ baseUrl: 'https://cutmy.video' }), './interview-project',
  { request: { ...request, clip_ids: ['answer'], rendition: 'draft' } },
);
```

The Node-only `./collections` entry completes validation, admission, polling and
download. The default admission batch is 3 (`admissionBatchSize: 1..128`), counting
accepted clips until their local files are published. `maxSubmitBatch` separately
bounds each pending submission (default 3); omitted admission size inherits that
value. Server limits still apply. It saves accepted jobs from partial responses, observes
`Retry-After`, and downloads their outputs while remaining clips wait for capacity.
Per-job poll deadlines are checkpointed so a fresh resume preserves rate-limit
and temporary-unavailability delays as well as the minimum poll interval.
The whole call, including progress callbacks, shares one deadline. Poll intervals
must be at least 1000ms. Timeout, cancellation and terminal failures throw
`CollectionError` with verified outputs and remaining clip diagnostics in `report`;
they do not cancel remote jobs. Resume can continue an uncertain submission using
its exact persisted body, owner and key. Finish that submission before changing the
request. Equivalent clip requests are deduplicated by the server's normalized
identity, so only changed clips create new jobs.

The caller chooses a private project directory. On POSIX the directory and child
directories are 0700 and files are 0600. `state.json` holds requests, keys, jobs and
receipts; `owner.json` holds the owner capability separately. A private
`initialize.json` bootstrap journal bridges crashes before these two files exist;
it is removed and synchronized before any collection submission. Keep the entire
directory private, including crash leftovers. These are sensitive recovery files,
not artifacts for sharing, Git or diagnostic uploads. SDK state is versioned and
language-specific; Python and TypeScript projects cannot share a state directory.
Configured server and saved owner conflicts are refused before a collection call.
The collection runner takes an existing source ID. Recoverable upload/import is
available through the separate source-preparation entry below.

State checkpoints synchronize bytes and use atomic publication. Completed files
are SHA-256 verified on every resume; outputs include immutable artifact IDs in
their names and publish exclusively. Interrupted downloads save a verified prefix,
then resume through the protected artifact ID route with `Range` and, when
available, `Last-Modified`/`If-Range`. Transfers request identity content encoding
and reject compressed responses before appending bytes. A server ignoring Range causes an owned
temporary file to restart. An inconsistent 206 or changed artifact is refused.
Prepared publication receipts recover a crash after exclusive publication without
adopting unrelated files. Symlinks, unverified lock receipts and modified saved
files require attention. `maxOutputBytes` bounds all retained project outputs and
reserved full sizes of unfinished transfers, including prior revisions;
transfer retries and network bytes are also bounded.

One writer owns each project. Node uses an exclusive loopback listener, released
by the OS even after SIGKILL, plus a private PID/host receipt. Its hashed port is in
10000..29999 and receives no secrets or other traffic. A rare port collision safely
returns `project_busy`; retry later or choose another project directory. Lock
receipts from another host are refused, so moving a crashed project between hosts
requires explicit operator recovery. Windows does not provide POSIX modes; use a
directory accessible only to the running user.

Public progress includes `output_ready`, `download_checkpoint` and
`download_interrupted`. Download phases carry
`download: { clip_id, artifact_id, offset, total_size, resumed }` after the prefix
has been synchronized and checkpointed. Progress callbacks may be asynchronous
and receive independent public report values.

Pending rows include the admitted `job_id`, `job_status`, worker `stage`,
`progress_ms` and bounded `error_code`/`message` when available. Unknown response
fields are excluded. A succeeded job with no remaining ready artifact ends with
`artifact_expired`; an explicit repair can create a new attempt. State is bounded
to 4 MiB and 4096 retained receipt entries. New transfers stop with `receipt_limit`
before exceeding that restore limit; existing verified files stay available.

## Recoverable source preparation and repair

```ts
import { prepareSource, resumeSource } from '@cutmyvideo/sdk/sources';

const prepared = await prepareSource(client,
  { kind: 'upload', path: './interview.mp4' }, './private/source-preparation',
  { timeoutMs: 7_200_000 });
// Imports use { kind: 'import', url: 'https://...' } instead.
const recovered = await resumeSource(new CutmyClient({ baseUrl: client.serverUrl }),
  './private/source-preparation');
```

Use a separate private directory from the collection project. Preparation saves
the original owner, exact intent and key before its first POST. A fresh process
looks up that receipt before trying work again; losing an upload/import reply
recovers the same source. Content fingerprints contain verified SHA256/size.
Provider fingerprints identify metadata-only imports and are not content hashes.
A required upload retry checks the local bytes against their original fingerprint
and can restart the transfer; upload-byte resume is not promised. The whole
cycle deadline and `signal` bound hashing, polling, waits and upload IO. A deleted
source is not silently replaced. `SourcePreparationError.report` contains safe
progress; private keys, owner capability and local input paths are excluded.
Explicit preparation and scoped project-retry keys require 1–128 visible ASCII
bytes without spaces; exact `.` and `..` path segments are reserved. Invalid
keys are refused before creating a private project.

Repair only explicitly chosen failed/cancelled/expired clips:

```ts
const repaired = await resumeCollection(client, './private/interview-project',
  { retryFailed: ['clip-15'] });
```

A private new attempt key is checkpointed before submission and reused after an
uncertain reply. Fourteen other verified outputs remain intact. Completed clips
cannot be retried. Resolve unknown submissions first; request revision and
`retryFailed` are separate resume calls.

## Browser handoff and portable edit documents

Persist `savedCollectionKey` privately before the first run and pass it as the
run's `idempotencyKey`. Project creation reuses that identity and can recover a
lost creation reply without overwriting subsequent browser edits:

```ts
const project = await client.createProject({ ...request,
  collection_key: savedCollectionKey,
  clip_jobs: repaired.outputs.map(output => ({ clip_id: output.clip_id, job_id: output.job_id })) });
const handoff = await client.createProjectHandoff(project.id);
// Deliver handoff.token only to the intended browser user. Never share owner.json.
const current = await resumeCollection(client, './private/interview-project',
  { projectId: project.id });
```

The browser uses its own session and the short-lived scoped token, not the SDK's
owner cookie/root key. Explicit project sync checks the configured server, owner,
fixed source and each normalized current request before adopting browser jobs.
It follows the saved server selection and reuses matching renders. Later
submissions and retries use scoped project routes and revision checks. A supplied
`request` conflicting with `projectId` fails explicitly, and an uncertain saved
submission must be resolved before adoption. Change synchronized project edits
through `editProject` with its latest `revision`, then sync again.

The browser-compatible main entry exports `exportPortableProject(request, source)`
and `importPortableProject(document, explicitlySelectedSource)`. Their
`cutmy.collection.project` version 1 envelope has clip decisions plus optional
fingerprint/title/duration. It excludes server resource IDs, owner credentials,
keys and source paths/URLs, and grants no authority to look up another source.

## Existing SRT/VTT sidecars

Use external subtitle text and the actual segment maps from verified ready
outputs. Retiming performs no transcription or media rendering:

```ts
import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const sidecars = await client.retimeSubtitles({ version: 1, format: 'srt',
  text: await readFile('./interview.srt', 'utf8'),
  outputs: current.outputs.map(output => ({ clip_id: output.clip_id, segments: output.segments })) });
for (const item of sidecars.outputs)
  await writeFile(join('./sidecars', item.clip_id + '.' + item.format), item.text,
    { encoding: 'utf8', flag: 'wx' });
```

Create the chosen sidecar directory first. The SDK verifies result identities,
formats and SHA256. Exclusive writes preserve existing files; sidecars are
outside the private media journal and its byte budget. `LocalCutmy.retimeSubtitles`
uses the same contract with an installed core binary. Local execution does not
promise a durable runner. Subtitle text is limited to 512 KiB UTF-8, with up to
128 clips/128 segments each, 4096 total segments and 8 MiB combined output text.
VTT STYLE/REGION/inline timestamps are rejected; cue settings require VTT output.
Use `output_format` to convert SRT/VTT. Requests are capped at 4 MiB, subtitle
responses at 64 MiB and project responses at 16 MiB; ordinary metadata retains
its 1 MiB limit.

For a manual agent loop, the browser-compatible main client exposes
`validateCollection(request)` and
`submitCollection(request, { idempotencyKey })`. Validation encodes no outputs;
submission makes exactly one call. Partial collection reports preserve
`http_status` and `retry_after_seconds`, even when admission returns non-2xx.
Collection envelope failures expose `ApiError.diagnostic` with only
`code`, `message`, `clip_id` and `field`; raw bodies and other error fields are not
retained. Durable pending diagnostics preserve the named field so an agent can
repair the clip in a revised request. Applications own their polling and retry
policy at this level.

Local collections use the installed core CLI without HTTP state:

```ts
import { LocalCutmy } from '@cutmyvideo/sdk/local';
const local = new LocalCutmy({ binary: '/usr/local/bin/cutmy', threads: 2 });
const plan = { ...request.plan, input: './interview.mp4' };
await local.validateCollection(plan, './drafts', {
  clipIds: ['answer'], rendition: 'draft', maxOutputBytes: 1_000_000_000,
});
const files = await local.executeCollection(plan, './drafts', {
  clipIds: ['answer'], rendition: 'draft', timeoutMs: 600_000,
});
```

The output directory must exist. CLI validation checks selected output collisions
without encoding; execution reports clip IDs, requested ranges, actual segments
and selected tracks. `LocalError.result` retains earlier published outputs if a
later clip fails. The wrapper preserves literal arguments and temporary-plan
privacy for these methods as for single-job plans.

## Results, limits and failures

- `valid: true` means the reported known prerequisites passed. Deferred decoding,
  compatibility, output size, queue/storage admission and publication checks can
  still fail; execution independently checks its source. Remote sources receive
  metadata-only preflight until local media is available.
- Times, durations and progress are milliseconds. Copy mode may change actual
  boundaries; use artifact `actual_start_ms`, `actual_end_ms` and `segments`.
- `createJob` requires an explicit key containing 1–128 UTF-8 bytes and no control
  characters. Changing work requires a new key. Retention/deletion can remove
  recovery information. Ordinary low-level methods never retry mutations;
  durable runners recover only their saved exact intents.
- `ApiError` exposes stable `code`, HTTP `status`, `retryAfterSeconds` when supplied,
  and available `jobId` /
  `idempotencyKey`. Transport failures, timeouts and unsuccessful submissions may
  have committed: recover submissions with the saved key. Unkeyed upload/import
  calls need caller reconciliation; `prepareSource`/`resumeSource` provide the
  separate durable keyed workflow.
- `waitForJob` polls at least once per second. Its timeout or AbortSignal stops
  waiting, leaving the server job active; use `cancelJob` explicitly to cancel it.
  Failed/cancelled jobs throw `JobError` with the full job and ready partial files.
- Local failures throw `LocalError`; `result?.outputs` preserves earlier
  published files reported by the CLI. A forced kill can prevent the CLI from
  returning a final report, so do not assume an empty result proves no output.
- Request, source and download defaults are 30 seconds, 2 minutes and 5 minutes.
  Downloads retain their deadline through stream consumption. Override with
  `timeoutMs` per operation and use `signal` for caller cancellation. JSON
  responses and CLI stdout/stderr ordinarily stay within 1 MiB. Project/subtitle
  methods use the explicit larger response caps above; stderr keeps its ordinary
  bound. Optional `maxBytes` limits downloads using advertised and actual bytes.
- Unknown response fields, diagnostic codes, track reasons, preflight check
  statuses and job states are preserved. An unknown job state continues polling
  within the waiting deadline; it is not treated as a successful export.

Run `npm test` for transport, cancellation, partial result, bounded stream and
local process tests. Repository CI also installs the packed tarball in a fresh
consumer and checks both clients against real core media operations.
