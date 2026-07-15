#!/usr/bin/env sh
set -eu

if docker compose version >/dev/null 2>&1; then
  exec docker compose "$@"
fi

if command -v docker-compose >/dev/null 2>&1; then
  exec docker-compose "$@"
fi

echo "Docker Compose is required. Install Docker Compose v2 or docker-compose v1." >&2
exit 1
