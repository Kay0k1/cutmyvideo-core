# cutmyvideo for Python

Embed local media cutting or the asynchronous cutmyvideo HTTP service in Python
applications. Python 3.10 or later; no runtime dependencies. Requests and results
use JSON-compatible typed dictionaries, so the same plans can be saved, reviewed,
or passed between tools without a framework or an agent-specific protocol.

Release `1.4.1` includes a wheel and source archive as signed GitHub release
assets. Install the wheel when that release is published:

```sh
python -m pip install https://github.com/Kay0k1/cutmyvideo-core/releases/download/v1.4.1/cutmyvideo-1.4.1-py3-none-any.whl
```

The preflight methods require a core build with the preflight API/CLI. Local
execution requires an installed `cutmy` binary plus FFmpeg and FFprobe. The SDK
does not install executables or require a server for local use.

## Local files

```python
from cutmyvideo import LocalCutmy, Plan

core = LocalCutmy(binary="cutmy", profile="fast", threads=2)
source = core.inspect("recording.mp4")
plan: Plan = {
    "version": 1,
    "input": "recording.mp4",
    "ranges": [
        {"label": "opening", "start_ms": 1000, "end_ms": 5000},
        {"label": "answer", "start_ms": 12000, "end_ms": 18000},
    ],
    "options": {"format": "mp4", "quality": "best", "cut_mode": "accurate"},
    "output_mode": "joined",
}
report = core.preflight(plan, "edit.mp4")
print(report["warnings"])
result = core.execute_plan(plan, "edit.mp4", timeout=1800)
for output in result["outputs"]:
    print(output["path"], output["segments"], output["track_manifest"])
```

For separate clips, set `output_mode` to `"separate"` or omit it and pass an
existing output directory. The SDK does not create output directories or
overwrite files. Relative plan input paths resolve against the caller's current
directory; the private temporary plan stores an absolute input path and is
removed after the operation. The caller's dictionary is not modified.

All CLI calls use argv without a shell, strict JSON results, and bounded output.
`binary` also accepts an argv prefix, such as `["/path/to/wrapper", "--option"]`.
`env={"FFMPEG_PATH": "...", "FFPROBE_PATH": "..."}` extends the inherited
environment. A nonzero exit raises `LocalError` with `code`, `exit_code`, and
`outputs` containing files already published before a later failure. Human
messages can change; match codes and provide a fallback for unknown values.

Local `timeout` is measured in seconds. On timeout the SDK terminates the private
process group, gives the command up to two seconds to clean up, then kills and
reaps it. POSIX descendants stay in the same group. Windows uses CTRL_BREAK and
`taskkill /T /F`; custom wrappers must keep descendants in their process tree.

## HTTP service

```python
from uuid import uuid4
from cutmyvideo import CutmyClient, ExportRequest, JobError, ready_artifacts

with CutmyClient("https://your-cutmy-server.example") as client:
    source = client.upload_source("recording.mp4")
    # Or: source = client.import_source("https://media.example/recording.mp4")
    request: ExportRequest = {
        "source_id": source["id"],
        "ranges": [{"label": "answer", "start_ms": 1000, "end_ms": 5000}],
        "format": "mp4",
        "quality": "best",
        "cut_mode": "accurate",
        "output_mode": "joined",
    }
    report = client.preflight(request, timeout=120)
    print(report["warnings"])
    submission_key = str(uuid4())  # Persist this before submitting the request.
    job = client.create_job(request, idempotency_key=submission_key)
    try:
        job = client.wait_for_job(job["id"], timeout=1800, poll_interval=2)
    except JobError as failure:
        job = failure.job  # Earlier succeeded items remain available.
    for index, artifact in enumerate(ready_artifacts(job), start=1):
        client.download(artifact, f"result-{index}.mp4", max_bytes=1 << 30)
```

One client owns one private capability-cookie session. Initialization happens once
on first use, including concurrent first requests. The cookie is never included
in representations or logs. A known capability may be supplied explicitly as
`session_cookie=...` to resume its owner; treat it as a password. After calling
`session()` or another operation, `export_session_cookie()` explicitly returns
the capability for your credential store. Keep it out of prompts, logs, public
plans, and source control. Ordinary HTTP methods do not persist it automatically;
the collection runner below saves a separate private owner file. `close()` forgets
the local capability. It does not delete remote sources/jobs or revoke a cookie.
`session()` returns public server limits. `get_source`, `list_sources`,
`get_tracks`, and `get_job` support inspection and recovery.

