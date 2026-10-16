# SDK collections: complete a project and resume it

The 1.4.0 SDK manages an HTTP collection from a supplied source and
timecodes to verified local files. It validates the plan, submits bounded batches,
waits for accepted jobs, downloads artifacts and saves progress before reporting
it. Python and the Node collection entry can continue in a fresh process.
See [installation](sdk.md) and the package references for
[Python](../sdk/python/README.md) and [TypeScript](../sdk/typescript/README.md).
Basic collections need the API introduced in core 1.3.0. Durable source
preparation, scoped project review and subtitle sidecars need core 1.4.0.

## Start a project

Prepare the source in a separate private directory. `prepare_source` /
`prepareSource` saves the owner and immutable intent before mutation; use
`resume_source` / `resumeSource` after process interruption or an uncertain
upload/import response. Ordinary unkeyed upload/import calls retain their legacy
behavior and must not be blindly retried. A collection request contains no local
`input`. Stable clip IDs allow
revisions to reuse work; each clip joins its ranges in their given order.

```python
from cutmyvideo import CutmyClient, CollectionError

# Choose and persist this key privately before the first run.
collection_key = "application-persisted-project-key"
with CutmyClient("http://127.0.0.1:8080") as client:
    prepared = client.prepare_source(
        {"kind": "upload", "path": "interview.mp4"}, "./interview-source", timeout=7200)
    source = prepared["source"]
    request = {
        "source_id": source["id"],
        "plan": {
            "version": 1,
            "options": {"format": "mp4", "quality": "best", "cut_mode": "accurate"},
            "clips": [
                {"id": "opening", "ranges": [{"start_ms": 10000, "end_ms": 18000}]},
                {"id": "answer", "ranges": [
                    {"start_ms": 30000, "end_ms": 40000},
                    {"start_ms": 45000, "end_ms": 51000},
                ]},
            ],
        },
        "rendition": "final",
    }
    try:
        result = client.run_collection(
            request, "./interview-project", idempotency_key=collection_key, timeout=1800,
            max_output_bytes=2_000_000_000,
            on_progress=lambda report: print(report["phase"], len(report["outputs"])),
        )
    except CollectionError as error:
        print(error.code, error.report["outputs"], error.report["pending"])
        raise
```

```typescript
import { CutmyClient, type CollectionRequest } from "@cutmyvideo/sdk";
import { prepareSource, resumeSource } from "@cutmyvideo/sdk/sources";
import { runCollection, resumeCollection } from "@cutmyvideo/sdk/collections";

const client = new CutmyClient({ baseUrl: "http://127.0.0.1:8080" });
// Choose and persist this key privately before the first run.
const collectionKey = "application-persisted-project-key";
const prepared = await prepareSource(client, {kind: "upload", path: "interview.mp4"}, "./interview-source");
const source = prepared.source!;
const request: CollectionRequest = {
  source_id: source.id,
  plan: {
    version: 1,
    options: { format: "mp4", quality: "best", cut_mode: "accurate" },
    clips: [
      { id: "opening", ranges: [{ start_ms: 10_000, end_ms: 18_000 }] },
      { id: "answer", ranges: [
        { start_ms: 30_000, end_ms: 40_000 },
        { start_ms: 45_000, end_ms: 51_000 },
      ] },
    ],
  },
  rendition: "final",
};
const result = await runCollection(client, request, "./interview-project", {
  idempotencyKey: collectionKey,
  timeoutMs: 1_800_000,
  maxOutputBytes: 2_000_000_000,
  onProgress: report => console.log(report.phase, report.outputs.length),
});
```

Validation writes no outputs and queues no jobs; it cannot guarantee future
decoding, capacity or source availability. Accepted jobs continue while remaining
clips wait for owner capacity or admission limits. Server retry delays are
respected. A lost submission reply is retried with the saved exact body, owner
and key. Low-level `validate_collection`/`validateCollection` and
`submit_collection`/`submitCollection` remain available for custom agent loops.
Low-level submission performs one call: its `complete` means admission, whereas
the runner's `complete` means all selected files are ready.

Source preparation is a separate durable cycle. For an import, replace the input
with `{kind:"import",url:"https://..."}`. Resume its private directory with
`client.resume_source("./interview-source")` or
`resumeSource(client, "./interview-source")`. The same owner/key/intent recovers
an already committed source immediately. An in-flight crash can require waiting
for the claim lease, `max(UPLOAD_TIMEOUT,SOURCE_TIMEOUT) + 1 minute`, before the
same intent is safe to retry. A processing receipt is not permission to start a
second upload. Upload byte-range resume is not supported; a required retry can
restart the transfer and must match the saved content fingerprint. Preparation
defaults to a two-hour whole-cycle budget, configurable by the caller, including
hashing and upload IO. Choose enough time for server lease/admission waits.

Ready upload/staged-file fingerprints are `{kind:"content",sha256,size_bytes}`.
Metadata-only remote imports instead use
`{kind:"provider",provider,provider_id,canonical_url_sha256}`; this identifies
the provider record and is not a claim about downloaded content bytes. A retained
ready receipt remains recoverable while its source exists. Deleting the source
does not silently prepare a replacement; start a new explicit preparation for
new work. Ordinary unkeyed source calls still require caller reconciliation.

