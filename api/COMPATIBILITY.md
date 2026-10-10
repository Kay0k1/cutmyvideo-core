# HTTP compatibility baseline

The effective baseline is derived from `api/openapi.yaml` at immutable tag
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