Job submission requires an explicit `idempotency_key` of at most 128 UTF-8 bytes.
Persist it before submitting. If `APIError` reports a transport failure, timeout,
or server error, the mutation may already have committed: recover by calling
`create_job` with the **same owner and key**. Reusing a retained key returns the
original job, even if the request body changes; use a fresh key for different
work. Individual HTTP calls do not retry mutations automatically. Unkeyed uploads
and imports retain their ordinary one-shot behavior. For recoverable preparation,
use the separate durable source workflow below; it saves an intent and key before
uploading or importing.

`wait_for_job` returns the succeeded job; failed or cancelled jobs raise
`JobError` with `job`, `job_id`, and ready `artifacts`. Unknown future statuses are
polled until the deadline. The minimum polling interval is one second.
`on_progress(job)` receives each observed job. A `threading.Event` supplied as
`cancel_event` stops waiting between requests and during poll sleep. An active
request remains bounded by its own deadline. `WaitTimeout` and `WaitCancelled`
retain the job ID and latest observed job/artifacts and do not cancel the server
job. Use `cancel_job(job_id)` explicitly to request server cancellation.

## Collections and durable HTTP execution

Collection operations require core 1.3 or later. Each clip assembles ordered
intervals of the same source with its own settings. Local wrappers expose
`validate_collection(plan, directory, clip_ids=None, rendition="final")` and
`execute_collection(...)`. The output directory must exist; partial
`LocalError.outputs` retain IDs, segments and tracks. Local rendering has no
saved runner or automatic process-resume promise.

```python
from cutmyvideo import CutmyClient, CollectionError

client = CutmyClient("https://your-cutmy-server.example")
source = client.upload_source("interview.mp4")  # Explicit preparation, once.
request = {
    "source_id": source["id"],
    "plan": {"version": 1, "clips": [
        {"id": "answer", "ranges": [
            {"start_ms": 50000, "end_ms": 65000},
            {"start_ms": 10000, "end_ms": 15000},
        ]},
    ]},
}
client.validate_collection(request)  # Read-only, no queued jobs.
try:
    result = client.run_collection(request, "./interview-project", timeout=1800)
except CollectionError as error:
    result = error.report  # Ready files remain usable; no implicit remote cancel.

# A fresh client/process restores the SAME saved owner and header key.
result = CutmyClient("https://your-cutmy-server.example").resume_collection(
    "./interview-project", timeout=1800,
)
```

`submit_collection(request, idempotency_key=...)` performs one submission. A
structured admission result, including all-unadmitted 429/503, preserves each
clip's diagnostic/retryability together with `http_status` and `retry_after`.
`complete` means queue admission, not finished media.

The runner persists a generated key before submission and retries uncertain
submissions with the saved owner, key and exact body. It observes quota and
`Retry-After` diagnostics while continuing accepted jobs. Both run/resume accept
`timeout`, `poll_interval` (minimum one second), `cancel_event`, `on_progress`,
`max_output_bytes` (default 10 GiB across retained outputs), and
`admission_batch_size` (default 3, configurable 1..128). The collection runner
takes an existing source ID. It does not upload/import, change export settings,
delete remote sources or cancel jobs.

Admission retry delays apply to every later submission in the project, including
previously unsubmitted IDs and uncertain retries. This shared delay survives
process interruption; polling and downloading accepted jobs continue during it.

Pass `request=revised_request` to resume with the same source and key. Select
changed IDs using `clip_ids`; server normalization decides job reuse. Previous
revision files remain available, and draft/final artifacts have distinct names.
Recover an uncertain original submission before revising it.

One private project contains bounded `state.json` and a separate secret
`owner.json`; POSIX modes are 0700 for its directory and 0600 for metadata. Keep
the project out of source control/public shares. The configured trusted server
must match saved scope; an initialized conflicting owner is refused. Python and
TypeScript states are deliberately separate formats. An OS lock rejects a live
writer and releases after a killed process.

