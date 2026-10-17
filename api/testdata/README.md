# Shared project and collection examples

`project-contracts.json` is a single test corpus for Go, OpenAPI/schema checks and
the Python/TypeScript SDKs. It contains synthetic edit decisions and source
metadata, with no media, credentials or network dependency.

The `schema_version: 1` collection cases keep separate expectations for existing
layers:

- `valid` is semantic `NormalizeCollection` validation in Go, before media checks.
- `schema_valid` is structural OpenAPI `CollectionRequest` validation. JSON
  Schema does not establish source identity, selector membership, cross-field
  validity or configured aggregate budgets.
- `sdk_valid` checks the SDK's public portable-export structure gate. SDKs
  preserve edit decisions for the core; they do not perform full media or budget
  validation. A structurally accepted edit can therefore have `valid: false`.
- Successful Go cases name `selected_clip_ids` in plan order and literal
  `effective_options` by clip ID, including null audio/crop selections.

Go cases use a local `fixture.mp4` source with duration 120000 ms and limits of
128 ranges, 120000 ms per range and 600000 ms in total. A case's `source` or
`limits` overrides the named values. These are test settings, not server defaults.

`portable_cases` separately exercise SDK export/import. Supplied `source` and
`trusted_source` are explicit inputs; `document`, `expected_document` and
`expected_request` are literal JSON expectations. These cases do not imply a Go
or HTTP portable-document importer.

Source SDK tests read this file directly. Installed tests use
`CUTMY_PROJECT_CONTRACTS`, which `scripts/check-sdks.py` sets to a frozen copy in
the external evidence directory. The corpus hash is included in `SDK-CHECKS.json`
source inputs and checked against the resolved release source when sealing the
SDK manifest. The corpus is test input, not a runtime package dependency. See
[SDK verification](../../docs/sdk.md#verification).
