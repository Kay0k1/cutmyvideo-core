# Security

Media sources and files are untrusted. The server must not access loopback,
private networks, cloud metadata, or arbitrary local paths on behalf of a URL
request. Source validation also applies after redirects and to extracted media
URLs. Media processes run with bounded time, transfer, output size, and resources.

Session ownership is checked for source metadata, previews, jobs, and downloads.
An identifier alone is not authorization. Production deployments must use HTTPS
and secure session cookies.

Please report vulnerabilities using GitHub's private vulnerability reporting on
this repository. Include reproducible steps with synthetic data. Do not place
working credentials or other users' media in reports.

The initial MVP is under active development. Supported source protocols and
operational limitations are listed in the README; unsupported streaming formats
must fail clearly rather than silently bypass the configured limits.
