# Configuration

D-API reads configuration from environment variables. Docker Compose loads the
project's `.env` file for interpolation; the application does not parse `.env`
files itself.

Values are read at process startup. Change `.env` and recreate the `dapi`
container; changing a value in the running container does not reload it.
Environment variables control process and database settings only. Upstream,
client-key, notification, alert, and routing settings are stored in PostgreSQL
through the admin console.

## Required Secrets

| Variable | Requirement | Purpose |
| --- | --- | --- |
| `DAPI_MASTER_KEY` | Base64 encoding of exactly 32 bytes | AES-256-GCM key for stored credentials |
| `DAPI_ADMIN_USERNAME` | Required when the first administrator is created | Initial administrator name |
| `DAPI_ADMIN_PASSWORD` | Required by Compose; minimum 12 characters when used | Initial administrator password or reset-password value |
| `POSTGRES_PASSWORD` | Required by `compose.yaml` | Password for the Compose PostgreSQL user |

Generate the master key with:

```sh
openssl rand -base64 32
```

Use unrelated values for the database password, administrator password, and
master key. After the first administrator exists, runtime bootstrap variables
do not overwrite that account. Compose still requires `DAPI_ADMIN_PASSWORD` to
be present; change the actual password in the admin console or with the reset
command documented in [Deployment](deployment.md).

## Application Variables

| Variable | Default | Description |
| --- | --- | --- |
| `DAPI_ADDR` | `:8080` | Address used by the Go HTTP server |
| `DAPI_WEB_DIR` | `web/dist` | Directory containing the built admin SPA |
| `DAPI_SESSION_TTL` | `24h` | Administrator session lifetime |
| `DAPI_LOG_RETENTION` | `720h` | Request-log retention; cleanup runs daily |
| `DAPI_HEALTH_INTERVAL` | `30s` | Automatic upstream health-check interval |
| `DAPI_BALANCE_INTERVAL` | `10m` | Automatic balance-check interval |
| `DAPI_TRUST_PROXY` | `false` | Trust a valid `X-Real-IP` supplied by the immediate proxy |

Gateway resource limits:

| Variable | Default | Description |
| --- | --- | --- |
| `DAPI_MAX_CONCURRENT_REQUESTS` | `256` | Maximum concurrent gateway requests |
| `DAPI_MAX_CONCURRENT_PER_KEY` | `32` | Maximum concurrent requests per client key |
| `DAPI_MAX_REQUESTS_PER_MINUTE` | `600` | Per-key fixed-window request limit |
| `DAPI_MAX_REQUEST_DURATION` | `15m` | Maximum lifetime of one proxied request |

Durations use Go duration syntax, for example `30s`, `10m`, or `24h`. Empty,
invalid, or non-positive optional durations fall back to their defaults.

Positive integer limits also fall back to their defaults when empty, invalid, or
non-positive. Upper bounds are enforced by the process: 10,000 global requests,
1,000 requests per key, 100,000 requests per minute, and 24 hours per request.
The configured value is printed in the startup log without secrets.

`DAPI_TRUST_PROXY=true` is safe only when clients cannot reach D-API without
passing through a trusted proxy that overwrites `X-Real-IP`. The supplied Caddy
configuration does so. Set it to `false` for direct access; D-API then strips
forwarding headers and uses the socket address for audit logs and login limits.

## Database Connection

Set either a full URL or split connection variables. A non-empty
`DAPI_DATABASE_URL` takes precedence.

| Variable | Default | Description |
| --- | --- | --- |
| `DAPI_DATABASE_URL` | none | PostgreSQL URL, including desired `sslmode` |
| `DAPI_DATABASE_HOST` | `postgres` | Database host when no full URL is set |
| `DAPI_DATABASE_PORT` | `5432` | Database port |
| `DAPI_DATABASE_NAME` | `dapi` | Database name |
| `DAPI_DATABASE_USER` | `dapi` | Database user |
| `DAPI_DATABASE_PASSWORD` | none | Database password; required for split configuration |
| `DAPI_DATABASE_SSLMODE` | `disable` | lib/pq SSL mode |

The split form safely URL-encodes passwords containing reserved characters and
is used by `compose.yaml`. For external PostgreSQL, enable an SSL mode appropriate
for that server rather than copying the Compose-only `disable` value.

Compose passes an optional `DAPI_DATABASE_URL` through to the application. When
it is non-empty, it takes precedence over the split variables, while the bundled
PostgreSQL container still uses `POSTGRES_PASSWORD` for its own initialization.

Example full URL:

```sh
export DAPI_DATABASE_URL='postgres://dapi:password@db.example:5432/dapi?sslmode=require'
```

## Compose Variables

These variables are interpreted by `compose.yaml`, not by the Go process.

| Variable | Default | Description |
| --- | --- | --- |
| `POSTGRES_PASSWORD` | required | PostgreSQL container password and D-API DB password |
| `DAPI_DOMAIN` | `localhost` | Caddy site address used for HTTPS |
| `DAPI_BIND` | `127.0.0.1:18083` | Host address and port mapped to D-API port 8080 |

The loopback `DAPI_BIND` lets the host inspect D-API while normal public traffic
uses Caddy on ports 80 and 443. Binding D-API to `0.0.0.0` creates a separate
unencrypted HTTP entry point and should only be used temporarily with
`DAPI_TRUST_PROXY=false` and a restrictive firewall.

`DAPI_BIND` is a Compose interpolation value, not an application environment
variable. The application always listens on its container `DAPI_ADDR` (default
`:8080`); Compose maps the host address and port to that listener.

## Runtime Settings in the Admin Console

The following values are stored in PostgreSQL rather than environment variables:

- Upstream URLs, credentials, priorities, protocols, models, aliases, timeouts,
  failure thresholds, cooldowns, and enabled state
- Upstream groups, their enabled state and members, and the group assigned to
  each client key
- Client API keys and their protocol/model restrictions
- Maximum upstream attempts per request (1 to 5, default 3)
- Notification channels and alert rules

The built-in cleanup job runs once per day and removes request logs, daily usage,
audit entries, alert events, and expired sessions older than `DAPI_LOG_RETENTION`.
Cleanup is best effort and does not replace PostgreSQL backups or partitioning
for very large installations.

NewAPI's optional Access Token and User ID are used only for compatible balance
queries. Normal forwarding always uses the upstream API Key. Sub2API upstreams
do not retain Access Token or User ID values.

## Test-Only Variable

`DAPI_TEST_DATABASE_URL` enables PostgreSQL integration tests. Tests create an
isolated schema inside that database and skip when the variable is absent.
