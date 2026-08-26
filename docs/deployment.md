# Deployment, Backup, and Restore

The supported v0.1.0 production target is a single Linux host running Docker
Compose v2. Other operating systems and bare-binary installations are best
effort. The stack is intentionally single-instance: rate-limit counters,
notification cooldowns, and scheduled probes are process-local.

## Prerequisites

- Docker Engine with the Compose v2 plugin
- A DNS A/AAAA record pointing to the host
- Inbound TCP ports 80 and 443
- Outbound HTTPS access to configured upstreams and certificate authorities
- Enough persistent storage for PostgreSQL and request logs
- A host firewall that limits PostgreSQL to the Compose network and, when
  applicable, limits the direct D-API port to localhost

## First Deployment

```sh
git clone https://github.com/gaoLfun/D-API.git
cd D-API
cp .env.example .env
openssl rand -base64 32
```

Edit `.env` and set at least:

```dotenv
DAPI_MASTER_KEY=replace-with-generated-value
POSTGRES_PASSWORD=replace-with-a-long-random-password
DAPI_ADMIN_USERNAME=admin
DAPI_ADMIN_PASSWORD=replace-with-at-least-12-characters
DAPI_DOMAIN=api.example.com
```

Start the stack:

```sh
docker compose up -d --build
docker compose ps
curl -fsS https://api.example.com/healthz
```

For a first installation, open the web console only after `/healthz` is healthy.
Use the configured administrator credentials, then change the password from the
console after confirming the first login. Do not put real upstream credentials
in a model test until the HTTPS path and backup procedure have been verified.

The `postgres_data` volume contains the database. `caddy_data` and
`caddy_config` contain Caddy state and certificates. Do not delete these volumes
during normal upgrades.

## Proxy and Network Boundary

The default Compose topology publishes Caddy on ports 80/443 and also maps D-API
to `127.0.0.1:18083`. Compose keeps `DAPI_TRUST_PROXY=false` by default; this
avoids trusting forged forwarding headers if the direct port is exposed.
When using the supplied Caddy proxy, set `DAPI_TRUST_PROXY=true` explicitly;
Caddy overwrites `X-Real-IP` with the connecting client address.

Do not expose port 18083 publicly while proxy trust is enabled. A client that
can bypass Caddy could forge the IP used for auditing and login rate limiting.

For temporary direct-IP testing without TLS, set:

```dotenv
DAPI_BIND=0.0.0.0:18083
DAPI_TRUST_PROXY=false
```

Then start only the database and app:

```sh
docker compose up -d --build postgres dapi
```

Access `http://SERVER_IP:18083` and restrict the port at the firewall. This mode
is not suitable for entering production credentials because traffic is cleartext.

## Routine Operations

```sh
docker compose ps
docker compose logs -f dapi
docker compose logs -f caddy
curl -fsS https://api.example.com/healthz
```

`/healthz` reports `200` only when D-API can reach PostgreSQL. It does not assert
that every configured upstream is healthy; use the admin console for upstream
status and individual connectivity checks.

The service emits structured Go application logs to stdout. Request attempts
and usage are stored in PostgreSQL and shown in the admin console; audit and
alert records are also stored in PostgreSQL. Request bodies are not stored.
Logs can contain request IDs, upstream names, model names, client IPs, status,
timings, and error summaries. They must be treated as operationally sensitive;
never forward them unredacted to a third-party log service.

## Administrator Password Reset

The admin console can change the password. If login is unavailable, set the
existing username and a new password in the command environment and run:

```sh
docker compose run --rm \
  -e DAPI_ADMIN_USERNAME=admin \
  -e DAPI_ADMIN_PASSWORD='new-password-at-least-12-chars' \
  dapi reset-password
```

The command updates the named existing administrator and revokes its sessions.
It does not create another administrator.

## Backup

Run backups from the repository directory while PostgreSQL is running:

```sh
mkdir -p /secure/backups
./deploy/backup.sh /secure/backups/dapi-$(date -u +%Y%m%dT%H%M%SZ).sql.gz
```

The script uses `pg_dump`, gzip compression, restrictive file permissions, and
an atomic final rename. It exits without replacing the destination if dumping
or compression fails. The dump contains the D-API database schema and data, but
not PostgreSQL cluster roles or the external `.env` file.

Back up these separately:

- The exact `DAPI_MASTER_KEY` required to decrypt stored secrets
- `.env` or an equivalent secret-manager record
- Any external proxy, firewall, or DNS configuration

Treat the SQL archive as sensitive even though application credentials are
encrypted. Test restores periodically and keep copies off the application host.

## Restore

Restoring replaces the current D-API database. Confirm the archive path and take
a fresh backup before proceeding.

1. Stop components that access the database, leaving PostgreSQL running:

   ```sh
   docker compose stop dapi caddy
   ```

2. Recreate the database:

   ```sh
   docker compose exec -T postgres dropdb --if-exists --force -U dapi dapi
   docker compose exec -T postgres createdb -U dapi -O dapi dapi
   ```

3. Restore the dump:

   ```sh
   gzip -dc /secure/backups/dapi-TIMESTAMP.sql.gz \
     | docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U dapi -d dapi
   ```

4. Ensure `.env` contains the same master key used when the backup was made,
   then restart and verify:

   ```sh
   docker compose up -d dapi caddy
   docker compose ps
   curl -fsS https://api.example.com/healthz
   ```

A different or missing master key allows the database to start but prevents
encrypted upstream and notification credentials from being decrypted.

## Upgrade

Back up first, review the changelog, then rebuild while preserving `.env` and
named volumes:

```sh
git pull --ff-only
docker compose up -d --build
docker compose ps
```

The application applies its embedded idempotent schema at startup. v0.1.0 does
not provide automatic schema rollback, so retain a pre-upgrade backup.

After an upgrade, verify both the direct health endpoint and the public proxy,
then perform one low-cost request with a test client key. If the new process
cannot start, keep the previous image available and restore the pre-upgrade
database backup before retrying; changing only the image may not reverse a
schema change.

## Uninstall

`docker compose down` removes containers and the Compose network but preserves
named volumes. `docker compose down -v` also deletes the database and Caddy data;
use it only when permanent data loss is intended and a verified backup exists.
