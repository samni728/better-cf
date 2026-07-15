#!/usr/bin/env bash
set -euo pipefail

VPS_HOST="${VPS_HOST:-}"
VPS_USER="${VPS_USER:-root}"
REMOTE_DIR="${REMOTE_DIR:-/root/cf-betterip/source/}"
LOCAL_DIR="${LOCAL_DIR:-$(pwd)/}"

if [[ -z "${VPS_HOST}" ]]; then
  echo "VPS_HOST is required. Example: VPS_HOST=your-vps-ip ./scripts/pull-from-vps.sh" >&2
  exit 1
fi

SSH_OPTS=(
  -o StrictHostKeyChecking=accept-new
  -o ConnectTimeout=8
)

RSYNC_RSH=(ssh "${SSH_OPTS[@]}")

if [[ -n "${VPS_PASSWORD:-}" ]]; then
  if ! command -v sshpass >/dev/null 2>&1; then
    echo "sshpass is required when VPS_PASSWORD is set." >&2
    exit 1
  fi
  export SSHPASS="$VPS_PASSWORD"
  RSYNC_RSH=(sshpass -e ssh "${SSH_OPTS[@]}")
fi

rsync -az \
  --exclude ".git/" \
  --exclude "bin/" \
  --exclude "data/" \
  --exclude "logs/" \
  -e "${RSYNC_RSH[*]}" \
  "${VPS_USER}@${VPS_HOST}:${REMOTE_DIR}" \
  "${LOCAL_DIR}"

echo "Synced ${VPS_USER}@${VPS_HOST}:${REMOTE_DIR} -> ${LOCAL_DIR}"
