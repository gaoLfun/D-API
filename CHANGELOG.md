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
- Upstream groups with group-scoped client keys, group routing, and group-level
  request-log and usage filters.
- Pricing profiles with built-in snapshots, per-upstream assignment, estimated
  USD cost, CNY display conversion, and cost coverage metrics.
- Built-in pricing refresh from LiteLLM's structured model-price file, with
  hashed source versions and atomic updates that preserve existing prices on
  download or parse failures.
- Historical cost backfill for unknown request costs, limited to 365 days and
  preserving existing cost values and retained aggregates.
- Dashboard topology view for inspecting client keys, group decisions, and
  normalized upstream clusters alongside the existing upstream status table.
- Per-upstream balance protection that pauses exhausted routes, automatically
  resumes recovered balances, and emits persisted notification events.
- Per-upstream User-Agent policies shared by gateway forwarding and operational
  probes, including Codex/OpenCode presets and custom values.
- Upstream dashboard cost details for today and lifetime, plus a transactional
  lifetime usage aggregate and retention of last successful balance metadata.
- Dashboard auto-refresh, period comparison, persistent filters, bulk upstream
  checks, route previews, client request simulation, and usage CSV export.
- Notification tabs with Webhook connectivity tests that keep channel secrets
  out of responses.
- Webhook payload adapters for DingTalk, Feishu/Lark, WeCom, Slack, Discord,
  and generic JSON with explicit provider selection or safe hostname detection.
- Alert coverage for client error rate, administrator login failures, and new
  login IPs, with persistent per-rule notification caps and recovery notices.
- Localized webhook event labels and UTC+8 timestamps, including DingTalk
  Markdown formatting for operational alerts.
- Global/per-key concurrency limits, per-key request-rate limits, and a maximum
  gateway request lifetime.
- Management API, troubleshooting guide, documentation index, release guide,
  third-party notices, and GitHub code ownership metadata.

### Changed

- Bounded and coalesced pending operational notifications, with up to three
  in-task retries for persisted security and state-transition events.
- Separated notification and system-settings views in the admin console, with
  dedicated controls for routing attempts and administrator password changes.
- Improved upstream bulk-selection feedback with accessible indeterminate states
  and clarified the maximum-attempts routing notice in the admin console.
- Reorganized the repository documentation around a Chinese default README,
  retained English reference documents, and added sanitized dashboard screenshots.
- Documented Responses, Chat Completions, and Anthropic Messages compatibility,
  model probes, usage dimensions, cache metrics, and failover semantics.
- Documented the default security boundary: loopback port 18083, explicit proxy
  trust, encrypted secrets, backup requirements, and direct-IP HTTP limitations.
- Reduced database work on routing, API-key authentication, dashboard, alert,
  usage, and request-log write paths; background probes use a bounded worker pool.
- Request, usage, audit, alert, and expired-session cleanup now runs in bounded
  batches under the configured retention period.

### Fixed

- Alert state now ignores unknown balance observations, sends explicit recovery
  messages, and preserves failed firing or recovery notifications for retry.
- Webhook delivery now detects common application-level rejection payloads even
  when providers return an HTTP 2xx response.
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
