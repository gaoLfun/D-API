# Support

## Supported Requests

Use GitHub Issues for:

- reproducible D-API bugs; and
- focused feature requests that fit the project's lightweight, single-administrator scope.

Before opening an issue, search existing reports and use the provided template. Include a minimal reproduction and remove all secrets.

Start with [故障排查](docs/troubleshooting.md) for common Compose, database,
TLS, upstream, model, quota, and client-key problems. API callers should also
read [API 兼容性](docs/api-compatibility.md) and administrators should read
[管理 API](docs/admin-api.md).

## Out of Scope

The project does not provide individual deployment, server administration,
networking, reverse-proxy, database recovery, upstream-provider, quota/billing,
or environment-specific troubleshooting support. Linux with Docker Compose is
the officially supported deployment target; other environments are best effort.

The following are intentional product boundaries rather than bugs: one
administrator, process-local rate limits, no protocol translation, no price
calculation, best-effort balance discovery, and no stream replay after the first
byte has been sent.

Security vulnerabilities must be reported privately according to [SECURITY.md](SECURITY.md), never through a public issue.
