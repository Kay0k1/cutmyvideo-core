# Security policy

The latest 0.x release and current main branch are the target for security fixes.
Older revisions should be upgraded; there is no guaranteed response-time SLA or
independent security-audit claim. Keep the Go toolchain, media dependencies,
extractor and container base images updated as well as this repository.

## Report privately

Use [GitHub private vulnerability reporting](https://github.com/Kay0k1/cutmyvideo-core/security/advisories/new)
when the repository's private form is available. Include the affected release or
commit, reproducible steps, a synthetic fixture and the expected security
boundary. If private reporting is unavailable, open an issue asking for a private
contact channel **without exploit details or sensitive data**.

Do not include working credentials, session cookies, private source/CDN URLs,
other users' media or production environment files. Do not probe a public service
with destructive workloads; reproduce against an isolated installation.

## Boundaries

Files and URLs are untrusted. URL validation applies after redirects and to
extracted media destinations. Session ownership applies to metadata, previews,
jobs and downloads; an identifier alone does not authorize access. Media
processes have bounded transfer/output/time settings, with deployment resource
and network isolation as an additional boundary.

Public hosting requires HTTPS, secure cookies, trusted-proxy configuration,
persistent storage and measured CPU/RAM/PID/disk limits. Current API IP limiting
is process-local, so multiple API replicas need a shared limiter. Application
storage admission does not replace a filesystem quota. See the detailed
[security model](docs/security-model.md), [configuration](docs/configuration.md)
and [operator guide](docs/operations.md).
