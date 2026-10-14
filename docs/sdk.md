# Python and TypeScript SDKs

Both SDKs support local media through the installed `cutmy` executable and a
self-hosted HTTP API. They have no runtime dependencies. The API client owns
one private session; the local client runs the same version 1 cutting plans as
the CLI. Neither client decides which moments to cut or changes encoding quality.

This is the SDK foundation for 1.3.0, available in `main`. Packages use
development versions and have not been published to PyPI or npm. Use core 1.3.0
or a matching current source build for preflight; the 1.2.0 binary does not contain
that command or endpoint. See the [project overview](../README.en.md#versions)
for release status and the [architecture](architecture.md) for dependencies.

## Install the candidate packages

Python 3.10 or newer:

```sh
python -m pip install ./sdk/python
```

Node.js 20.19 or newer, with an ESM project:

```sh
cd sdk/typescript
npm ci
npm run build
npm pack
# Install the resulting .tgz in the consuming project.
```

Use the [Python package guide](../sdk/python/README.md) and
[TypeScript package guide](../sdk/typescript/README.md) for complete options.
Local workflows also need `cutmy`, FFmpeg and ffprobe. The SDK does not download
executables or create a database/server on import.

## Local workflow

```python
from cutmyvideo import LocalCutmy

core = LocalCutmy("/usr/local/bin/cutmy", threads=2)
plan = {
    "version": 1,
    "input": "/media/interview.mp4",
    "ranges": [{"label": "Quote", "start_ms": 5000, "end_ms": 15000}],
    "options": {"format": "mp4", "quality": "best", "cut_mode": "accurate"},
}
report = core.preflight(plan, "/media/clips")
result = core.execute_plan(plan, "/media/clips")
for output in result["outputs"]:
    print(output["path"], output["segments"])
```

```typescript
import { LocalCutmy } from "@cutmyvideo/sdk/local";

const core = new LocalCutmy({ binary: "/usr/local/bin/cutmy", threads: 2 });
const plan = {
  version: 1 as const,
  input: "/media/interview.mp4",
  ranges: [{ label: "Quote", start_ms: 5000, end_ms: 15000 }],
  options: { format: "mp4", quality: "best", cut_mode: "accurate" },
};
const report = await core.preflight(plan, "/media/clips");
const result = await core.executePlan(plan, "/media/clips");
```

The destination directory must exist. For one joined output, set
`output_mode: "joined"` and pass its filename instead. Plans stay ordinary JSON:
the same request can come from an editor, a script, a service or an automated
assistant. SDKs preserve source/output timestamps, selected tracks and labels.

`preflight` returns passed/deferred checks and proposed output names without
writing media. Review deferred checks; `valid: true` does not guarantee decoding,
container compatibility, final size or publication. Execution checks these again.
Existing outputs are preserved. If a later local interval fails, `LocalError`
retains the completed outputs so the caller can use them instead of losing them.

## HTTP workflow

```python
from cutmyvideo import CutmyClient, ready_artifacts

with CutmyClient("http://127.0.0.1:8080") as client:
    source = client.upload_source("interview.mp4")
    request = {
        "source_id": source["id"],
        "ranges": [{"label": "Quote", "start_ms": 5000, "end_ms": 15000}],
        "format": "mp4", "quality": "best", "cut_mode": "accurate",
    }
    report = client.preflight(request)
    job = client.create_job(request, idempotency_key="quote-2026-10-14-01")
    done = client.wait_for_job(job["id"], timeout=1800, poll_interval=1)
    for artifact in ready_artifacts(done):
        client.download(artifact, artifact["filename"])
```

```typescript
import { CutmyClient, readyArtifacts } from "@cutmyvideo/sdk";
import { uploadFile, downloadArtifactToFile } from "@cutmyvideo/sdk/local";

const client = new CutmyClient({ baseUrl: "http://127.0.0.1:8080" });
const source = await uploadFile(client, "interview.mp4");
const request = {
  source_id: source.id,
  ranges: [{ label: "Quote", start_ms: 5000, end_ms: 15000 }],
  format: "mp4", quality: "best", cut_mode: "accurate",
};
const report = await client.preflight(request);
const job = await client.createJob(request, { idempotencyKey: "quote-2026-10-14-01" });
const done = await client.waitForJob(job.id, { timeoutMs: 1_800_000 });
for (const artifact of readyArtifacts(done)) {
  await downloadArtifactToFile(client, artifact.id, artifact.filename);
}
```

The TypeScript main entry also supports browser `Blob`/`File` uploads and
streamed downloads. The `./local` entry contains Node filesystem/process helpers;
it is separate from the browser entry. Browser callers use their HttpOnly cookie
and must obey the API's same-origin policy. No wildcard CORS access is introduced.
Python and Node file helpers stream uploads/downloads rather than reading the
whole recording into memory. Downloads refuse redirects, enforce byte limits,
preserve existing destinations and remove incomplete temporary files.

## Control, recovery and errors

Both clients expose inspection, preflight, job creation, job lookup, explicit
server cancellation and bounded waiting. Polling defaults to one second; choose
a longer interval when many tasks share one owner. Progress callbacks receive
the real job snapshot rather than an invented completion percentage.

Submission requires an explicit idempotency key. Persist that key before sending
the request and reuse it if acknowledgement is lost; a fresh key can create
another job. The SDK never automatically repeats uploads, imports or mutations.
Submission errors retain the key, while wait errors retain the job ID. If a
batch fails or is cancelled, `JobError` includes the terminal job and already
ready artifacts. `ready_artifacts`/`readyArtifacts` selects downloadable outputs.

An expired wait or cancelled wait stops the client's observation. It does not
cancel the server job: use `cancel_job`/`cancelJob` when that is intended. Local
timeouts stop the CLI, allow its bounded cleanup and then force/reap the process
tree if needed. Output-publication uncertainty and partial outputs remain visible.

The anonymous session cookie is the owner's capability. For a long-lived
service, keep the client instance. To resume in another process, explicitly
export the initialized capability with `export_session_cookie()` or Node's
`exportSessionCookie()`, save it in the application's private credential store,
and pass `session_cookie`/`sessionCookie` to the next client. Never put it in a
plan or a job report. Browser HttpOnly cookies cannot be exported by JavaScript.

Errors expose machine-readable codes and relevant status/context. Unknown
response fields, statuses and future error codes are preserved. A timeout or
network error does not imply that a mutation was unapplied. SDKs make no
automatic choice to lower quality, select another stream or delete source media.

## Verification

The SDK check builds a Python wheel and source distribution, rebuilds the wheel
from that source distribution, and builds an npm tarball. It installs both into
fresh consumer environments outside the repository, reruns their protocol tests,
checks external TypeScript declarations and executes real local media workflows.
The HTTP acceptance uses an isolated PostgreSQL database and the actual API and
worker: upload, preflight without creating a job, idempotent submission, polling
and streamed artifact download. Downloaded bytes match the published artifact;
independent full decoding checks durations, selected audio, geometry and manifests.

```sh
python -m pip install --require-hashes --only-binary=:all: -r scripts/requirements-sdk-build.txt
# TEST_DATABASE_URL must name a disposable test database.
make sdk-check SDK_REPORT_DIR=/tmp/new-cutmy-sdk-evidence
```

CI requires installed local consumers on all five supported native targets and
HTTP/worker acceptance on Linux. Missing prerequisites or skipped SDK consumers
cannot pass the evidence gate. No package registry publication is performed.
