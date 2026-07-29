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

PID_FILE="$ROOT_DIR/demo/logs/q.pid"
START_FILE="$PID_FILE.start"
LOG_DIR="$ROOT_DIR/demo/logs"
RUNNER="$ROOT_DIR/scripts/run-demo-q-session.sh"
Q_SCRIPT="$ROOT_DIR/demo/q/asyncq_demo.q"
EXPECTED_START=""
demo_require_real_directory "$ROOT_DIR/demo" "demo directory"
if [[ ! -e "$LOG_DIR" && ! -L "$LOG_DIR" ]]; then
  echo "No AsyncQ demo q PID file found"
  exit 0
fi
demo_secure_directory "$LOG_DIR" "q log directory"
command -v timeout >/dev/null 2>&1 || {
  demo_error "timeout is required to inspect and stop the q demo"
  exit 1
}

if [[ ! -e "$PID_FILE" && ! -L "$PID_FILE" ]]; then
  demo_clear_pid_state "$PID_FILE" "$START_FILE"
  echo "No AsyncQ demo q PID file found"
  exit 0
fi

if ! PID="$(demo_read_pid_file "$PID_FILE" 2>/dev/null)"; then
  if ! demo_clear_pid_state "$PID_FILE" "$START_FILE"; then
    demo_error "refusing to alter invalid AsyncQ demo q PID state because it is linked or unsafe"
    exit 1
  fi
  echo "Removed invalid AsyncQ demo q PID state without signaling a process" >&2
  exit 0
fi

if ! kill -0 "$PID" 2>/dev/null; then
  echo "AsyncQ demo q process with PID $PID is not running"
  demo_clear_pid_state "$PID_FILE" "$START_FILE" || exit 1
  exit 0
fi

if ! EXPECTED_START="$(demo_read_start_file "$START_FILE" 2>/dev/null)"; then
  demo_error "refusing to signal live PID $PID without a safe, valid recorded start time"
  exit 1
fi
if ! demo_q_demo_process_matches "$PID" "$ROOT_DIR" "$RUNNER" "$Q_SCRIPT" ||
  ! demo_process_group_is_safe "$PID" ||
  ! demo_process_instance_matches "$PID" "$EXPECTED_START"; then
  demo_error "refusing to signal PID $PID because it does not match the recorded q demo process"
  exit 1
fi

if demo_stop_process_group \
  "$PID" \
  "$EXPECTED_START" \
  "" \
  demo_q_demo_process_matches \
  "$ROOT_DIR" \
  "$RUNNER" \
  "$Q_SCRIPT"; then
  echo "Stopped AsyncQ demo q process with PID $PID"
  demo_clear_pid_state "$PID_FILE" "$START_FILE" || exit 1
  exit 0
fi

demo_error "q demo process group $PID did not stop after TERM and KILL"
exit 1