A temporary private initialization journal seals the original owner, request
and key before the primary files are created. Resume completes an interrupted
initialization from this journal; it is removed before any collection job is
submitted. Missing primary files without a valid journal are refused.

Downloads use configured artifact-ID routes, never remote URLs/filenames.
Private parts retain fsynced offsets and prefix SHA256. Resume verifies prefixes
and uses genuine `Range` with saved `Last-Modified`/`If-Range`; malformed 206 or
changed validators are refused. An ignored Range returning 200 restarts the
owned part. Uncheckpointed crash bytes are truncated. Complete files are hashed,
exclusively published and recorded durably. Finished files are rehashed on
resume; tampering preserves the file and gives a clear error for caller repair.

Progress receives public ready `outputs`, `pending`, `source_id`, `rendition`
and `phase` after a state checkpoint. Download details include `clip_id`,
`artifact_id`, `offset`, `total_size`, `resumed`; pending rows expose `stage`,
`downloaded_bytes`, `total_bytes`. Pending jobs also retain `job_id`,
`job_status`, the worker stage, `progress_ms` and a bounded worker
`error_code`/`message` when available. `download_interrupted` follows a failed
transfer checkpoint before retry; `output_ready` follows durable publication.
A callback exception stops with `CollectionError.report`, retaining state.
Reports/errors never include the owner capability. State JSON is bounded to
4 MiB; ordinary metadata JSON is bounded to 1 MiB, project responses to 16 MiB
and subtitle responses to 64 MiB. Publication requires hard links. Advisory
revision history retains the latest 128 entries; verified old files remain.
Projects retain at most 1024 completed receipts and 1024 unfinished transfers;
`receipt_limit` stops new publication before writing an unreadable state.

Collection validation/submission `APIError.diagnostic` retains only the public
`code`, `message`, `clip_id` and `field` repair fields. Durable failures include
the attributed clip's field in `CollectionError.report.pending`; unknown
response fields are discarded.

## Recoverable source preparation and clip repair

```python
prepared = client.prepare_source(
    {"kind": "upload", "path": "interview.mp4"}, "private/source-preparation",
    timeout=7200,
)
# An import uses {"kind": "import", "url": "https://..."} instead.
# After interruption, use the same server and private directory:
prepared = CutmyClient("https://your-cutmy-server.example").resume_source("private/source-preparation")
request["source_id"] = prepared["source_id"]
```

Use the same configured server URL when constructing a fresh client. Source preparation and collections
use separate private directories. Preparation saves the owner, exact intent and
idempotency key before its first POST. Resume first looks up that receipt, so a
lost successful upload/import reply recovers the same source without duplication.
A ready upload carries a verified content SHA256/size; metadata-only imports may
carry a provider identity instead, which is not a content hash. If an upload
must be retried, local bytes must still match its saved fingerprint. A transfer
may restart from the beginning; upload-byte resume is not promised. The same
whole-cycle timeout and cancellation checks bound hashing, waits and upload IO.
Deleting the source is explicit; a retained receipt never silently creates a
replacement source. `SourcePreparationError.report` preserves safe progress.
Explicit preparation and scoped project-retry keys require 1–128 visible ASCII
bytes without spaces; exact `.` and `..` path segments are reserved. Invalid
keys are refused before creating a private project.

Terminal failed, cancelled or expired clips require an explicit repair decision:

```python
repaired = client.resume_collection("private/interview-project", retry_failed=["clip-15"])
```

Each chosen clip receives a new private attempt key, saved before submission.
An uncertain reply resumes that same attempt. The other fourteen verified
outputs remain in place, and retrying a completed clip is refused. Polling a
succeeded job whose artifacts have expired ends with `artifact_expired` rather
than waiting until the whole deadline. Repair and request revision are separate
resume calls; resolve an uncertain original attempt before either.

## Browser handoff and portable edits

Choose and privately persist `collection_key` before the first collection run.
The same key lets `create_project` recover a lost creation reply without
overwriting subsequent browser edits:

```python
project = client.create_project({**request, "collection_key": saved_collection_key,
    "clip_jobs": [{"clip_id": output["clip_id"], "job_id": output["job_id"]} for output in repaired["outputs"]]})
handoff = client.create_project_handoff(project["id"])
# Deliver the short-lived handoff["token"] only to the intended browser user.
# The browser uses its own session; never transfer owner.json or the root key.
current = client.resume_collection("private/interview-project", project_id=project["id"])
```