## Resume in another process

Configure a new client with the same trusted server and use the same private
directory. The runner restores the saved owner; an initialized client with a
different owner is refused. A new key or directory creates a different project.

```python
with CutmyClient("http://127.0.0.1:8080") as client:
    result = client.resume_collection("./interview-project", timeout=1800)
```

```typescript
const result = await resumeCollection(
  new CutmyClient({ baseUrl: "http://127.0.0.1:8080" }),
  "./interview-project", { timeoutMs: 1_800_000 },
);
```

Deadline/cancellation stops this caller's work without cancelling remote jobs.
Python progress callbacks run synchronously: keep them bounded, because the SDK
cannot interrupt caller code blocked inside a callback. Node bounds its await
of asynchronous callbacks; cancellation cannot stop their own external side effects.
`CollectionError.report` retains verified ready files and pending diagnostics.
Progress follows state checkpoints and contains no owner capability.
`download_interrupted` identifies the artifact, saved offset and total size
before retry. A killed process needs no exception handler to preserve previously
acknowledged progress. Resume hashes ready files and saved prefixes, then
continues accepted jobs instead of rendering completed clips again.

Incomplete transfers use the authenticated immutable artifact-ID route with
`Range` and the saved `Last-Modified` in `If-Range` when available. Identity content
encoding, partial-response bounds and byte limits are checked. A full 200 response
restarts the owned part; malformed partial responses cannot be appended. Complete
files publish exclusively with artifact ID, size and SHA256 receipts. Prepared
receipts recover a crash between publication and final acknowledgement without
adopting arbitrary existing files. Outputs include source/output segment maps
and selected-track manifests for downstream editing.

## Revise selected clips

Keep the source and stable IDs, edit the request and pass it to resume. Recover
any uncertain saved submission first. Server normalization reuses equivalent
jobs: changes to two clips create two new jobs. Prior revision files remain
available, with immutable artifact IDs in their names. `clip_ids` selects outputs
in plan order. Draft and final use separate identities; draft is explicitly
lower-quality accurate MP4 with a 720p height cap.

```python
request["plan"]["clips"][1]["ranges"][0]["end_ms"] = 42000
with CutmyClient("http://127.0.0.1:8080") as client:
    revised = client.resume_collection("./interview-project", request=request)
    draft = client.resume_collection(
        "./interview-project",
        request={**request, "clip_ids": ["answer"], "rendition": "draft"},
    )
```

```typescript
request.plan.clips[1].ranges[0].end_ms = 42_000;
const revised = await resumeCollection(client, "./interview-project", { request });
const draft = await resumeCollection(client, "./interview-project", {
  request: { ...request, clip_ids: ["answer"], rendition: "draft" },
});
```

Aggregate output budgets include retained revisions and partial-file reservations,
so repeated edits cannot bypass the configured project limit. The SDK never
silently changes quality, selected tracks or crop to fit capacity.

## Repair a failed clip

A failed/cancelled job or a succeeded job with no unexpired ready artifact needs
an explicit attempt. The runner reports the latter as `artifact_expired` without
polling until its deadline. For a 15-clip run where only `clip-15` failed:

```python
repaired = client.resume_collection("./interview-project", retry_failed=["clip-15"])
```

```typescript
const repaired = await resumeCollection(client, "./interview-project", {
  retryFailed: ["clip-15"],
});
```

The SDK verifies the chosen terminal state, saves a fresh private attempt key
before POST, and recovers a lost reply using that exact attempt. The other
fourteen verified files keep their hashes and job IDs. Completed clips cannot be
retried as failures. Resolve uncertain original submissions first; repair and
request revision are separate resume calls. Public pending rows carry `job_id`,
`job_status`, worker `stage`, `progress_ms` and bounded `error_code`/`message` when
available, so an agent can distinguish worker failure from admission/download
work without scraping process logs.

## Review in the browser, then adopt its edits

Create a server project using the privately saved collection key and the current
ready job IDs. Project creation is idempotent by owner/key: a lost creation reply
can be repeated, returning the current project without replacing browser edits.
Use explicit `clip_jobs` pairs so separate stable clip IDs with identical
normalized media decisions can still attach their intended jobs unambiguously.

```python
project = client.create_project({**request, "collection_key": collection_key,
    "clip_jobs": [{"clip_id": item["clip_id"], "job_id": item["job_id"]} for item in repaired["outputs"]]})
handoff = client.create_project_handoff(project["id"])
# Privately deliver handoff["token"] to the intended browser recipient.
# After browser review/export:
current = client.resume_collection("./interview-project", project_id=project["id"])
```

```typescript
const project = await client.createProject({ ...request, collection_key: collectionKey,
  clip_jobs: repaired.outputs.map(item => ({ clip_id: item.clip_id, job_id: item.job_id })) });
const handoff = await client.createProjectHandoff(project.id);
// Privately deliver handoff.token to the intended browser recipient.
const current = await resumeCollection(client, "./interview-project", { projectId: project.id });
```

