# Normal storage operations: 1,000 and 10,000 retained sources

These are successful synthetic Linux observations under normal caller budgets: 10 seconds for the one-shot `RunMaintenance` call and 5 seconds for API database admission through `ReserveSource`. They compare schema004 at `63873e935626d51087d0a26b84f1de9a7ed86a28` with the current schema005 application at `d594cc4681dc1cab61664cec8c92bdf9a3e9444c`, using one identical test-only fixture and fixed harness `e7bf76434c593fda7cef25a07eb3f209e919f1b6`.

The [initial 1,000 campaign](https://github.com/Kay0k1/cutmyvideo-core/actions/runs/38152420022) and [fresh 1,000→10,000 campaign](https://github.com/Kay0k1/cutmyvideo-core/actions/runs/38153402987) completed on attempt 1. The second campaign required its own fresh successful 1,000 pair before promotion to 10,000. All six version/size workloads passed seven required stages and their original Go test. Source identities, resource limits, original ZIP bytes, per-path charges, protected rows/pins/files, acknowledged completion and cleanup were independently checked. The schema004 persisted-counter audit stage is recorded as not applicable; schema005 executes its actual audit.

| Campaign and retained sources | One-shot maintenance: schema004 / current | API bootstrap: schema004 / current |
|---|---:|---:|
| Initial campaign, 1,000 | 367.703 / 765.323ms | 178.395 / 173.170ms |
| Promotion campaign, fresh 1,000 | 302.550 / 662.333ms | 131.020 / 128.813ms |
| Promotion campaign, 10,000 | 2249.014 / 2464.432ms | 1468.795 / 1444.776ms |

Each normal stage completed in one acknowledged call before its parent deadline. Maintenance was slower in these pairs; API bootstrap was slightly faster. These are single observations on seeded databases and prepared pools, not latency distributions or a general speedup. They measure neither the worker queue nor a production SLA. The original 32-cycle, 5ms experiment remains a separate historical FAIL with its 512-entry criterion unchanged.

Each normal stage uses an independent fresh fixture. The 1,000 case has 2,024 scan paths and 990 healthy physical controls; the 10,000 case has 11,024 scan paths and 9,900 controls. Original retained source/job rows and 200 preview pins stay protected. Exact per-path charges and file contents, protected row digests, reservations and full ledgers agree after each acknowledged call and independent role-bound reopen. The API reservation is subsequently released. A stage may use at most eight calls with unchanged parent-derived budgets; genuine database errors, late acknowledgment or incomplete durable state fail the verdict. No unbounded fallback repairs a result. The comparison retains source TTL checks, foreground DML, held-fence cancellation correctness, persisted audit and concurrent normal operations.

Both versions use PostgreSQL 17.11, immutable OCI index `sha256:3645570cccdfa447589da9f57dd740faa29b30938e861289a5574b6ca6b03826`, platform linux/amd64, child `sha256:66aafa11cf15800a3c94763f7e11d1e7b2e37e5e84bc4ce027cb6e1e4bfaf4df` and config `sha256:589f742565e4f6d22149176b032974f55f8201a8a37b71ed587ec5c6902e689f`. The owned PostgreSQL container has 2 CPUs / 1 GiB, no additional swap, shared_buffers 128 MB, work_mem 4 MB, max_connections 32, jit off, fsync on and synchronous_commit on. Go 1.27.2 uses GOMAXPROCS=2 and a 128 MiB soft Go memory target. Compilation occurs outside measured calls. Limits remain 55 minutes per job, 30 minutes per harness work window and 6 minutes per Go invocation, with resource admission, bounded capture and owned-resource cleanup.

After the current 10,000 workload, the same PostgreSQL cgroup's cumulative `memory.peak` reached 1,073,741,824 bytes, exactly its 1 GiB hard limit. The saved container `OOMKilled` flag is false. No `memory.events` capture establishes the absence of OOM events, and this observation shows no memory headroom. It grants no 100,000 or production-capacity claim.

The two unchanged official artifact ZIPs contain 127 and 185 original members. They retain summaries, complete workload reports, original Go events, query plans, source/image/resource captures and cleanup receipts; test binaries are removed before upload and absent. Their exact API identities, sizes, SHA256 digests, run/head/attempt, fixture hash and observed verifier contexts are in `manifest.json`. The compressed originals are published instead of expanded multi-megabyte reports. Raw evidence retains ephemeral owned runner paths. Content review found no matching password, database URL, authorization token, private key or cookie assignments. Its only HTTP URLs are public PostgreSQL source/image references; no production data or credentials were read.

To verify saved data, use a separate trusted checkout at the exact harness commit above. Verify the chosen ZIP SHA256 and byte size against `manifest.json`, extract it into a new empty directory, and save its corresponding `manifest.artifacts[i].verifier_context` as a context JSON file. For example, prepare the initial campaign context with:

```sh
python3 -B -c 'import json; from pathlib import Path; m=json.loads(Path("manifest.json").read_text()); a=next(a for a in m["artifacts"] if a["run_id"]==38152420022); Path("context-38152420022.json").write_text(json.dumps(a["verifier_context"]))'
```

From the trusted harness checkout, run:

```sh
python3 -B benchmarks/storage-normal-capacity/verify_saved.py \
  --directory /absolute/path/to/fresh-extracted-evidence \
  --context /absolute/path/to/context-38152420022.json \
  --output /absolute/path/to/new-saved-proof.json
```

For the promotion campaign select run 38153402987, its own ZIP and matching context. This saved-data verifier reads original files and requires no Go compilation, database, container or workload execution. A successful result verifies the recorded observations; it does not reproduce timings or establish an SLA.

Raw diagnostics were retired from the working tree on 2026-10-13. The [original files](https://github.com/Kay0k1/cutmyvideo-core/tree/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/benchmarks/2026-10-11-storage-normal-profile) remain in Git history; future runs follow [artifact retention](../../docs/artifacts.md).
