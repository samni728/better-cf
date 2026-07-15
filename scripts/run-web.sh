#!/usr/bin/env bash
set -euo pipefail

LISTEN_ADDR="${LISTEN_ADDR:-:18080}"
DATA_DIR="${DATA_DIR:-./data}"

go run ./cmd/cf-betterip-web --listen "${LISTEN_ADDR}" --data-dir "${DATA_DIR}"
