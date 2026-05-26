#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${GRAFANA_VERSION:-13.0.1}"
PORT="${GRAFANA_PORT:-3000}"
RUNTIME_DIR="$ROOT_DIR/demo/runtime"
INSTALL_DIR="$RUNTIME_DIR/grafana-$VERSION"
PLUGIN_ROOT="$RUNTIME_DIR/plugins"
PROVISIONING_DIR="$RUNTIME_DIR/provisioning"
LOG_DIR="$RUNTIME_DIR/logs"
PID_FILE="$RUNTIME_DIR/grafana.pid"
LOG_FILE="$LOG_DIR/grafana.log"

case "$(uname -m)" in
  x86_64 | amd64)
    GRAFANA_ARCH="amd64"
    ;;
  aarch64 | arm64)
    GRAFANA_ARCH="arm64"
    ;;
  armv7l | armv7)
    GRAFANA_ARCH="armv7"
    ;;
  *)
    echo "Unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

if [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
  echo "Grafana demo is already running with PID $(cat "$PID_FILE")"
  echo "URL: http://localhost:$PORT/d/asyncq-kdb-demo/asyncq-kdb-demo"
  exit 0
fi

"$ROOT_DIR/scripts/build-demo-plugin.sh"

mkdir -p \
  "$RUNTIME_DIR" \
  "$PLUGIN_ROOT" \
  "$PROVISIONING_DIR/alerting" \
  "$PROVISIONING_DIR/dashboards" \
  "$PROVISIONING_DIR/datasources" \
  "$PROVISIONING_DIR/plugins" \
  "$LOG_DIR"

if [[ ! -x "$INSTALL_DIR/bin/grafana" ]]; then
  URL="https://dl.grafana.com/oss/release/grafana-$VERSION.linux-$GRAFANA_ARCH.tar.gz"
  TARBALL="$RUNTIME_DIR/grafana-$VERSION.linux-$GRAFANA_ARCH.tar.gz"
  EXTRACT_DIR="$RUNTIME_DIR/extract-grafana-$VERSION"

  echo "Downloading Grafana $VERSION for linux-$GRAFANA_ARCH"
  curl -fL "$URL" -o "$TARBALL"
  rm -rf "$EXTRACT_DIR" "$INSTALL_DIR"
  mkdir -p "$EXTRACT_DIR"
  tar -xzf "$TARBALL" -C "$EXTRACT_DIR"
  EXTRACTED="$(find "$EXTRACT_DIR" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
  mv "$EXTRACTED" "$INSTALL_DIR"
  rm -rf "$EXTRACT_DIR"
fi

ln -sfn "$ROOT_DIR/dist" "$PLUGIN_ROOT/asyncq-kdbbackend-datasource"
ln -sfn "$ROOT_DIR/dist-panel/asyncq-masterdata-panel" "$PLUGIN_ROOT/asyncq-masterdata-panel"
ln -sfn "$ROOT_DIR/dist-panel/asyncq-excel-report-panel" "$PLUGIN_ROOT/asyncq-excel-report-panel"

sed 's/host\.docker\.internal/localhost/g' \
  "$ROOT_DIR/demo/grafana/provisioning/datasources/asyncq.yml" \
  > "$PROVISIONING_DIR/datasources/asyncq.yml"

cat > "$PROVISIONING_DIR/dashboards/dashboards.yml" <<EOF
apiVersion: 1

providers:
  - name: AsyncQ Demo
    orgId: 1
    folder: AsyncQ
    type: file
    disableDeletion: false
    allowUiUpdates: true
    updateIntervalSeconds: 5
    options:
      path: $ROOT_DIR/demo/grafana/provisioning/dashboards/json
EOF

GF_AUTH_ANONYMOUS_ENABLED=true \
GF_AUTH_ANONYMOUS_ORG_ROLE=Admin \
GF_DATABASE_TYPE=sqlite3 \
GF_LOG_LEVEL=info \
ASYNCQ_DEMO_TEMPLATE_DIR="$ROOT_DIR/demo/templates" \
GF_PATHS_DATA="$RUNTIME_DIR/data" \
GF_PATHS_LOGS="$LOG_DIR" \
GF_PATHS_PLUGINS="$PLUGIN_ROOT" \
GF_PATHS_PROVISIONING="$PROVISIONING_DIR" \
GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=asyncq-kdbbackend-datasource,asyncq-masterdata-panel,asyncq-excel-report-panel \
GF_SECURITY_ADMIN_PASSWORD=admin \
GF_SECURITY_ADMIN_USER=admin \
GF_SERVER_HTTP_PORT="$PORT" \
GF_SERVER_ROOT_URL="http://localhost:$PORT" \
GF_USERS_DEFAULT_THEME=light \
setsid "$INSTALL_DIR/bin/grafana" server --homepath "$INSTALL_DIR" >"$LOG_FILE" 2>&1 &

PID="$!"
echo "$PID" > "$PID_FILE"

echo "Started Grafana demo locally with PID $PID"
echo "URL: http://localhost:$PORT/d/asyncq-kdb-demo/asyncq-kdb-demo"
echo "Compatibility matrix: http://localhost:$PORT/d/asyncq-compat-matrix/asyncq-panopticon-compatibility-matrix"
echo "Panopticon tests: http://localhost:$PORT/d/asyncq-pano-compat/asyncq-panopticon-compatibility-tests"
echo "Async tests: http://localhost:$PORT/d/asyncq-async-tests/asyncq-async-execution-tests"
echo "Master data/cache controls: http://localhost:$PORT/d/asyncq-masterdata-cache/asyncq-master-data-and-cache-controls"
echo "Excel reporting: http://localhost:$PORT/d/asyncq-excel-report/asyncq-excel-reporting"
echo "Login: admin / admin, or use anonymous admin access."
echo "Log: $LOG_FILE"
