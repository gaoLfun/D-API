# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

D-API is operated by a single technical administrator who monitors gateway health, investigates request failures, reviews usage, and manages upstreams, client keys, notifications, and alert rules.

## Product Purpose

D-API provides one focused control surface for operating a lightweight AI API gateway. Success means the administrator can understand current routing health quickly, diagnose a failed request, and change configuration without losing context or introducing avoidable risk.

## Positioning

D-API combines priority-based routing, automatic failover, health and balance monitoring, request-attempt history, usage records, and scoped client keys in one lightweight service for NewAPI and Sub2API upstreams.

## Operating Context

The primary environment is a desktop browser used for routine administration. Mobile access remains fully operable for urgent checks and small configuration changes. Monitoring, analysis, and configuration workflows have equal priority.

## Capabilities and Constraints

- The Vue application, backend APIs, database model, business rules, navigation
  labels, and primary information architecture are the implemented baseline.
- The login screen, application shell, six authenticated views, tables, charts,
  drawers, dialogs, and interaction states are complete for the v0.1 line.
- Automatic, light, and dark themes use a persisted preference and honor reduced
  motion.
- The application stays lightweight and does not use a component system or
  enterprise data-grid framework.
- Client-side tables provide low-risk affordances such as sorting, sticky
  headers, copying, disclosure rows, and compact overflow actions.
- Upstream and client-key lists assume no more than roughly 100 rows until real
  scale data is available. Request logs retain server-side pagination; usage
  queries are limited to 365 days and Top 100 dimensions.
- Single-administrator, single-process deployment is the supported boundary;
  rate limits and some scheduler state are process-local.

## Brand Commitments

- Keep the D-API name and its routing/network identity.
- Use the open-source CLI Proxy API management center as a structural and interaction reference, not as a pixel-for-pixel copy.
- The visual language is calm, spacious, light-first, and operational, with a cold neutral foundation and D-API green as the primary accent. Dark mode uses the same hierarchy rather than a separate identity.

## Evidence on Hand

- Product behavior and Chinese interface copy live in `web/src/App.vue`.
- Existing visual rules live in `web/src/styles.css`.
- Product and deployment facts live in `README.zh-CN.md` and `docs/`.
- API behavior and security limits live in `docs/api-compatibility.md`,
  `docs/admin-api.md`, and `SECURITY.md`.
- No approved screenshots, customer claims, or production usage-scale measurements are available.

## Product Principles

- Make system state easy to scan before adding visual expression.
- Keep frequent actions visible and move uncommon or destructive actions into contextual menus.
- Preserve context during configuration by using drawers for long forms and dialogs for short, interruptive tasks.
- Use motion for feedback and state transitions, never decoration.
- Keep every workflow complete in both themes and on mobile.

## Accessibility & Inclusion

Target WCAG 2.2 AA. Support keyboard navigation, visible focus, reduced motion, meaningful status text in addition to color, and readable contrast in both light and dark themes.
