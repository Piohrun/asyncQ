#!/bin/bash
set -euo pipefail
umask 077

SCRIPT_SOURCE="${BASH_SOURCE[0]}"
SCRIPT_PARENT="${SCRIPT_SOURCE%/*}"
[[ "$SCRIPT_PARENT" != "$SCRIPT_SOURCE" ]] || SCRIPT_PARENT=.
SCRIPT_DIR="$(
  builtin cd -- "$SCRIPT_PARENT" &&
    builtin pwd -P
)" || {
  printf 'Error: could not resolve the script directory\n' >&2
  exit 1
}

# shellcheck source=scripts/demo-launch-common.sh
source "$SCRIPT_DIR/demo-launch-common.sh"
demo_validate_safe_path "$PATH"

if (($# != 4)); then
  echo "Usage: run-demo-q-session.sh Q_BINARY Q_SCRIPT PORT STATE_DIR" >&2
  exit 2
fi

Q_BINARY="$1"
Q_SCRIPT="$2"
PORT="$3"
STATE_DIR="$4"
SESSION_DIR=""
STDIN_FIFO=""
Q_PID=""
Q_START=""
QINIT_FILE=""
Q_CHILD_HOME=""
Q_CHILD_TMP=""
QHOME_VALUE=""
QLIC_VALUE=""
Q_ENV=()

# shellcheck disable=SC2329 # Invoked by cleanup, which is invoked by the EXIT trap.
q_child_is_running() {
  [[ -n "$Q_PID" && -n "$Q_START" ]] || return 1
  demo_process_instance_is_running "$Q_PID" "$Q_START" "$$"
}

# shellcheck disable=SC2329 # Invoked by the EXIT trap.
cleanup() {
  local status=$?
  local attempt

  set +e
  if [[ -n "$Q_PID" ]]; then
    if [[ -n "$Q_START" ]] &&
      demo_process_instance_matches "$Q_PID" "$Q_START" "$$"; then
      if q_child_is_running; then
        kill -TERM "$Q_PID" 2>/dev/null || true
        for ((attempt = 0; attempt < 20; attempt++)); do
          q_child_is_running || break
          sleep 0.1
        done
        if q_child_is_running; then
          kill -KILL "$Q_PID" 2>/dev/null || true
        fi
      fi
      if demo_process_instance_matches "$Q_PID" "$Q_START" "$$"; then
        wait "$Q_PID" 2>/dev/null || true
      fi
    else
      # $! is an unreaped child of this shell, so it cannot be PID-reused before wait.
      demo_terminate_owned_child "$Q_PID" 0 || true
    fi
  fi
  if [[ -n "${STDIN_FD:-}" ]]; then
    exec {STDIN_FD}>&-
  fi
  if [[ -n "$SESSION_DIR" ]] &&
    demo_validate_private_temp_directory \
      "$STATE_DIR" \
      "$SESSION_DIR" \
      "q session directory"; then
    if [[ -n "$STDIN_FIFO" && "$STDIN_FIFO" == "$SESSION_DIR/stdin" ]] &&
      [[ -p "$STDIN_FIFO" && ! -L "$STDIN_FIFO" ]]; then
      rm -- "$STDIN_FIFO" || true
    fi
    if [[ -n "$QINIT_FILE" && "$QINIT_FILE" == "$SESSION_DIR/qinit.q" ]] &&
      demo_regular_file_link_count_one "$QINIT_FILE"; then
      rm -- "$QINIT_FILE" || true
    fi
    if [[ -n "$Q_CHILD_HOME" && "$Q_CHILD_HOME" == "$SESSION_DIR/home" ]] &&
      [[ -d "$Q_CHILD_HOME" && ! -L "$Q_CHILD_HOME" ]]; then
      rmdir -- "$Q_CHILD_HOME" || true
    fi
    if [[ -n "$Q_CHILD_TMP" && "$Q_CHILD_TMP" == "$SESSION_DIR/tmp" ]] &&
      [[ -d "$Q_CHILD_TMP" && ! -L "$Q_CHILD_TMP" ]]; then
      rmdir -- "$Q_CHILD_TMP" || true
    fi
    demo_safe_remove_q_session_directory "$STATE_DIR" "$SESSION_DIR" || true
  fi
  trap - EXIT INT TERM
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ ! "$PORT" =~ ^[1-9][0-9]{0,4}$ ]] || ((10#${PORT} > 65535)); then
  echo "Invalid q demo port" >&2
  exit 2
fi
[[ "$Q_BINARY" == /* && -x "$Q_BINARY" &&
  "$Q_SCRIPT" == /* && -f "$Q_SCRIPT" && ! -L "$Q_SCRIPT" &&
  "$STATE_DIR" == /* && -d "$STATE_DIR" && ! -L "$STATE_DIR" ]] || {
  echo "Invalid q demo session arguments" >&2
  exit 2
}
SESSION_DIR="$(mktemp -d "$STATE_DIR/.q-session.XXXXXX")"
STDIN_FIFO="$SESSION_DIR/stdin"
QINIT_FILE="$SESSION_DIR/qinit.q"
Q_CHILD_HOME="$SESSION_DIR/home"
Q_CHILD_TMP="$SESSION_DIR/tmp"
mkdir -- "$Q_CHILD_HOME" "$Q_CHILD_TMP"
chmod 700 -- "$Q_CHILD_HOME" "$Q_CHILD_TMP"
: >"$QINIT_FILE"
chmod 600 -- "$QINIT_FILE"
demo_regular_file_link_count_one "$QINIT_FILE" || {
  echo "Could not create a trusted q initialization file" >&2
  exit 1
}
mkfifo -m 600 "$STDIN_FIFO"

# An open read/write FIFO keeps q's stdin live without a shell command string or
# a tail process that could mask q's immediate exit.
exec {STDIN_FD}<>"$STDIN_FIFO"
rm -f -- "$STDIN_FIFO"
STDIN_FIFO=""

Q_ENV=(
  env -i
  "PATH=$PATH"
  "HOME=$Q_CHILD_HOME"
  "TMPDIR=$Q_CHILD_TMP"
  LANG=C
  LC_ALL=C
  "QINIT=$QINIT_FILE"
)
if [[ -v QHOME ]]; then
  QHOME_VALUE="$(demo_canonical_environment_directory QHOME "$QHOME")"
  Q_ENV+=("QHOME=$QHOME_VALUE")
fi
if [[ -v QLIC ]]; then
  QLIC_VALUE="$(demo_canonical_environment_directory QLIC "$QLIC")"
  Q_ENV+=("QLIC=$QLIC_VALUE")
fi

"${Q_ENV[@]}" "$Q_BINARY" "$Q_SCRIPT" \
  -p "127.0.0.1:$PORT" \
  -T 30 \
  -w 1024 \
  -u 1 \
  <&"$STDIN_FD" &
Q_PID="$!"
if ! Q_START="$(demo_pid_start_time "$Q_PID")"; then
  echo "q child exited before its process identity could be captured" >&2
  exit 1
fi
if ! demo_process_instance_matches "$Q_PID" "$Q_START" "$$"; then
  echo "q child process identity changed before wait" >&2
  exit 1
fi
set +e
wait "$Q_PID"
STATUS=$?
set -e
Q_PID=""
Q_START=""
exit "$STATUS"