Explicit project sync verifies the configured server, owner, fixed source and
each current clip's normalized request before adopting server jobs. It follows
the server's current selection and reuses matching browser renders. Further
submissions/retries use that project's scoped routes and revision checks.
Conflicting simultaneous `request=`/`project_id=` revisions fail explicitly;
saved uncertain submissions must be resolved first. To change a synchronized
project, call `edit_project` with its latest `revision`, then sync again.

`export_portable_project(request, source)` and
`import_portable_project(document, explicitly_selected_source)` exchange
`cutmy.collection.project` version 1 edit documents. They contain clip decisions
and optional source fingerprint/title/duration, not server IDs, owner credentials,
keys or source paths/URLs. Portable metadata grants no access to another source.

## Existing SRT/VTT sidecars

Retiming uses supplied subtitle text and the actual segment maps of verified
ready outputs; it performs no transcription and changes no rendered media:

```python
from pathlib import Path

sidecars = client.retime_subtitles({"version": 1, "format": "srt",
    "text": Path("interview.srt").read_text(encoding="utf-8"),
    "outputs": [{"clip_id": item["clip_id"], "segments": item["segments"]}
                for item in current["outputs"]]})
for item in sidecars["outputs"]:
    with Path("sidecars", item["clip_id"] + "." + item["format"]).open("x", encoding="utf-8", newline="\n") as file:
        file.write(item["text"])
```

Create the chosen sidecar directory first. The SDK verifies each result's clip
identity, format and SHA256; this explicit exclusive write preserves existing
files. Sidecars live outside the private media receipt journal/byte budget.
`LocalCutmy.retime_subtitles` uses the same request/result with the local binary;
local collection execution has no durable runner. Requests allow 512 KiB of
UTF-8 text, up to 128 clips/128 segments each, 4096 total segments and 8 MiB of
combined output text. VTT STYLE/REGION/inline timestamps are rejected; cue settings
require VTT output. Set `output_format` explicitly to convert SRT/VTT.

## Reports, timeouts, and downloads

Preflight checks known prerequisites and exposes deferred checks/warnings;
`valid=True` does not guarantee decoding, rendering, storage admission, or
publication. Remote preflight can validate only available metadata without
downloading media. Execution independently validates current source state.
Output segments expose actual bounds, which can differ from requested ranges in
copy mode. Track manifests describe selected and excluded streams.

Every HTTP method accepts a `timeout` in seconds. Defaults are 30 seconds for
requests, 600 seconds for uploads/imports and downloads; use `timeout=120` for a
longer preflight. Constructor options `request_timeout`, `upload_timeout`, and
`download_timeout` change these defaults. Deadlines cover initialization, DNS,
connection/TLS, headers, and all streamed chunks; they are not reset per chunk.
Ordinary JSON requests/plans, HTTP JSON responses and command output are limited
to 1 MiB per document/stream. Project responses and subtitle transformation use
the larger explicit caps documented above; stderr retains its ordinary bound.

Uploads and downloads stream in bounded chunks. Downloads use the configured
server's artifact-ID endpoint, ignoring `download_url`; redirects are refused.
The SDK writes a private temporary file beside the destination, verifies transfer
length when provided, flushes it, then publishes atomically without overwriting
an existing file or symlink. Interrupted or oversized transfers leave no final
file. `max_bytes` optionally limits download size. The destination filesystem
must support hard links for exclusive publication; failure preserves existing
files. Choose destination paths yourself rather than trusting remote filenames.

HTTPS validates certificates using the system trust store. The explicit server
URL may use HTTP for a deployment that provides its own transport security.
Configured base paths are supported. No request follows redirects or inherits
proxy settings. `APIError` exposes `code`, optional HTTP `status`,
`idempotency_key` for failed submissions, and a validated optional `retry_after`
in seconds. Unknown response fields, status strings, and error codes remain
available and require a generic caller fallback.

## Development checks

```sh
PYTHONPATH=sdk/python/src python -m unittest discover -s sdk/python/tests -v
```

Repository checks also build a wheel and source archive, install them outside
the checkout, and exercise real local and HTTP media exports.
