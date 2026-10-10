# Server configuration

API, worker and maintenance read the same environment. Use matching media budgets,
database and `DATA_DIR` for all three. `clip` and `inspect` only read
`FFMPEG_PATH`/`FFPROBE_PATH`; configure other options through CLI flags.

Byte values are decimal integers, not `1GiB` strings. Durations use Go syntax
(`30s`, `2m`, `12h`), must be positive and may not exceed 30 days. These are
**code defaults**; the [Compose example](../deploy/compose.example.yml) uses
smaller upload/output/storage/range budgets for a local trial.

| Setting | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | Required | PostgreSQL DSN; keep credentials private |
| `DATA_DIR` | `./data` | Persistent shared media directory; resolved to an absolute path |
| `LISTEN_ADDR` | `:8080` | API listen address |
| `PUBLIC_ORIGIN` | Empty | Exact browser origin, e.g. `https://video.example.org` |
| `COOKIE_SECURE` | `false` | Literal `true` enables HTTPS-only cookies |
| `TRUST_PROXY` | `false` | Enable only behind a trusted proxy that normalizes forwarded client IPs |
| `MAX_SOURCE_BYTES` | 34359738368 (32 GiB) | Whole-file upload/direct download ceiling; also bounds remote staging |
| `MAX_FETCH_BYTES` | 34359738368 (32 GiB) | Aggregate guarded transfer budget for one export, across all ranges |
| `MAX_OUTPUT_BYTES` | 17179869184 (16 GiB) | Ceiling for each output |
| `MAX_STORAGE_BYTES` | 103079215104 (96 GiB) | Shared files plus processing reservations |
| `MAX_OWNER_BYTES` | 68719476736 (64 GiB) | Source/thumbnail bytes and source reservations per session |
| `MAX_RANGES` | 32 | Intervals per job; allowed 1–128 |
| `MAX_RANGE_MS` | 43200000 (12 h) | Selected duration per interval |
| `MAX_JOB_MS` | 86400000 (24 h) | Sum of selected durations per job |
| `MAX_ACTIVE_JOBS` | 32 | Global active jobs; allowed 1–1024 |
| `MUTATIONS_PER_MINUTE` | 20 | Per-IP mutation allowance, independent of cookies |
| `SOURCE_TIMEOUT` | `2m` | Platform inspection/import deadline |
| `UPLOAD_TIMEOUT` | `2h` | Upload/direct transfer plus inspection deadline |
| `JOB_TIMEOUT` | `12h` | Job processing deadline, including local processing-slot wait |
| `SOURCE_TTL` | `24h` | Retention target for unpinned sources |
| `ARTIFACT_TTL` | `24h` | Deadline assigned to newly published results |
| `STORAGE_SAFETY_BYTES` | 536870912 (512 MiB) | Actual disk headroom; zero allowed |
| `STORAGE_WAIT_TIMEOUT` | `30m` | Persisted deadline for accepted storage-waiting jobs |
| `WORKER_CONCURRENCY` | 1 | Local worker processing slots; allowed 1–8 |
| `FFMPEG_THREADS` | 2 | Codec threads per process; allowed 1–32 |
| `FFMPEG_PROFILE` | `fast` | `fast` favors CPU time; `compact` favors smaller files |
| `FFMPEG_PATH` / `FFPROBE_PATH` / `YTDLP_PATH` | Executable names | Media/extractor executables |
| `WORKER_HEALTH_PATH` | `/tmp/cutmy-worker-health` | Private local marker; keep off shared storage |
| `GOMEMLIMIT` | Go default; `128MiB` in Docker | Soft Go memory target; does not limit FFmpeg/container |

Positive byte limits are required; reservation arithmetic is checked for overflow.
A session can retain 20 source records and three active jobs. Source preparation
admits one per session and four globally. See the [API](api.md) for request limits
and fixed diagnostics.

Raising `MAX_SOURCE_BYTES` permits larger uploads without raising every short
remote staging reservation: remote producers use
`min(MAX_SOURCE_BYTES, MAX_FETCH_BYTES)`. Transfer is charged across the job.
Exact exports cap bitrate to fit the output ceiling; copy retains packets and can
fail the size limit. A 12-hour permitted interval does not guarantee completion
within the configured deadline or disk budget.

Outputs and bounded temporary inputs are reserved before processing. Accepted
jobs can enter `waiting_storage`; impossible reservations are rejected. Files
remain charged until physical deletion succeeds. This is application admission,
not a filesystem quota. Account for database, backups, logs and other host files.

Window previews have fixed separate limits: 30-second windows, 480p MP4,
90-second preparation, 64 MiB fetched input and 16 MiB output; 144 MiB processing
reservation. At most two render globally. Completed windows are charged at actual
size and cached for 30 minutes idle, up to 512 MiB/64 files globally and 128 MiB/16
per owner. These are not environment settings.

PostgreSQL pools default to four connections per process. Explicit `pool_max_conns`,
`pool_min_conns` and `pool_min_idle_conns` DSN options can alter the budget. Current
public hosting uses one API because IP rate limits are process-local. Multiple
workers share durable state; processing/RAM budgets remain local.

API database stages have a fixed five-second deadline, including connection-pool
acquisition and SQL lock waits. An earlier caller deadline wins. Media transfer
and processing retain their separate deadlines; the database deadline does not
limit the duration of an upload, export or download. Unavailable database
operations return HTTP 503 `database_unavailable`; this response does not prove
that a submitted mutation was never committed. See the [API error contract](api.md)
before retrying a mutation.

For custom `LISTEN_ADDR`, probe its `/readyz` directly: `cutmy healthcheck` currently
uses `http://127.0.0.1:8080/readyz`. Worker health validates successful queue/lease
access within 30 seconds rather than the existence of a process.
