#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

"$ROOT_DIR/scripts/build-demo-plugin.sh"

cd "$ROOT_DIR/demo"
docker compose up -d

echo "Grafana is starting at http://localhost:3000"
echo "Login: admin / admin, or use anonymous admin access."
echo "Dashboard: http://localhost:3000/d/asyncq-kdb-demo/asyncq-kdb-demo"
echo "Compatibility matrix: http://localhost:3000/d/asyncq-compat-matrix/asyncq-panopticon-compatibility-matrix"
echo "Panopticon tests: http://localhost:3000/d/asyncq-pano-compat/asyncq-panopticon-compatibility-tests"
echo "Async tests: http://localhost:3000/d/asyncq-async-tests/asyncq-async-execution-tests"
echo "Master data/cache controls: http://localhost:3000/d/asyncq-masterdata-cache/asyncq-master-data-and-cache-controls"
echo "Excel reporting: http://localhost:3000/d/asyncq-excel-report/asyncq-excel-reporting"
echo "Business Suite smoke: http://localhost:3000/d/asyncq-business-suite/asyncq-business-suite-smoke"
