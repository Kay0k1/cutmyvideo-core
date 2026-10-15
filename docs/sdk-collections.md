# SDK collections: complete a project and resume it

The 1.4.0 SDK candidate manages an HTTP collection from a supplied source and
timecodes to verified local files. It validates the plan, submits bounded batches,
waits for accepted jobs, downloads artifacts and saves progress before reporting
it. Python and the Node collection entry can continue in a fresh process.
See [installation](sdk.md) and the package references for
[Python](../sdk/python/README.md) and [TypeScript](../sdk/typescript/README.md).
The server needs the collection API introduced in core 1.3.0.

## Start a project

Prepare the source explicitly, retaining its source ID and owner session.
Upload/import has no idempotency guarantee: recover an uncertain source creation
separately. A collection request contains no local `input`. Stable clip IDs allow
revisions to reuse work; each clip joins its ranges in their given order.

```python
from cutmyvideo import CutmyClient, CollectionError

with CutmyClient("http://127.0.0.1:8080") as client:
    source = client.upload_source("interview.mp4")
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
            request, "./interview-project", timeout=1800,
            max_output_bytes=2_000_000_000,
            on_progress=lambda report: print(report["phase"], len(report["outputs"])),
        )
    except CollectionError as error:
        print(error.code, error.report["outputs"], error.report["pending"])
        raise
```

```typescript
import { CutmyClient, type CollectionRequest } from "@cutmyvideo/sdk";
import { uploadFile } from "@cutmyvideo/sdk/local";
import { runCollection, resumeCollection } from "@cutmyvideo/sdk/collections";

const client = new CutmyClient({ baseUrl: "http://127.0.0.1:8080" });
const source = await uploadFile(client, "interview.mp4");
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
protect checkpoints. Output publication requires hard-link support. These are
process-interruption recovery guarantees; power-loss durability also depends on
the filesystem, especially where directory synchronization is unavailable.
Expired remote artifacts, unavailable sources and modified files need explicit
repair. Local collection wrappers preserve existing files and partial results
but do not provide this durable HTTP project loop. Browser collection operations
are low-level; durable TypeScript recovery is Node-only. Transcription, moment
selection, music and downstream editing remain the agent's responsibility.
See [the 1.4.0 candidate](release-1.4.0.md) for release boundaries.
