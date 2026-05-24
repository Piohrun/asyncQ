#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

"$ROOT_DIR/scripts/start-demo-q.sh"
"$ROOT_DIR/scripts/start-demo-grafana-local.sh"
