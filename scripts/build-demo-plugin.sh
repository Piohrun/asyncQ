#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

npm run build:all

case "$(uname -m)" in
  x86_64 | amd64)
    GOARCH=amd64
    ;;
  aarch64 | arm64)
    GOARCH=arm64
    ;;
  armv7l | armv7)
    GOARCH=arm
    ;;
  *)
    echo "Unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

OUTPUT="dist/gpx_asyncq-kdbbackend-datasource_linux_${GOARCH}"
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -o "$OUTPUT" ./pkg
chmod +x "$OUTPUT"

echo "Built datasource plugin assets in $ROOT_DIR/dist"
echo "Built panel plugin assets in $ROOT_DIR/dist-panel/asyncq-masterdata-panel"
echo "Built Excel report panel plugin assets in $ROOT_DIR/dist-panel/asyncq-excel-report-panel"
echo "Backend binary: $OUTPUT"
