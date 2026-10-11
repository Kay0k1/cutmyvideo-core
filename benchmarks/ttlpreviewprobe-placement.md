# TTL query comparison harness placement

This source-only branch starts at immutable f80cb9bba29f2ba97b37152bbf3388735d4a3173.
The application files, including source_retention.go and source_retention_test.go,
remain at f80. The reviewed two-file candidate exists inside the frozen packet
as candidate.patch; the once runner applies it in a separate disposable runtime
worktree after an explicit ROOT authorization. Nothing has run in preparation.

The 15 reviewed files plus packet-manifest.json are copied byte for byte into
benchmarks/ttlpreviewprobe. The frozen packet manifest SHA is
3b350dcddbe61103ab49a9b512b2c880f2591b0400c50be4ab097faf6ff48bbc.
Its README and comments retain their original source-preparation paths and the
initial proposed new workflow path. Those archival bytes are intentionally exact.
The executable runner uses packet-relative input paths and validates actual
hosted branch/head/context, source files, toolchain and future ROOT once JSON.

The installed workflow is byte-identical to the packet's ttl-preview-probe.yml.txt,
but its repository path is .github/workflows/storage005-capacity.yml. This is the
existing registered provider workflow path/ID381076044, rather than a newly
registered workflow. Saved original API evidence observed default-file404 and
then HTTP204/actual workflow_dispatch run38138782219 from an independent branch.
That evidence supports this planned route but does not guarantee future dispatch
acceptance. ROOT must freshly bind provider ID/path/state, exact published future
branch/head/workflow bytes and original run context before a single dispatch.

No commit, publication, pull request, network lookup, workflow dispatch, Go/gofmt,
SQL, Docker, CI or production change is authorized or executed by this preparation.
The authorization template remains false. ROOT's independent v2 source-review
receipt SHA is ccd24d91652eccd19b35d8d36f2f93edf9bf116f186bb2585f9a05ed1f90f2ff.

The existing three workflows remain at their original f80 bytes. A push of this
non-main branch without a tag matches none of their automatic push triggers.
Opening/reopening/updating a pull request selects all three without path filters:
Core checks, Public self-hosting acceptance and Verified CLI archives. Archived
CLI checks also invoke Core checks via workflow_call, so a PR is broader runtime
work than the single TTL query experiment. Both direct and called Core checks
retain the same five-platform native matrix and exact40required storage test names.
The installed TTL workflow itself has workflow_dispatch only. Publication and
any PR therefore require ROOT's concrete trigger-scope review before execution.

The query experiment remains16tests/20pairs/40originalplans on same schema005,
with all samples, rollback row+xmin digests and accounting. Admission remains
3GiB available host memory/2GiB free disk/2CPUs; ownfreshPG remains1GiB,
128MBshared/2CPUs/noextraswap. No measured speedup or full-maintenance result is
claimed. This branch is an isolated harness candidate, not a 1.0 acceptance result.
