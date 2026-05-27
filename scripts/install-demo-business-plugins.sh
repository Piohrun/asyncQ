#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${GRAFANA_VERSION:-13.0.1}"
RUNTIME_DIR="$ROOT_DIR/demo/runtime"
GRAFANA_HOME="$RUNTIME_DIR/grafana-$VERSION"
GRAFANA_BIN="$GRAFANA_HOME/bin/grafana"
PLUGIN_ROOT="$RUNTIME_DIR/plugins"

if [[ ! -x "$GRAFANA_BIN" ]]; then
  echo "Grafana binary not found at $GRAFANA_BIN" >&2
  exit 1
fi

mkdir -p "$PLUGIN_ROOT"

install_plugin() {
  local id="$1"
  local version="$2"
  local plugin_json="$PLUGIN_ROOT/$id/plugin.json"

  if [[ -f "$plugin_json" ]] && grep -q "\"version\"[[:space:]]*:[[:space:]]*\"$version\"" "$plugin_json"; then
    echo "Business plugin $id $version is already installed"
    return
  fi

  echo "Installing business plugin $id $version"
  "$GRAFANA_BIN" cli \
    --homepath "$GRAFANA_HOME" \
    --pluginsDir "$PLUGIN_ROOT" \
    plugins install "$id" "$version"
}

install_plugin volkovlabs-table-panel 3.6.5
install_plugin volkovlabs-echarts-panel 7.2.5
install_plugin volkovlabs-form-panel 6.3.4
