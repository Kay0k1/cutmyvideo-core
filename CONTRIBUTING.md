# Contributing

The project is a reusable media engine. Product interfaces and a particular
operator's deployment settings do not belong in this repository.

Use short, focused changes with a clear user-visible result. Update `CHANGELOG.md`
for behavior changes and document API changes before merging them. Keep exported
engine operations independent of HTTP handlers so other clients can reuse them.

## Verification

Run `go test ./...`, `go vet ./...`, and the documented PostgreSQL integration
tests. Media tests use synthetic fixtures generated with FFmpeg; do not commit
large videos, user uploads, credentials, or downloaded third-party content.

Changes to source fetching must test URL validation, redirects, bounded transfers,
and session isolation. Changes to trimming must distinguish requested boundaries
from actual boundaries and verify output streams and duration.

## Reporting problems

Include the engine version, source type, requested ranges, output settings, and
sanitized error. Omit cookies, private source links, tokens, and user data. Use the
security reporting guidance for vulnerabilities rather than a public issue.
