# Architecture

This document describes D-API v0.1.0 as implemented. It is not a roadmap or a
stability guarantee.

## Components

```text
                              +----------------------+
Client ---- HTTPS ---- Caddy ->| D-API Go process     |
Admin ---- HTTPS -------------| - API gateway        |
                              | - Admin API and SPA  |
                              | - Health/balance jobs|
                              | - Alert evaluator    |
                              +----------+-----------+
                                         |
                                      PostgreSQL
                                         |
                        NewAPI/Sub2API <- routing/probes
```

- **Caddy** terminates TLS and proxies to D-API in the Compose deployment.
- **D-API** is one stateless Go process apart from in-memory connection pools,
  notification cooldowns, and login rate-limit counters.
- **PostgreSQL** stores configuration, encrypted secrets, sessions, health and
  circuit state, request metadata, usage aggregates, audit entries, and alerts.
- **Vue SPA** is built into static files and served by the Go process.

Running multiple D-API replicas is not an officially supported v0.1.0 topology.
Some rate-limit and notification cooldown state is process-local.

## Request Flow

1. D-API hashes the presented client API key and looks up an enabled key.
2. It validates the key's protocol and model restrictions.
3. It loads upstreams ordered by numeric priority, then excludes disabled,
   circuit-open, protocol-incompatible, and model-incompatible entries.
4. It tries up to the configured maximum attempts, which defaults to 3 and is
   constrained to 1 through 5.
5. For a model alias, only the request's top-level `model` value is rewritten.
6. The response is returned with D-API request, upstream, and attempt headers.
7. Request metadata, attempts, result, latency, client IP, and available token
   usage are recorded. The request and response bodies are not persisted.

Upstreams with the same priority retain database order. There is no weighted,
random, cost-aware, or least-latency load balancing in v0.1.0.

## Failover and Circuit State

D-API retries another eligible upstream on transport errors, configured
timeouts, HTTP 401, 403, 404, 429, and 5xx responses. Other HTTP responses are
returned directly.

Each upstream has connect, first-byte, and response-idle timeouts. A successful
attempt clears its consecutive failure count. Failures move health from unknown
or healthy to degraded, then unhealthy once the failure threshold is reached.
An unhealthy upstream is excluded until its cooldown expires. Authentication
failures (401 or 403) open the circuit immediately.

For streaming requests, D-API can switch upstreams only before it has written
the first response byte to the client. A failure after that point is recorded,
but the committed stream cannot be replayed safely.

## Background Work

- Health probes call the upstream `GET /v1/models` endpoint. The default
  interval is 30 seconds.
- Successful health probes update discovered models unless the administrator
  has locked the model list.
- Balance probes try known NewAPI/Sub2API-style endpoints. The default interval
  is 10 minutes; unsupported balance APIs are reported as unknown/unavailable.
- Alert rules are evaluated once per minute and can deliver email or webhook
  notifications.
- Request logs older than the configured retention are removed once per day.
  Daily usage aggregates are retained.

Only enabled upstreams are probed by background jobs.

## Data and Security Boundaries

- Upstream API keys, optional NewAPI balance credentials, and notification
  channel configurations are encrypted with AES-256-GCM using
  `DAPI_MASTER_KEY`.
- Client API keys and session tokens are stored as SHA-256 hashes. Client key
  plaintext is returned only when the key is created.
- Administrator passwords use bcrypt. Changing or resetting the password
  revokes existing sessions.
- Admin sessions use `HttpOnly`, `SameSite=Strict` cookies. The `Secure` flag is
  set when the request is HTTPS or a trusted proxy reports HTTPS.
- Admin mutations reject a mismatched `Origin` header. Login attempts are
  rate-limited per observed client IP in process memory.
- `DAPI_TRUST_PROXY` defines whether `X-Real-IP` is trusted. The supplied Caddy
  configuration overwrites this header. Direct exposure must disable trust.

The master key is deliberately not stored in PostgreSQL. Database backups are
not sufficient for recovery unless the same key is retained separately.

## Persistence and Migrations

The schema is embedded in the binary and applied at startup with idempotent SQL.
D-API v0.1.0 has no separate migration command and no automated rollback.
PostgreSQL is the only supported database.

Deleting an upstream or client key removes the active configuration while
preserving historical request references where possible and retaining daily
usage identifiers for historical totals.

## Operational Limits

- Maximum client request body: 32 MiB.
- Maximum buffered non-streaming upstream response: 32 MiB.
- Health/balance probe response limit: 1 MiB.
- One administrator account is the intended deployment model.
- No distributed rate limiter, distributed scheduler, or external job queue.
- No protocol translation, request billing, or multi-tenant isolation.
