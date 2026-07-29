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

Q_LOG_DIR="$ROOT_DIR/demo/logs"
Q_PID_FILE="$Q_LOG_DIR/q.pid"
Q_START_FILE="$Q_PID_FILE.start"
Q_RUNNER="$ROOT_DIR/scripts/run-demo-q-session.sh"
Q_SCRIPT="$ROOT_DIR/demo/q/asyncq_demo.q"
STATUS_FILE=""
Q_STATUS=""
Q_STATUS_PID=""
Q_ROLLBACK_REQUIRED=0
GRAFANA_LAUNCH_PID=""
GRAFANA_LAUNCH_START=""
GRAFANA_LAUNCHER="$ROOT_DIR/scripts/start-demo-grafana-local.sh"
ORCHESTRATOR_HOME="$Q_LOG_DIR/.local-launch-home"
ORCHESTRATOR_TMP="$Q_LOG_DIR/.local-launch-tmp"
GRAFANA_LAUNCH_ENV=()

load_q_status() {
  local status_text
  local parsed_status
  local parsed_pid

  [[ -n "$STATUS_FILE" ]] || return 1
  demo_validate_file_size_cap "$STATUS_FILE" 128 "q launcher orchestration status" || return 1
  status_text="$(<"$STATUS_FILE")"
  [[ "$status_text" =~ ^(existing|started)\ ([1-9][0-9]{0,9})$ ]] || return 1
  parsed_status="${BASH_REMATCH[1]}"
  parsed_pid="${BASH_REMATCH[2]}"
  demo_valid_signal_pid "$parsed_pid" || return 1
  Q_STATUS="$parsed_status"
  Q_STATUS_PID="$parsed_pid"
}

# shellcheck disable=SC2329 # Invoked by the EXIT trap.
cleanup() {
  local status=$?
  local current_q_pid=""

  trap - EXIT INT TERM
  if [[ -n "$GRAFANA_LAUNCH_PID" ]]; then
    if [[ -n "$GRAFANA_LAUNCH_START" ]] &&
      demo_process_instance_matches \
        "$GRAFANA_LAUNCH_PID" "$GRAFANA_LAUNCH_START" "$$" &&
      demo_bash_script_process_matches \
        "$GRAFANA_LAUNCH_PID" "$GRAFANA_LAUNCHER"; then
      if ! demo_stop_process_group \
        "$GRAFANA_LAUNCH_PID" \
        "$GRAFANA_LAUNCH_START" \
        "$$" \
        demo_bash_script_process_matches \
        "$GRAFANA_LAUNCHER"; then
        demo_terminate_owned_child "$GRAFANA_LAUNCH_PID" 1 || true
      else
        wait "$GRAFANA_LAUNCH_PID" 2>/dev/null || true
      fi
    else
      # $! remains this shell's exact unreaped child if identity capture failed.
      demo_terminate_owned_child "$GRAFANA_LAUNCH_PID" 1 || true
    fi
    GRAFANA_LAUNCH_PID=""
    GRAFANA_LAUNCH_START=""
  fi
  if ((status != 0 && Q_ROLLBACK_REQUIRED == 0)) &&
    [[ -n "$STATUS_FILE" ]] &&
    load_q_status &&
    [[ "$Q_STATUS" == "started" ]]; then
    Q_ROLLBACK_REQUIRED=1
  fi
  if ((status != 0 && Q_ROLLBACK_REQUIRED == 1)); then
    current_q_pid="$(
      demo_verified_q_runner_pid \
        "$Q_PID_FILE" \
        "$Q_START_FILE" \
        "$ROOT_DIR" \
        "$Q_RUNNER" \
        "$Q_SCRIPT" 2>/dev/null || true
    )"
    if demo_should_rollback_reported_process started "$Q_STATUS_PID" "$current_q_pid"; then
      echo "Demo startup did not complete; rolling back q process $Q_STATUS_PID" >&2
      "$ROOT_DIR/scripts/stop-demo-q.sh" || {
        demo_error "could not roll back q process $Q_STATUS_PID"
      }
    else
      echo "Demo startup did not complete; q process $Q_STATUS_PID no longer matches and was not signaled" >&2
    fi
  fi
  if [[ -n "$STATUS_FILE" &&
    "${STATUS_FILE%/*}" == "$Q_LOG_DIR" &&
    "${STATUS_FILE##*/}" =~ ^\.start-local-status\.[A-Za-z0-9]{6}$ ]] &&
    demo_regular_file_link_count_one "$STATUS_FILE"; then
    rm -- "$STATUS_FILE" || true
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

demo_require_real_directory "$ROOT_DIR/demo" "demo directory"
[[ -f "$GRAFANA_LAUNCHER" && ! -L "$GRAFANA_LAUNCHER" ]] || {
  demo_error "Grafana launcher is not a regular file: $GRAFANA_LAUNCHER"
  exit 1
}
for required_command in setsid timeout; do
  command -v "$required_command" >/dev/null 2>&1 || {
    demo_error "$required_command is required for local demo orchestration"
    exit 1
  }
