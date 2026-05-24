#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

"$ROOT_DIR/scripts/build-demo-plugin.sh"

cd "$ROOT_DIR/demo"
docker compose up -d

echo "Grafana is starting at http://localhost:3000"
echo "Login: admin / admin, or use anonymous admin access."
echo "Dashboard: http://localhost:3000/d/asyncq-kdb-demo/asyncq-kdb-demo"
