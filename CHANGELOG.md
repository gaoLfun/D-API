# Changelog

All notable changes to D-API will be documented in this file. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

- Per-model upstream tests following NewAPI and Sub2API channel-test behavior,
  including batch selection, progress, degraded latency, and audit summaries.
- Recoverable encrypted client-key copies, copy actions, gateway Base URL, and
  one-click CCSwitch import with optional model discovery.
- Request TTFB/streaming TTFT, input/output/cache-read/cache-write tokens, cache
  hit rates, multidimensional usage grouping, average/P95 latency, and charts.
- Global/per-key concurrency limits, per-key request-rate limits, and a maximum
  gateway request lifetime.
- Management API, troubleshooting guide, documentation index, release guide,
  third-party notices, and GitHub code ownership metadata.

### Changed

- Documented Responses, Chat Completions, and Anthropic Messages compatibility,
  model probes, usage dimensions, cache metrics, and failover semantics.
- Documented the default security boundary: loopback port 18083, explicit proxy
  trust, encrypted secrets, backup requirements, and direct-IP HTTP limitations.
- Reduced database work on routing, API-key authentication, dashboard, alert,
  usage, and request-log write paths; background probes use a bounded worker pool.
- Request, usage, audit, alert, and expired-session cleanup now runs in bounded
  batches under the configured retention period.

### Fixed

- Upstream create/update credential handling, conditional NewAPI balance fields,
  fetched-model selection, and model-aware connectivity checks.
- Client-key copying after creation and CCSwitch imports that use the selected
  key without forcing a model restriction.
- Stale frontend responses overwriting newer data, browser back/forward
  navigation, request cancellation, and keyboard access to log details.

### Security

- Added outbound URL validation and DNS-rebinding-resistant dialing for upstream,
  webhook, and SMTP destinations; environment HTTP proxies cannot bypass it.
- Documented secret handling, API-key recovery limits, log redaction, and the
  supported single-administrator threat model.
- Added login limits by IP and account, strict JSON/input validation, management
  response no-store headers, security response headers, and secret redaction for
  notification-channel list responses.
- Filtered forwarded request/response headers, bounded failed-response draining,
  capped connection pools, and serialized schema migration with an advisory lock.

## 0.1.0 - 2026-08-25

### Added

- Priority-based failover across NewAPI and Sub2API upstreams.
- OpenAI Responses, Chat Completions, and Anthropic Messages compatibility.
- Upstream health checks, connection testing, balance probes, and model discovery.
- Client API keys with protocol and model restrictions.
- Local usage accounting, request logs, audit logs, and circuit breaking.
- Configurable alert rules with email and webhook notifications.
- Single-administrator web console and password reset command.
- PostgreSQL persistence, encrypted upstream credentials, backup tooling, and Docker Compose deployment with Caddy.
