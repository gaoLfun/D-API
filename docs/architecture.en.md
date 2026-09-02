# Architecture

[中文](architecture.md)

This document describes the current D-API `main` branch. It is not a roadmap or
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

Running multiple D-API replicas is not an officially supported topology.
Some rate-limit and notification cooldown state is process-local.

The browser uses the same origin for the SPA and management API. The management
surface uses a session cookie; client traffic uses independently created `dapi_`
API keys. These authentication paths do not share credentials or permissions.

## Request Flow

1. D-API hashes the presented client API key and looks up an enabled key.
2. It validates the key's protocol and model restrictions.
3. It loads only the key's enabled group members, ordered by numeric priority,
   then excludes disabled, circuit-open, protocol-incompatible, and
   model-incompatible entries.
4. It tries up to the configured maximum attempts within that group, which
   defaults to 3 and is constrained to 1 through 5.
5. For a model alias, only the request's top-level `model` value is rewritten.
6. The response is returned with D-API request, upstream, and attempt headers.
7. Request metadata, attempts, result, total/first-byte/first-token latency,
   client IP, and available token usage are recorded. The request and response
   bodies are not persisted.

Client keys, route candidates, and `max_attempts` use short-lived in-process
caches. Related admin mutations invalidate them immediately. Client-key and
route caches are bounded to 4,096 and 1,024 entries respectively, and route
entries expire within two seconds, so PostgreSQL remains the source of truth.

Upstreams with the same priority retain database order. There is no weighted,
random, cost-aware, or least-latency load balancing at present.

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
- The admin upstream form also provides an explicit model probe. NewAPI probes
  follow its endpoint selection convention: regular models use Chat and
  Responses/Codex models use Responses, with the configured protocol list
  constraining what is tested. Sub2API probes first send one HEAD request to
  the endpoint origin, then send a small arithmetic challenge through the
  selected protocol; the expected number is generated per run. Sub2API model
  responses slower than six seconds are marked degraded. Model probes are
  foreground admin actions, are not part of the 30-second health loop, and can
  consume upstream quota.
- Balance probes try known NewAPI/Sub2API-style endpoints. The default interval
  is 10 minutes; unsupported balance APIs are reported as unknown/unavailable.
  Disabled upstreams are also checked so a recovered balance can clear a suspension.
  Per-upstream balance protection pauses routing after two consecutive automatic
  zero-balance results (or one manual refresh), persists that state in PostgreSQL,
  and resumes after the first positive or unlimited balance result. Failed balance
  queries break the confirmation sequence without resuming a paused upstream.
  Pause/resume transitions are persisted as operational events and delivered to
  enabled notification channels without requiring an alert rule.
- The pricing refresh job downloads the structured LiteLLM model price file once
  per day and updates the managed OpenAI, Anthropic, and Google Gemini profiles
  atomically. Custom profiles remain user-managed and provide the fallback for
  models not covered by LiteLLM.
- Alert rules are evaluated once per minute and enqueue email or webhook
  notifications in the PostgreSQL outbox. A worker polls every five seconds,
  waits ten seconds to aggregate up to 50 ready jobs, and groups events by
  type, state, severity, and channel. Each channel retries failed deliveries
  after 10 seconds, one minute, and five minutes, then keeps the job as a
  dead letter after five attempts. Latency alerts average each upstream
  attempt in the observation window and remain unknown when no samples exist.
- Transient degraded states and their recovery are persisted without a notification.
  An incident is delivered once; recovery is confirmed after three consecutive scheduled
  or manual probe successes, or a two-minute recovery period. A failure resets the progress,
  while successful proxied requests do not advance recovery confirmation.
- Request logs older than the configured retention are removed once per day.
  Daily usage aggregates, audit entries, and alert events are retained for the
  configured `DAPI_LOG_RETENTION` window and removed in bounded batches.
- A separate `upstream_lifetime_usage` aggregate keeps per-upstream request and
  estimated-cost totals beyond daily-log retention. It is updated transactionally
  with request recording and historical cost backfill, so the dashboard can show
  lifetime totals without scanning or retaining every request log.

Health and balance jobs iterate over the upstream list; disabled upstreams still
receive balance probes so a recovered balance can clear a suspension automatically.

## Observability and Failure Isolation

