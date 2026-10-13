# Current 10k memory diagnostic

One hosted Linux diagnostic measured the unchanged application `d594cc4681dc1cab61664cec8c92bdf9a3e9444c` (173 application files) in a fresh PostgreSQL container. Corrected tooling `7892ade3fa80e714c6c80eb6ddb6951921f18ef5`, run [38161439646](https://github.com/Kay0k1/cutmyvideo-core/actions/runs/38161439646), attempt 1, passed all seven original correctness/operational stages, the diagnostic memory checks, all 24 hosted synthetic controls and owned cleanup. This is one instrumented observation, not a memory-headroom guarantee, performance comparison, worker/service SLA or large-preview result.

| Observation | Result |
|---|---:|
| Periodic samples / completed marker ACKs | 496 / 54 |
| Largest periodic interval / last periodic to final snapshot | 102.948543 / 78.428973 ms |
| Cumulative cgroup peak | 866,168,832 bytes |
| Largest sampled current / final current | 839,737,344 / 413,655,040 bytes |
| One-shot RunMaintenance / API database admission | 3,320.479 / 1,600.408 ms |

Both operational calls returned one acknowledged completion with a live caller deadline (10 seconds / 5 seconds), no unbounded repair, durable reopen/progress and exact per-path accounting. Each independent fixture retained 10,000 sources and jobs, 200 preview pins, 9,900 healthy physical artifacts and 11,024 scan files. All seven stage verdicts remained PASS, and all namespaces, roles, owned processes, the container and named volume were cleaned up.

The observer reads current/continuous cumulative peak, memory.stat, global/local events and swap every 100 ms. The unchanged maximum gap is 200 ms, including the last periodic sample to the explicit final snapshot. The endpoint is the final cgroup read after the last SQL/disk boundary, before atomic final ACK publication; caller report parsing and later resource removal are outside this observed interval. Final raw fields are bound to ACK 54 and the saved-data verdict. Global/local OOM and OOM-kill counters started and ended at zero, and all recorded swap-current values were zero.

At the endpoint, memory.stat recorded anon 7,208,960; file 393,121,792; kernel 12,857,344; shmem 142,241,792 bytes. Shmem is included in file and must not be added again. The global peak is not an allocation total for a particular stage. Sampling cannot exclude spikes shorter than 100 ms below a previous cumulative maximum. Read-only SQL/disk boundaries added 11.974401195 seconds in total and altered caches/resource use; this observer is not performance neutral.

Limits: PostgreSQL 17.11, linux/amd64, 2 CPUs / 1 GiB hard memory limit and no extra swap; shared_buffers 128 MB, work_mem 4 MB, max_connections 32, jit off, fsync/synchronous_commit on. Go 1.27.2 uses GOMAXPROCS=2 and soft GOMEMLIMIT 128 MiB. Maximum eight normal calls, 30-second marker ACK, 30-minute supervised operation and 55-minute job bounds are unchanged. Exact OCI identities and raw cgroup/resource evidence are in the manifest and original ZIPs.

## Preserved failures

Run [38158894464](https://github.com/Kay0k1/cutmyvideo-core/actions/runs/38158894464), attempt 1, refused at setup because the disk measurement omitted the nested WAL row. The product workload did not start. Run [38159865451](https://github.com/Kay0k1/cutmyvideo-core/actions/runs/38159865451), attempt 1, completed all seven raw product stages and 54 ACKs but refused a sampling gap after the final fixture-cleanup marker. That observer discarded the failing candidate before writing it, so its exact rejected interval is unavailable. Both original diagnostic verdicts remain FAIL. The two source corrections and fresh successful run do not relabel or retry those original attempts. Earlier 32 × 5 ms capacity failures and separate accepted normal-profile observations also keep their original meanings.

## Verify saved data

The three ZIPs are unchanged official artifacts; expanded pilot reports and binaries are not copied into Git. Check their sizes and SHA-256 values against manifest.json. For the successful artifact, use the harness at exact tooling commit `7892ade3fa80e714c6c80eb6ddb6951921f18ef5`, extract it into a fresh directory and run:

```sh
python3 -B benchmarks/storage-normal-memory/verify_saved.py --directory /absolute/fresh-extracted-artifact --output /absolute/absent-verification.json
```

Do not add `-I`: the reader imports sibling modules. This command checks saved data only, including raw counters, endpoint/ACK binding, all seven verdicts and cleanup; it does not replay PostgreSQL or Go. The two historical failed artifacts are retained as refusal evidence and cannot produce a successful saved-data verdict. On failure, the corrected stream keeps its first rejected periodic row before validation: samples counts accepted rows, bytes counts all written rows, and a separate preservation error cannot replace the first refusal.

Raw diagnostics were retired from the working tree on 2026-10-13. The [original files](https://github.com/Kay0k1/cutmyvideo-core/tree/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/benchmarks/2026-10-11-storage-normal-memory) remain in Git history; future runs follow [artifact retention](../../docs/artifacts.md).
