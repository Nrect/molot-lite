#!/bin/sh
# Nightly Postgres backup: pg_dump through the running container,
# gzipped, with 14-day rotation. Wire it into crontab on the VPS:
#   0 3 * * * /opt/molot-lite/deploy/backup.sh
set -eu

BACKUP_DIR="${BACKUP_DIR:-/var/backups/molotlite}"
COMPOSE_FILE="$(cd "$(dirname "$0")/.." && pwd)/docker-compose.prod.yml"
STAMP="$(date +%Y%m%d-%H%M%S)"

mkdir -p "$BACKUP_DIR"

docker compose -f "$COMPOSE_FILE" exec -T postgres \
    pg_dump -U molotlite molotlite \
    | gzip > "$BACKUP_DIR/molotlite-$STAMP.sql.gz"

# Rotation: anything older than 14 days goes away.
find "$BACKUP_DIR" -name 'molotlite-*.sql.gz' -mtime +14 -delete

echo "backup written: $BACKUP_DIR/molotlite-$STAMP.sql.gz"
