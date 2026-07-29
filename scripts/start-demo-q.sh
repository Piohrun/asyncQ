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

PORT="${ASYNCQ_DEMO_Q_PORT:-5000}"
LOG_DIR="$ROOT_DIR/demo/logs"
PID_FILE="$LOG_DIR/q.pid"
START_FILE="$PID_FILE.start"
LOG_FILE="$LOG_DIR/q.log"
RUNNER="$ROOT_DIR/scripts/run-demo-q-session.sh"
Q_SCRIPT="$ROOT_DIR/demo/q/asyncq_demo.q"
STARTED_PID=""
STARTED_START=""
REPORT_STATUS=0
Q_RUNNER_HOME="$LOG_DIR/.q-runner-home"
Q_RUNNER_TMP="$LOG_DIR/.q-runner-tmp"
QHOME_VALUE=""
QLIC_VALUE=""
Q_RUNNER_ENV=()

case "$#" in
  0)
    ;;
  1)
    if [[ "$1" == "--report-status-fd=3" ]]; then
      REPORT_STATUS=1
    else
      demo_error "unknown argument: $1"
      exit 2
    fi
    ;;
  *)
    demo_error "start-demo-q.sh accepts at most --report-status-fd=3"
    exit 2
    ;;
esac

report_status() {
  if ((REPORT_STATUS == 1)); then
    printf '%s %s\n' "$1" "$2" >&3
  fi
}

cleanup_failed_start() {
  local status=$?

  if [[ -n "$STARTED_PID" ]]; then
    if [[ -n "$STARTED_START" ]] &&
      demo_process_instance_matches "$STARTED_PID" "$STARTED_START" "$$" &&
      demo_q_runner_process_matches \
        "$STARTED_PID" "$ROOT_DIR" "$RUNNER" "$Q_SCRIPT" "$PORT" "$Q_BINARY"; then
      if ! demo_stop_process_group \
        "$STARTED_PID" \
        "$STARTED_START" \
        "$$" \
        demo_q_runner_process_matches \
        "$ROOT_DIR" \
        "$RUNNER" \
        "$Q_SCRIPT" \
        "$PORT" \
        "$Q_BINARY"; then
        demo_terminate_owned_child "$STARTED_PID" 1 || true
      else
        wait "$STARTED_PID" 2>/dev/null || true
      fi
    else
      # $! remains this shell's exact unreaped child even without /proc identity.
      demo_terminate_owned_child "$STARTED_PID" 1 || true
    fi
  fi
  trap - EXIT INT TERM
  exit "$status"
}
trap cleanup_failed_start EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

demo_validate_port "ASYNCQ_DEMO_Q_PORT" "$PORT"
command -v setsid >/dev/null 2>&1 || {
  demo_error "setsid is required to start the q demo safely"
  exit 1
}
command -v timeout >/dev/null 2>&1 || {
  demo_error "timeout is required to bound q demo process cleanup"
  exit 1
}
Q_BINARY="$(type -P q || true)"
if [[ -z "$Q_BINARY" || ! -x "$Q_BINARY" ]]; then
  demo_error "q executable was not found on PATH"
  exit 1
fi
[[ -f "$Q_SCRIPT" && ! -L "$Q_SCRIPT" ]] || {
  demo_error "q demo script is not a regular file: $Q_SCRIPT"
  exit 1
}
[[ -f "$RUNNER" && ! -L "$RUNNER" ]] || {
  demo_error "q session runner is not a regular file: $RUNNER"
  exit 1
}

demo_require_real_directory "$ROOT_DIR/demo" "demo directory"
demo_require_real_directory "$ROOT_DIR/demo/q" "q demo directory"
demo_ensure_child_directory "$ROOT_DIR/demo" "$LOG_DIR" "q log directory"
demo_secure_directory "$LOG_DIR" "q log directory"
demo_ensure_child_directory "$LOG_DIR" "$Q_RUNNER_HOME" "q runner HOME"
demo_ensure_child_directory "$LOG_DIR" "$Q_RUNNER_TMP" "q runner temporary directory"
demo_secure_directory "$Q_RUNNER_HOME" "q runner HOME"
demo_secure_directory "$Q_RUNNER_TMP" "q runner temporary directory"
demo_secure_regular_output "$LOG_DIR" "$LOG_FILE" "q log"