Every proxied request receives a request ID and records an ordered attempt chain.
The record includes status, total duration, time to first byte, streaming time to
first token when observable, token metadata, and the client IP. Bodies are
deliberately excluded. Health failures update the upstream circuit in
PostgreSQL, while gateway concurrency and login limits remain in process memory.

The production gateway places request records in a bounded 2,048-entry queue and
flushes after 64 entries or 100 milliseconds. Daily, hourly, and upstream lifetime
increments are coalesced by dimension within each batch before PostgreSQL is
updated. A full queue falls back to synchronous writes for backpressure, and a
failed batch is retried with bounded backoff. Permanent failures increment a
dropped-record counter and are returned by graceful shutdown, which flushes
pending entries. If the shutdown deadline expires, the active write is canceled,
remaining records are counted as dropped, and the recorder worker stops before
the database is closed. A forced process termination can still lose the small
number of records that have not yet been flushed.

The admin dashboard's upstream panel provides a tabular view and a topology view.
The topology groups upstream entries by normalized base URL and shows the
client-key to group-decision to upstream-cluster path, while preserving the
priority, health, and balance-pause state used by the gateway. It is an
operational summary; actual routing still follows the persisted group membership
and SQL eligibility filters described above.

An upstream request is bounded by the global request lifetime, per-upstream
connect/first-byte/idle timeouts, and a maximum buffered response size. The
outbound network guard resolves and validates destinations again immediately
before dialing and disables redirects and environment proxy variables. This
keeps probes, webhooks, SMTP, and normal forwarding behind the same egress
boundary.

Each upstream may optionally override User-Agent. The same configured value is
used for gateway forwarding, health and balance probes, model discovery, and
explicit model tests so compatibility behavior stays consistent across paths.

## Data and Security Boundaries

- Upstream API keys, optional NewAPI balance credentials, and notification
  channel configurations are encrypted with AES-256-GCM using
  `DAPI_MASTER_KEY`.
- Pricing profiles and per-upstream assignments are stored in PostgreSQL. Costs
  are estimates derived from recorded Token usage and matching profile prices;
  D-API has no billing or quota-charging subsystem.
- Balance records retain the last successful usage fields (`used`, currency, and
  success timestamp) when a later probe is unavailable or incomplete; probe status
  remains separate from the last known successful values.
- Client API keys use SHA-256 hashes for authentication and an AES-256-GCM
  encrypted copy for administrator copy/CCSwitch import. Existing keys created
  before this encrypted copy was introduced cannot be recovered and must be
  recreated. Session tokens remain hash-only.
- Administrator passwords use bcrypt. Changing or resetting the password
  revokes existing sessions.
- Admin sessions use `HttpOnly`, `SameSite=Strict` cookies. The `Secure` flag is
  set when the request is HTTPS or a trusted proxy reports HTTPS.
- Admin mutations reject a mismatched `Origin` header. Login attempts are
  rate-limited per observed client IP and account identifier in process memory.
- `DAPI_TRUST_PROXY` defines whether `X-Real-IP` is trusted. The supplied Caddy
  configuration overwrites this header. Direct exposure must disable trust.
- Administrator-configured HTTP and SMTP destinations pass an outbound network
  guard. Loopback, private, link-local, multicast, carrier-grade NAT, and cloud
  metadata addresses are rejected both when saved and immediately before DNS
  connections; redirects are disabled.

The master key is deliberately not stored in PostgreSQL. Database backups are
not sufficient for recovery unless the same key is retained separately.

## Persistence and Migrations

The schema is embedded in the binary and applied at startup with idempotent SQL.
D-API currently has no separate migration command and no automated rollback.
PostgreSQL is the only supported database.

Deleting an upstream or client key removes the active configuration while
preserving historical request references where possible and retaining daily
usage identifiers for historical totals.

## Operational Limits

- Maximum client request body: 32 MiB.
- Maximum buffered non-streaming upstream response: 32 MiB.
- Health/balance probe response limit: 1 MiB.
- Gateway defaults: 256 concurrent requests globally, 32 per client key, 600
  requests per key per minute, and a 15-minute request lifetime.
- One administrator account is the intended deployment model.
- No distributed rate limiter, distributed scheduler, or external job queue.
- No protocol translation, request billing, or multi-tenant isolation.
