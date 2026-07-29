#!/bin/bash
set -euo pipefail

SCRIPT_SOURCE="${BASH_SOURCE[0]}"
SCRIPT_PARENT="${SCRIPT_SOURCE%/*}"
[[ "$SCRIPT_PARENT" != "$SCRIPT_SOURCE" ]] || SCRIPT_PARENT=.
ROOT_DIR="$(
  builtin cd -- "$SCRIPT_PARENT/.." &&
    builtin pwd -P
)" || {
  printf 'Error: could not resolve the repository root\n' >&2
  exit 1
}

# shellcheck source=scripts/demo-launch-common.sh
source "$ROOT_DIR/scripts/demo-launch-common.sh"
demo_validate_safe_path "$PATH"

demo_prepare_build_output_roots "$ROOT_DIR"
GOARCH="$(demo_resolve_build_goarch)"
OUTPUT="$ROOT_DIR/dist/gpx_asyncq-kdbbackend-datasource_linux_${GOARCH}"

cd "$ROOT_DIR"
npm run build:all

demo_prepare_build_output_roots "$ROOT_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -o "$OUTPUT" ./pkg
demo_publish_build_output_trees "$ROOT_DIR" "$GOARCH"
demo_validate_build_output_publication "$ROOT_DIR" "$GOARCH"

echo "Built datasource plugin assets in $ROOT_DIR/dist"
echo "Built panel plugin assets in $ROOT_DIR/dist-panel/asyncq-masterdata-panel"
echo "Built Excel report panel plugin assets in $ROOT_DIR/dist-panel/asyncq-excel-report-panel"
echo "Backend binary: $OUTPUT"
