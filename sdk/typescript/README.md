# @cutmyvideo/sdk

TypeScript clients for local `cutmy` execution and the cutmyvideo-core HTTP API.
The package has no runtime dependencies. Node 20.19 or newer is required for the
Node entry; the main HTTP entry also works in browsers with modern Fetch APIs.
This is the unpublished `1.3.0-dev.0` development package.

Build and install a package from this checkout:

```sh
cd sdk/typescript
npm ci
npm pack
npm install /absolute/path/to/cutmyvideo-sdk-1.3.0-dev.0.tgz
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