The recipient redeems the ten-minute token with its own session. It receives a
24-hour grant for this project, not the agent's cookie, key or other resources.
An owner can revoke grants. Project metadata expires after seven days; a grant
does not preserve the original source beyond its retention policy.

Sync is explicit: configured server/owner and fixed source must match saved
scope. `clips[].request` describes each currently selected normalized request;
only `jobs[].current` matches are adopted. The DTO can also include attached
historical or unselected jobs; they are not adopted as current outputs. A repaired
job remains current instead of reverting to the old failed job. Two changed
clips reuse the other thirteen jobs/files. Server selection remains authoritative;
if only two clips are selected, the report contains those two selected results.

Subsequent admissions and repairs use scoped project routes with revision checks.
Saved uncertain work must be resolved before sync; the SDK never clears unknown
submissions to guess a newer revision. An explicit `request` conflicting with
`project_id`/`projectId` returns `project_conflict`. Edit a synchronized project
using `edit_project`/`editProject` with its latest `revision`, then sync. Stale
revisions fail instead of silently overwriting another editor's changes.

For sharing edit decisions without access, use
`export_portable_project`/`exportPortableProject` and
`import_portable_project`/`importPortableProject`. The envelope is
`{kind:"cutmy.collection.project",version:1,source,plan,clip_ids?,rendition?}`.
Its source metadata may contain title, duration and fingerprint, never server
resource IDs, keys, paths or URLs. Import requires an explicitly selected trusted
Source; a supplied fingerprint/duration must match. The document grants no
lookup or media authority and excludes unknown edit fields.

## Retime an existing subtitle transcript

Pass the actual segment maps of the verified current results, which may contain
packet-copy preroll or repeated/reordered source intervals. A fifteen-output
project returns fifteen sidecars, including empty sidecars where no cue overlaps:

```python
from pathlib import Path

sidecars = client.retime_subtitles({"version": 1, "format": "srt",
    "text": Path("interview.srt").read_text(encoding="utf-8"),
    "outputs": [{"clip_id": item["clip_id"], "segments": item["segments"]}
                for item in current["outputs"]]})
Path("sidecars").mkdir(exist_ok=True)
for item in sidecars["outputs"]:
    with Path("sidecars", item["clip_id"] + "." + item["format"]).open("x", encoding="utf-8", newline="\n") as file:
        file.write(item["text"])
```

```typescript
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

const sidecars = await client.retimeSubtitles({ version: 1, format: "srt",
  text: await readFile("interview.srt", "utf8"),
  outputs: current.outputs.map(item => ({ clip_id: item.clip_id, segments: item.segments })) });
await mkdir("sidecars", { recursive: true });
for (const item of sidecars.outputs)
  await writeFile(join("sidecars", item.clip_id + "." + item.format), item.text,
    { encoding: "utf8", flag: "wx" });
```

Both SDKs check SHA256 and clip/format identity before returning results. The
explicit exclusive write preserves existing files and stays outside the private
media receipt journal and its byte budget. `LocalCutmy` exposes the same retiming
method through the CLI. The pure operation performs no transcription, cue
selection, media rendering or caption burn-in. See
[supported SRT/VTT features and limits](subtitle-sidecars.md).

## Privacy and limits

The project contains private `state.json`, a separate secret `owner.json`, parts
and published `files/`. An initialization journal covers crashes before both
primary files exist and is removed before submission. Keep the entire directory
private, including recovery leftovers: requests, keys and owner credentials do
not belong in prompts, Git or diagnostic uploads. Share chosen output files and
public reports. POSIX directories/files use 0700/0600; on Windows use a directory
accessible only to the running user.

One OS-held writer lock owns a project and releases after process termination.
Node uses a private loopback listener; a rare hashed-port collision returns
`project_busy` and can be retried. State/schema/language, server, owner and file
identity conflicts are reported rather than resetting work. Python and TypeScript
have separate state formats and cannot share a project directory.

State documents are bounded; atomic metadata replacement and file synchronization
protect checkpoints. Advisory Python history keeps the latest 128 entries;
finished files remain retained and budgeted. Python caps completed receipts and
unfinished transfers at 1024 each; Node caps combined receipt entries at 4096.
`receipt_limit` refuses a new transfer before persisting state it cannot restore.
Project DTO responses have a separate 16 MiB SDK cap, subtitle responses 64 MiB;
ordinary metadata keeps its 1 MiB bound. Output publication requires hard-link
support. These are
process-interruption recovery guarantees; power-loss durability also depends on
the filesystem, especially where directory synchronization is unavailable.
Expired remote artifacts, unavailable sources and modified files need explicit
repair. Local collection wrappers preserve existing files and partial results
but do not provide this durable HTTP project loop. Browser collection operations
are low-level; durable TypeScript recovery is Node-only. Transcription, moment
selection, music and downstream editing remain the agent's responsibility.
See [the 1.4.0 scope](release-1.4.0.md) for release boundaries.
