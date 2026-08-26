# D-API

[![CI](https://github.com/gaoLfun/D-API/actions/workflows/ci.yml/badge.svg)](https://github.com/gaoLfun/D-API/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/gaoLfun/D-API?include_prereleases&sort=semver)](https://github.com/gaoLfun/D-API/releases)

[简体中文](README.zh-CN.md)

D-API is a lightweight, single-administrator gateway for NewAPI and Sub2API
upstreams. It routes supported AI API requests by priority and tries another
eligible upstream when a connection, timeout, or retryable HTTP error occurs.

Current version: **v0.1.0**. The project is usable, but its configuration and
management API may still change before v1.0.

## Features

- Priority-based routing across NewAPI and Sub2API upstreams
- Automatic failover, health checks, circuit breaking, and per-upstream timeouts
- OpenAI Responses, OpenAI Chat Completions, and Anthropic Messages endpoints
- Model discovery, allowlists, and client-to-upstream model aliases
- Upstream connectivity tests and best-effort balance discovery
- Locally recorded request attempts and token usage
- Group-scoped client API keys with protocol and model restrictions
- Optional pricing profiles with estimated USD cost and coverage metrics
- Single-administrator web console with audit logs and login rate limiting
- Email and webhook notifications with configurable alert rules
- One Go service, one Vue frontend, PostgreSQL, and an optional Caddy proxy
- Bounded concurrency, per-key rate limits, request lifetime limits, and SSRF protection

## Quick Start

The supported deployment target is Linux with Docker Compose v2. A DNS name is
recommended for public use.

```sh
cp .env.example .env
openssl rand -base64 32
```

Put the generated value in `DAPI_MASTER_KEY`, then set a separate
`POSTGRES_PASSWORD` and an administrator password of at least 12 characters in
`.env`. Keep the master key outside the database backup: losing it makes stored
upstream, notification, and client-key copies unreadable.

```sh
docker compose up -d --build
docker compose ps
```

Set `DAPI_DOMAIN` to a DNS name whose A/AAAA record points to the host, and allow
inbound TCP ports 80 and 443. Caddy obtains and renews HTTPS certificates. For a
local installation, the default address is `https://localhost`; its local CA
may need to be trusted by your browser.

The Compose file maps D-API itself to `127.0.0.1:18083` and keeps
`DAPI_TRUST_PROXY=false` by default. If Caddy is the only public entry point,
set `DAPI_TRUST_PROXY=true` so audit logs and login limits use the client IP;
never expose port 18083 publicly while proxy trust is enabled. See
[Deployment](docs/deployment.md) for temporary direct-IP HTTP access.

Log in with `DAPI_ADMIN_USERNAME` and `DAPI_ADMIN_PASSWORD`, then:

1. Add a NewAPI or Sub2API upstream and use **Fetch upstream models** before saving.
2. Select models and run **Test model** for one or more entries. This sends a small real model request (Sub2API uses an arithmetic challenge) and may consume upstream quota.
3. Create a group and bind one or more upstreams; client requests stay within the key's group.
4. Optionally assign a pricing profile to each upstream for estimated cost reporting.
5. Select supported protocols and models. Lower priority numbers route first.
6. Create a client API key and select its group. The list shows only its prefix; use the copy button or CCSwitch import. Keys created before encrypted copies were added must be recreated.

For production details, direct-IP HTTP access, upgrades, backups, and restores,
see [Deployment](docs/deployment.md). For a scriptable administrator interface,
see [Management API](docs/admin-api.md).

## Client Requests

Use a client key created in the admin console, not an upstream API key.

```sh
export DAPI_BASE_URL=https://api.example.com
export DAPI_KEY=dapi_replace_me

curl "$DAPI_BASE_URL/v1/models" \
  -H "Authorization: Bearer $DAPI_KEY"

curl "$DAPI_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $DAPI_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"hello"}]}'
```

The supported routes are:

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/messages`

D-API passes JSON and streaming responses through without translating between
protocols. See [API compatibility](docs/api-compatibility.md) for authentication,
retry behavior, streaming limits, usage collection, and response headers.

## Architecture

```text
Client -> Caddy (HTTPS) -> D-API -> priority-ordered upstreams in the key's group
                              |
                              +-> PostgreSQL
```

The Go process serves the admin UI and API gateway, runs health and balance
probes, evaluates alerts, and stores operational state in PostgreSQL. Upstream
credentials and notification channel secrets are encrypted with AES-256-GCM
before storage. Request bodies are not stored.

See [Architecture](docs/architecture.md) for routing, health, persistence, and
security boundaries.

## Production Checklist

- Use HTTPS and restrict the administrator console to a trusted network.
- Keep `DAPI_MASTER_KEY`, `.env`, database backups, and client-key creation
  responses outside source control and routine logs.
- Set a database `sslmode` suitable for external PostgreSQL; Compose uses
  `disable` only because the database is on its private Docker network.
- Review the configured upstream model allowlists and run a model test before
  enabling traffic. Tests are real requests and may consume quota.
- Configure log retention and a tested backup schedule. A database dump without
  the exact master key cannot recover encrypted secrets.
- Read [Security](SECURITY.md) and [Troubleshooting](docs/troubleshooting.md)
  before exposing a direct port.

## Documentation

- [Architecture](docs/architecture.md)
- [Configuration](docs/configuration.md)
- [Deployment, backup, and restore](docs/deployment.md)
- [API compatibility](docs/api-compatibility.md)
- [Management API](docs/admin-api.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Documentation index](docs/README.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Support policy](SUPPORT.md)
- [Changelog](CHANGELOG.md)
- [Third-party notices](THIRD_PARTY_NOTICES.md)
- [Release guide](RELEASING.md)

## Development

Local development requires Go 1.26, Node.js 26, and PostgreSQL 17. Build the
frontend before starting the Go service:

```sh
cd web
npm ci
npm run build
cd ..

export DAPI_DATABASE_URL='postgres://dapi:password@127.0.0.1:5432/dapi?sslmode=disable'
export DAPI_MASTER_KEY="$(openssl rand -base64 32)"
export DAPI_ADMIN_USERNAME=admin
export DAPI_ADMIN_PASSWORD='local-password-at-least-12-chars'
go run ./cmd/dapi
```

Run the checks used by the project:

```sh
go test ./...
go vet ./...
go test -race ./...
(cd web && npm test && npm run build && npm audit --audit-level=high)
docker build -t dapi:local .
```

PostgreSQL integration tests run only when `DAPI_TEST_DATABASE_URL` is set.

The CI workflow runs the same backend/frontend checks on every pull request.
Maintainers use [RELEASING.md](RELEASING.md) for versioning and GitHub Release
preparation.

## Scope

D-API is intentionally dedicated and small. v0.1.0 does **not** aim to provide:

- Multi-tenant accounts or organizations
- Reseller billing, quota charging, or payment processing
- Login to OpenAI, Anthropic, NewAPI, or Sub2API user accounts
- Protocol translation or a general-purpose reverse proxy
- A stable public management API

D-API is an independent compatibility project and is not affiliated with or
endorsed by NewAPI, Sub2API, CCSwitch, OpenAI, or Anthropic.

## License

Licensed under the [Apache License 2.0](LICENSE). Copyright 2026 gaoLfun.
See [third-party notices](THIRD_PARTY_NOTICES.md) for bundled font and dependency
licenses.
