# HTTP compatibility baselines

The older effective baseline is derived from `api/openapi.yaml` at immutable tag
`v0.2.1`, commit `f2be62c42a6a0d3d7a017b40bd683596bc578b95`.
[The original tagged document](https://github.com/Kay0k1/cutmyvideo-core/blob/v0.2.1/api/openapi.yaml)
is the provenance, rather than the modified working-tree specification.
Descriptions/examples are omitted, local references are expanded, and request,
response and header schemas are retained in `compatibility-v0.2.1.json`.

Two effective promises were normalized explicitly:

- `ranges[].label` has `x-maxBytes: 200` and `x-trimSpace: true`: the original
  schema already described 200 UTF-8 bytes and the tagged runtime trims Unicode
  whitespace before enforcing that limit (`internal/app/types.go`, Validate).
- The original three preview response headers are marked required: the tagged
  `servePreview` always sets cache/start/end headers before serving bytes.

The tagged source/artifact `expires_at` descriptions also contained unquoted
commas in YAML flow mappings. Their trailing prose became two inert, null-valued
schema keywords. Those exact fragments are removed from this effective snapshot;
the current descriptions are quoted. This repairs annotations without changing
the field types, nullability or date-time constraint.

These additions capture existing runtime/documented behavior, rather than
claiming this JSON snapshot is byte-identical to the old YAML. Reducing the label
bound to 100 or removing a guaranteed preview header fails the gate. Conversion
of the old URL/key `maxLength` to the same UTF-8 byte bound is an explicit
correction of the old character-versus-runtime-byte mismatch. Diagnostic
`code`/`error_code` fields remain open vocabularies; the old item error enum was
contrary to the already documented unknown-code fallback.

The additional `compatibility-v1.4.1.json` freezes the entire structural HTTP
contract at immutable tag `v1.4.1`, commit
`eb24d8e177de977da5e4a34b9b48ecabe14cf635`:
46 operations across 41 paths, including all 23 collection/project operations.
[The tagged specification](https://github.com/Kay0k1/cutmyvideo-core/blob/eb24d8e177de977da5e4a34b9b48ecabe14cf635/api/openapi.yaml)
has SHA-256
`d12ceac0881402fb9a625afd3bd1792188bdccdd89b9121e18bec0a19ea477b1`.
This snapshot stores paths and shared components with local references intact;
the checker expands those references in memory. Descriptions, examples, titles
and defaults are omitted from the snapshot and compatibility comparison. This
keeps its 35 named schemas in one approximately 42 KiB file without duplicating
large source/job/project schemas per operation. Its envelope records the source
commit, digest and normalization. No effective v0.2.1 promises or baseline bytes
were replaced by the newer snapshot.
Annotation removal respects named maps: response status `default` and properties
or components named `title`, `description` or `default` remain contract data.

Both checked-in files have independently pinned SHA-256 digests in the checker.
The ordinary check always applies both baselines; `--baseline PATH` adds an
expanded review snapshot and cannot replace either frozen check.
`--write-baseline PATH` only exports the supplied specification's expanded
contract for review. It refuses either protected baseline path, including
resolved path and symlink aliases. Editing or regenerating a checked-in baseline
fails its digest check, so an intentional baseline change also requires review
of its checker pin and provenance. Normal checks work offline and do not require
a Git checkout or access to tags.

The checker detects removed operations/parameters/response fields/headers,
new required inputs, incompatible types/enums, narrowed requests and weakened
response bounds. Changed validation keywords it cannot safely interpret require
explicit review. Validation siblings of `$ref` are rejected until supported
instead of silently discarding their constraints. Only offline local references
are supported. This is a conservative structural gate, not a general proof of
JSON Schema containment: cross-field timing rules, quotas, authorization,
provider availability and native runtime behavior need their own tests.

An intentional incompatible change needs a version/API decision and migration
note. Updating the baseline must be an explicit reviewed change with provenance;
do not regenerate it from current source merely to make a failed gate pass.
The [checker](../scripts/check-contract.py), its
[negative controls](../scripts/contract_test.py), and
[actual-handler fixtures](../internal/app/http_contract_test.go) run in CI.


## Reviewed 1.6.0 caller-selected output formats

WAV, FLAC and compatible AAC-in-M4A are explicit new output choices. The
unchanged historical snapshots still describe MP4/MP3/MKV. Requests using those
legacy choices retain their formats and defaults. A client selecting a new
format, or opening a project saved with it, can receive that same choice in
normalized preflight, collection and project plan/request echoes.

The checker permits only these three additions at27 explicitly listed historical
response positions. It requires the exact historical string/enum node shape,
preserves every old value (including collection inheritance's empty string),
and rejects other formats, unrelated response enum widening, changed types and
new validation constraints. It does not make all fields named `format` open.
Negative controls exercise every reviewed position. Baseline files and their
pinned hashes remain unchanged. Clients must preserve new selected formats or
report unsupported functionality; they must never treat an unfamiliar audio
container as MP4 or silently discard delivery controls.

The 1.6 subtitle retiming extension is also explicitly bounded: only the
`POST /api/v1/subtitles/retime` request's `version` can change from integer
constant 1 to the exact integer enumeration `[1, 2]`. Version 1 retains its
existing clipping semantics. Version 2 opts into scaling supplied cues across
actual source/output spans, including changed playback speed. The response
continues to use version 1. This exception permits neither response version
widening nor changes to other version fields; negative controls cover both.
