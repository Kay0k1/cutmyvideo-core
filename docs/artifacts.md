# Checks and artifact retention

Validate user-visible behavior with small regression fixtures: playback while
exporting, exact cuts, source ownership, cancellation and resource bounds.
Performance measurements are optional diagnostics, not a reason to expand the
product scope or rerun a successful check without a changed implementation.

Keep code, fixture generators and a short outcome in Git. Do not commit dated
raw journals, provider archives, traces, sampled resources or generated media.
Historical observations retain their compact README and links to the original
Git revision; history is not rewritten. There is no need to publish a new
benchmark dossier for each change.

Use a temporary directory that is removed when the check finishes. A failing
check may retain its diagnostics in ignored `artifacts/`, with a seven-day local
retention limit. CI test diagnostics expire after three days. Release packages,
provenance, recovery snapshots, production media and database backups are separate
and keep their existing retention guarantees.

For legacy local workspace reports, preview cleanup with:

```sh
python3 scripts/prune-workspace-artifacts.py --root /absolute/workspace/reports
```

Add `--apply` to delete eligible generated files older than seven days. The
script retains short summaries, source files and small diagnostics; it refuses
production directories and symlinks. `--older-than-days 0` is an explicit cleanup
of all eligible completed diagnostics. Run only after their checks have ended.

`scripts/check-artifacts.py` rejects new bulky or raw dated results in the
tracked tree. The guard does not apply to actual source-media regression fixtures.
