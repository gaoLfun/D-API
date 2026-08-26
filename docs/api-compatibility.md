# API Compatibility

D-API v0.1.0 exposes a small pass-through subset of OpenAI- and
Anthropic-compatible HTTP APIs. Compatibility means routing request and response
payloads in the same protocol; D-API does not implement every vendor endpoint or
translate between protocols.

This document covers client-facing `/v1/*` routes. The cookie-authenticated
administrator surface is documented separately in [Management API](admin-api.md)
and is not part of the compatibility promise.

## Supported Routes

| Route | Family | Behavior |
| --- | --- | --- |
| `GET /v1/models` | Models | Locally builds a model list from eligible upstream configuration |
| `POST /v1/chat/completions` | Chat | OpenAI Chat Completions pass-through |
| `POST /v1/responses` | Responses | OpenAI Responses pass-through |
| `POST /v1/messages` | Messages | Anthropic Messages pass-through |

Other `/v1/*` routes are not implemented.

## Client Authentication

Create client keys in the admin console. Upstream keys are never valid as D-API
client credentials unless separately created as a client key.

- All routes accept `Authorization: Bearer <DAPI_KEY>`.
- `POST /v1/messages` also accepts `x-api-key: <DAPI_KEY>` and prefers it when
  both headers are present.
- An enabled client key may restrict proxy capabilities and model names.
- `/v1/models` applies the key's model restriction and only lists models from its group.

Each client key is bound to one enabled group. Requests are routed only through
that group's upstream members; there is no fallback to another group or to the
global upstream pool.

## Request Handling

Proxy requests must contain a JSON object with a non-empty top-level `model`.
The maximum request body is 32 MiB. D-API otherwise forwards the payload without
schema validation or protocol conversion.

When a model alias is configured, D-API rewrites only the top-level `model`
field before forwarding. Query parameters and end-to-end headers are retained,
while hop-by-hop headers and client authentication headers are replaced with the
selected upstream API key. For Messages requests, the upstream receives both
Bearer authorization and `X-Api-Key`.

An upstream Base URL may end at the host or include `/v1`; D-API avoids adding a
second `/v1`. Redirects from upstream API and probe requests are not followed.

## Models

`GET /v1/models` returns a sorted OpenAI-style list derived from models stored
for enabled, non-circuit-open upstreams and filtered by the client key. Model
entries use `owned_by: "dapi"` and `created: 0`; D-API does not proxy this call to
a single upstream.

Health checks discover models through each upstream's `GET /v1/models`. Manual
model selection can lock the list so later probes do not replace it. An empty
upstream model list permits routing any requested model, but it contributes no
entries to D-API's `/v1/models` list.

The admin console's **model test** is a separate real-request check. NewAPI
tests follow its channel tester's endpoint convention: regular models use Chat
Completions while Codex-like models use Responses. Sub2API tests first issue one
HEAD request to the endpoint origin, then run the selected models through the
native protocol adapters concurrently. Each Sub2API request contains a
per-run few-shot arithmetic challenge and is accepted only when the expected
number appears in the response; successful responses taking six seconds or
more are reported as degraded. NewAPI Responses probes use an input message
array; Sub2API Responses probes use `instructions` and a string `input`. These
tests are explicit admin actions and may consume provider quota; they are never
triggered by ordinary client traffic or background health checks.

## Failover

Eligible upstreams are tried from lowest numeric priority upward, limited by the
admin setting `max_attempts` (default 3, range 1 to 5).

D-API tries the next upstream after:

- A connection, first-byte, or response-idle timeout
- Another transport or response-read failure before the response is committed
- HTTP 401, 403, 404, 429, or any 5xx response

Other HTTP statuses, including most 4xx responses, are returned without another
attempt. Repeated failures can open the upstream circuit; 401 and 403 open it
immediately. Disabled and currently circuit-open upstreams are not candidates.

When every attempted upstream is rate-limited, D-API returns 429. When every
attempt times out, it returns 504. Other exhausted failover paths return 502.

## Streaming

Set the protocol's normal `stream` field to `true`; D-API relays the upstream
body and flushes chunks to the client. It can fail over while waiting for the
first byte. Once any byte has been sent, HTTP response commitment makes a safe
retry impossible, so a later upstream interruption terminates the existing
stream and is recorded in the request log.

D-API does not reconstruct events, resume streams, or deduplicate partial output.

## Response Headers

Proxied responses include:

| Header | Meaning |
| --- | --- |
| `X-DAPI-Request-ID` | Generated request identifier used in logs |
| `X-DAPI-Upstream` | Selected or last-attempted upstream name |
| `X-DAPI-Attempts` | Number of attempted upstreams |

`GET /v1/models` includes the request ID and reports zero attempts because it is
built locally.

## Error Shapes

Chat Completions and Responses gateway errors use an OpenAI-style `error`
object. Messages gateway errors use Anthropic-style `type: "error"` and nested
error type/message fields. Error compatibility is intentionally limited to this
shape; vendor-specific error fields are not synthesized.

Non-retryable upstream responses are passed through as received.

## Usage and Logs

For non-streaming responses, D-API reads common OpenAI/Anthropic token fields
from the top-level `usage` object. For server-sent events, it looks for usage in
`usage`, `response.usage`, or `message.usage`. Recognized values include input or
prompt tokens, output or completion tokens, cached-input variants, and
`cache_creation_input_tokens` (cache writes).

The administrator log records total duration, TTFB, and streaming TTFT in
integer milliseconds, plus the upstream attempt chain. Anthropic Messages usage
counts `input_tokens` and cache-creation tokens as uncached input; OpenAI-style
usage treats cached input as a subset of total input. The usage report can split
daily, weekly, or monthly totals by upstream, client key, protocol, or model,
with Top-N/other aggregation and average/P95 latency. Group is also available as
a usage dimension.

Usage is best effort: missing fields remain unknown in administrator views. D-API
does not calculate token counts, prices, or billable cost. It stores request
metadata, attempts, latency, status, client IP, and discovered usage; request and
response bodies are not stored.

## Balance Discovery

Balance display is separate from request compatibility. D-API probes several
known NewAPI/Sub2API-style endpoints, including `/v1/usage`, billing
subscription, token usage, and user-self endpoints. Providers may omit or alter
these non-standard APIs, in which case the balance is shown as unknown or
unavailable without preventing normal request forwarding.

NewAPI Access Token plus User ID can be supplied for compatible user-self
balance queries. They are not used to forward model requests. Sub2API uses only
its upstream API key.