Q_RUNNER_ENV=(
  env -i
  "PATH=$PATH"
  "HOME=$Q_RUNNER_HOME"
  "TMPDIR=$Q_RUNNER_TMP"
  LANG=C
  LC_ALL=C
)
if [[ -v QHOME ]]; then
  QHOME_VALUE="$(demo_canonical_environment_directory QHOME "$QHOME")"
  Q_RUNNER_ENV+=("QHOME=$QHOME_VALUE")
fi
if [[ -v QLIC ]]; then
  QLIC_VALUE="$(demo_canonical_environment_directory QLIC "$QLIC")"
  Q_RUNNER_ENV+=("QLIC=$QLIC_VALUE")
fi

if PID="$(demo_read_pid_file "$PID_FILE" 2>/dev/null)"; then
  if kill -0 "$PID" 2>/dev/null; then
    if demo_q_demo_process_matches "$PID" "$ROOT_DIR" "$RUNNER" "$Q_SCRIPT" &&
      demo_process_group_is_safe "$PID" &&
      { [[ ! -e "$START_FILE" && ! -L "$START_FILE" ]] || demo_pid_state_matches "$PID" "$START_FILE"; }; then
      RUNNING_PORT="$(demo_q_runner_port "$PID")"
      if [[ "$RUNNING_PORT" != "$PORT" ]]; then
        demo_error "verified q demo PID $PID is running on port $RUNNING_PORT; stop it before changing ASYNCQ_DEMO_Q_PORT"
        exit 1
      fi
      if [[ ! -e "$START_FILE" ]]; then
        demo_write_pid_state "$PID_FILE" "$START_FILE" "$PID"
      fi
      chmod 600 -- "$PID_FILE" "$START_FILE"
      echo "AsyncQ demo q process is already running with PID $PID"
      echo "Log: $LOG_FILE"
      report_status existing "$PID"
      exit 0
    fi
    echo "Ignoring q PID file because PID $PID does not identify this demo process" >&2
  else
    echo "Clearing stale q PID file for PID $PID"
  fi
  demo_clear_pid_state "$PID_FILE" "$START_FILE"
elif [[ -e "$PID_FILE" || -L "$PID_FILE" || -e "$START_FILE" || -L "$START_FILE" ]]; then
  echo "Clearing invalid q PID state" >&2
  demo_clear_pid_state "$PID_FILE" "$START_FILE"
fi

cd "$ROOT_DIR"
"${Q_RUNNER_ENV[@]}" \
  setsid bash "$RUNNER" "$Q_BINARY" "$Q_SCRIPT" "$PORT" "$LOG_DIR" \
  >"$LOG_FILE" 2>&1 &
STARTED_PID="$!"
if ! STARTED_START="$(demo_pid_start_time "$STARTED_PID")"; then
  demo_error "q demo exited before its process identity could be captured; see $LOG_FILE"
  exit 1
fi
READY=0
READINESS_DEADLINE=$((SECONDS + 15))
while ((SECONDS < READINESS_DEADLINE)); do
  if ! demo_process_instance_is_running "$STARTED_PID" "$STARTED_START" "$$"; then
    if demo_process_instance_matches "$STARTED_PID" "$STARTED_START" "$$"; then
      wait "$STARTED_PID" || true
    fi
    demo_error "q demo exited before opening port $PORT; see $LOG_FILE"
    exit 1
  fi
  if Q_CHILD_PID="$(demo_q_runner_child_pid "$STARTED_PID" "$Q_SCRIPT" "$PORT" 2>/dev/null)" &&
    demo_process_owns_ipv4_loopback_listener "$Q_CHILD_PID" "$PORT"; then
    READY=1
    break
  fi
  sleep 0.1
done
if ((READY == 0)); then
  demo_error "q demo did not open port $PORT within 15 seconds; see $LOG_FILE"
  exit 1
fi
if ! demo_process_instance_matches "$STARTED_PID" "$STARTED_START" "$$" ||
  ! demo_q_demo_process_matches \
    "$STARTED_PID" "$ROOT_DIR" "$RUNNER" "$Q_SCRIPT" "$PORT" "$Q_BINARY" ||
  ! demo_process_group_is_safe "$STARTED_PID"; then
  demo_error "q demo process identity could not be verified after launch"
  exit 1
fi

demo_write_pid_state "$PID_FILE" "$START_FILE" "$STARTED_PID"
PID="$STARTED_PID"
report_status started "$PID"

echo "Started AsyncQ demo q process on port $PORT with PID $PID"
echo "q is bound to IPv4 loopback at 127.0.0.1:$PORT."
echo "Use this permissive query-evaluating demo only on a trusted local development machine."
echo "Log: $LOG_FILE"
STARTED_PID=""
STARTED_START=""
