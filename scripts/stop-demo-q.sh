#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PID_FILE="$ROOT_DIR/demo/logs/q.pid"

if [[ ! -f "$PID_FILE" ]]; then
  echo "No AsyncQ demo q PID file found"
  exit 0
fi

PID="$(cat "$PID_FILE")"
if kill -0 "$PID" 2>/dev/null; then
  kill -- "-$PID" 2>/dev/null || kill "$PID"
  echo "Stopped AsyncQ demo q process with PID $PID"
else
  echo "AsyncQ demo q process with PID $PID is not running"
fi

rm -f "$PID_FILE"
