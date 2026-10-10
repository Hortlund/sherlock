#!/bin/sh
set -eu
umask 077
if [ "$#" -ne 1 ]; then
  echo 'Usage: scripts/backup.sh NEW_BACKUP_DIRECTORY' >&2
  exit 2
fi
if [ -e "$1" ]; then
  echo 'Backup destination already exists; choose a new directory.' >&2
  exit 1
fi
mkdir -m 700 -- "$1"
cp .env "$1/installation.env"
docker compose exec -T postgres pg_dump -U sherlock -d sherlock -Fc > "$1/database.dump"
echo 'Backup saved. Protect both files; the environment file contains encryption keys.'