done
demo_ensure_child_directory "$ROOT_DIR/demo" "$Q_LOG_DIR" "q log directory"
demo_secure_directory "$Q_LOG_DIR" "q log directory"
demo_ensure_child_directory "$Q_LOG_DIR" "$ORCHESTRATOR_HOME" "local launcher HOME"
demo_ensure_child_directory "$Q_LOG_DIR" "$ORCHESTRATOR_TMP" "local launcher temporary directory"
demo_prepare_clean_environment \
  GRAFANA_LAUNCH_ENV \
  "$ORCHESTRATOR_HOME" \
  "$ORCHESTRATOR_TMP"
for input_name in \
  GRAFANA_VERSION \
  GRAFANA_ARCH \
  GRAFANA_SHA256 \
  GRAFANA_PORT \
  ASYNCQ_DEMO_Q_PORT \
  ASYNCQ_DEMO_INSTALL_BUSINESS_PLUGINS; do
  if [[ -v "$input_name" ]]; then
    GRAFANA_LAUNCH_ENV+=("$input_name=${!input_name}")
  fi
done
STATUS_FILE="$(mktemp "$Q_LOG_DIR/.start-local-status.XXXXXX")"

if ! "$ROOT_DIR/scripts/start-demo-q.sh" --report-status-fd=3 3>"$STATUS_FILE"; then
  demo_error "q demo startup failed"
  exit 1
fi

if ! load_q_status; then
  demo_error "q demo launcher returned invalid orchestration status"
  exit 1
fi
if [[ "$Q_STATUS" == "started" ]]; then
  Q_ROLLBACK_REQUIRED=1
fi

# shellcheck disable=SC2016 # The child Bash expands its own PID and positional argument.
"${GRAFANA_LAUNCH_ENV[@]}" setsid bash -c \
  'kill -STOP "$$"; exec bash "$1"' \
  asyncq-grafana-launcher \
  "$GRAFANA_LAUNCHER" &
GRAFANA_LAUNCH_PID="$!"
if ! GRAFANA_LAUNCH_START="$(demo_pid_start_time "$GRAFANA_LAUNCH_PID")"; then
  demo_error "Grafana launcher exited before its process identity could be captured"
  exit 1
fi
GRAFANA_GROUP_VERIFIED=0
for ((attempt = 0; attempt < 100; attempt++)); do
  if demo_process_instance_matches \
    "$GRAFANA_LAUNCH_PID" "$GRAFANA_LAUNCH_START" "$$" &&
    demo_process_group_is_safe "$GRAFANA_LAUNCH_PID" &&
    [[ -r "/proc/$GRAFANA_LAUNCH_PID/stat" ]]; then
    GRAFANA_LAUNCH_STAT="$(<"/proc/$GRAFANA_LAUNCH_PID/stat")"
    GRAFANA_LAUNCH_STATE="$(
      demo_proc_stat_state "$GRAFANA_LAUNCH_STAT" 2>/dev/null || true
    )"
    if [[ "$GRAFANA_LAUNCH_STATE" == "T" ||
      "$GRAFANA_LAUNCH_STATE" == "t" ]]; then
      GRAFANA_GROUP_VERIFIED=1
      break
    fi
  fi
  sleep 0.01
done
if ((GRAFANA_GROUP_VERIFIED == 0)); then
  demo_error "Grafana launcher process identity could not be verified"
  exit 1
fi
kill -CONT "$GRAFANA_LAUNCH_PID"
GRAFANA_SCRIPT_VERIFIED=0
for ((attempt = 0; attempt < 100; attempt++)); do
  if demo_process_instance_matches \
    "$GRAFANA_LAUNCH_PID" "$GRAFANA_LAUNCH_START" "$$" &&
    demo_bash_script_process_matches \
      "$GRAFANA_LAUNCH_PID" "$GRAFANA_LAUNCHER"; then
    GRAFANA_SCRIPT_VERIFIED=1
    break
  fi
  demo_process_instance_is_running \
    "$GRAFANA_LAUNCH_PID" "$GRAFANA_LAUNCH_START" "$$" || break
  sleep 0.01
done
if ((GRAFANA_SCRIPT_VERIFIED == 0)); then
  demo_error "Grafana launcher script identity could not be verified"
  exit 1
fi
set +e
wait "$GRAFANA_LAUNCH_PID"
GRAFANA_STATUS=$?
set -e
if ((GRAFANA_STATUS == 0)); then
  Q_ROLLBACK_REQUIRED=0
  GRAFANA_LAUNCH_PID=""
  GRAFANA_LAUNCH_START=""
  exit 0
else
  exit "$GRAFANA_STATUS"
fi
