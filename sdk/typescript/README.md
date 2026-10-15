# @cutmyvideo/sdk

TypeScript clients for local `cutmy` execution and the cutmyvideo-core HTTP API.
The package has no runtime dependencies. Node 20.19 or newer is required for the
Node entry; the main HTTP entry also works in browsers with modern Fetch APIs.
This is the unpublished `1.4.0-dev.0` development package.

Build and install a package from this checkout:

```sh
cd sdk/typescript
npm ci
npm pack
npm install /absolute/path/to/cutmyvideo-sdk-1.4.0-dev.0.tgz
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
The runner never automatically repeats an uncertain source upload.

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
  recovery information. The SDK never automatically retries a mutation.
- `ApiError` exposes stable `code`, HTTP `status`, `retryAfterSeconds` when supplied,
  and available `jobId` /
  `idempotencyKey`. Transport failures, timeouts and unsuccessful submissions may
  have committed: recover submissions with the saved key. For uncertain
  upload/import outcomes, check `listSources()` before repeating them.
- `waitForJob` polls at least once per second. Its timeout or AbortSignal stops
  waiting, leaving the server job active; use `cancelJob` explicitly to cancel it.
  Failed/cancelled jobs throw `JobError` with the full job and ready partial files.
- Local failures throw `LocalError`; `result?.outputs` preserves earlier
  published files reported by the CLI. A forced kill can prevent the CLI from
  returning a final report, so do not assume an empty result proves no output.
- Request, source and download defaults are 30 seconds, 2 minutes and 5 minutes.
  Downloads retain their deadline through stream consumption. Override with
  `timeoutMs` per operation and use `signal` for caller cancellation. JSON
  responses and CLI stdout/stderr are bounded to 1 MiB; optional `maxBytes`
  limits downloads using both advertised and actual bytes.
- Unknown response fields, diagnostic codes, track reasons, preflight check
  statuses and job states are preserved. An unknown job state continues polling
  within the waiting deadline; it is not treated as a successful export.

Run `npm test` for transport, cancellation, partial result, bounded stream and
local process tests. Repository CI also installs the packed tarball in a fresh
consumer and checks both clients against real core media operations.
