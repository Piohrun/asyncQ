#!/bin/bash
set -euo pipefail
umask 077

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

RUNTIME_DIR="$ROOT_DIR/demo/runtime"
PID_FILE="$RUNTIME_DIR/grafana.pid"
START_FILE="$PID_FILE.start"
EXPECTED_START=""
demo_require_real_directory "$ROOT_DIR/demo" "demo directory"
if [[ ! -e "$RUNTIME_DIR" && ! -L "$RUNTIME_DIR" ]]; then
  echo "No local Grafana demo PID file found"
  exit 0
fi
demo_secure_directory "$RUNTIME_DIR" "Grafana runtime directory"
command -v timeout >/dev/null 2>&1 || {
  demo_error "timeout is required to inspect and stop the Grafana demo"
  exit 1
}

if [[ ! -e "$PID_FILE" && ! -L "$PID_FILE" ]]; then
  demo_clear_pid_state "$PID_FILE" "$START_FILE"
  echo "No local Grafana demo PID file found"
  exit 0
fi

if ! PID="$(demo_read_pid_file "$PID_FILE" 2>/dev/null)"; then
  if ! demo_clear_pid_state "$PID_FILE" "$START_FILE"; then
    demo_error "refusing to alter invalid local Grafana PID state because it is linked or unsafe"
    exit 1
  fi
  echo "Removed invalid local Grafana PID state without signaling a process" >&2
  exit 0
fi

if ! kill -0 "$PID" 2>/dev/null; then
  echo "Local Grafana demo with PID $PID is not running"
  demo_clear_pid_state "$PID_FILE" "$START_FILE" || exit 1
  exit 0
fi

if ! EXPECTED_START="$(demo_read_start_file "$START_FILE" 2>/dev/null)"; then
  demo_error "refusing to signal live PID $PID without a safe, valid recorded start time"
  exit 1
fi
if ! demo_grafana_process_home "$PID" "$ROOT_DIR" "$RUNTIME_DIR" >/dev/null ||
  ! demo_process_group_is_safe "$PID" ||
  ! demo_process_instance_matches "$PID" "$EXPECTED_START"; then
  demo_error "refusing to signal PID $PID because it does not match the recorded Grafana demo process"
  exit 1
fi

if demo_stop_process_group \
  "$PID" \
  "$EXPECTED_START" \
  "" \
  demo_grafana_process_home \
  "$ROOT_DIR" \
  "$RUNTIME_DIR"; then
  echo "Stopped local Grafana demo with PID $PID"
  demo_clear_pid_state "$PID_FILE" "$START_FILE" || exit 1
  exit 0
fi

demo_error "Grafana demo process group $PID did not stop after TERM and KILL"
exit 1
