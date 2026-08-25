#!/bin/sh
set -eu

umask 077
destination="${1:-dapi-$(date -u +%Y%m%dT%H%M%SZ).sql.gz}"
sql_tmp=
archive_tmp=
trap 'rm -f "$sql_tmp" "$archive_tmp"' EXIT HUP INT TERM
sql_tmp=$(mktemp "${destination}.sql.XXXXXX")
archive_tmp=$(mktemp "${destination}.gz.XXXXXX")

docker compose exec -T postgres pg_dump -U dapi -d dapi > "$sql_tmp"
gzip -c "$sql_tmp" > "$archive_tmp"
mv "$archive_tmp" "$destination"
echo "$destination"
