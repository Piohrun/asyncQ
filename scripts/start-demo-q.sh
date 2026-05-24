#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${ASYNCQ_DEMO_Q_PORT:-5000}"
LOG_DIR="$ROOT_DIR/demo/logs"
PID_FILE="$LOG_DIR/q.pid"
LOG_FILE="$LOG_DIR/q.log"

mkdir -p "$LOG_DIR"

if [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
  echo "AsyncQ demo q process is already running with PID $(cat "$PID_FILE")"
  echo "Log: $LOG_FILE"
  exit 0
fi

cd "$ROOT_DIR"
setsid bash -c "tail -f /dev/null | q demo/q/asyncq_demo.q -p '$PORT'" >"$LOG_FILE" 2>&1 &
PID="$!"
echo "$PID" >"$PID_FILE"

echo "Started AsyncQ demo q process on port $PORT with PID $PID"
echo "Log: $LOG_FILE"
