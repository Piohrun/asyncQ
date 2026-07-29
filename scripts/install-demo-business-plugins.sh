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
VERSION="${GRAFANA_VERSION:-$(demo_grafana_pinned_version)}"
RUNTIME_DIR="$ROOT_DIR/demo/runtime"
GRAFANA_HOME="$RUNTIME_DIR/grafana-$VERSION"
GRAFANA_BIN="$GRAFANA_HOME/bin/grafana"
INSTALL_STATE="$GRAFANA_HOME/.asyncq-demo-install-state"
PLUGIN_ROOT="$RUNTIME_DIR/plugins"
TARBALL=""
GRAFANA_CONFIG="$RUNTIME_DIR/grafana.ini"
CLI_HOME="$RUNTIME_DIR/.grafana-cli-home"
CLI_TMP="$RUNTIME_DIR/.grafana-cli-tmp"
CLI_ENV=()

demo_validate_release_token "$VERSION"
if [[ -n "${GRAFANA_ARCH+x}" ]]; then
  GRAFANA_ARCH_VALUE="$(demo_resolve_grafana_arch "$GRAFANA_ARCH")"
else
  GRAFANA_ARCH_VALUE="$(demo_resolve_grafana_arch)"
fi
IFS=$'\t' read -r EXPECTED_INSTALL_SHA _ < <(
  demo_resolve_grafana_artifact \
    "$VERSION" \
    "$GRAFANA_ARCH_VALUE" \
    "${GRAFANA_SHA256:-}"
)
[[ -n "$EXPECTED_INSTALL_SHA" ]] || exit 1
for required_command in timeout node env; do
  command -v "$required_command" >/dev/null 2>&1 || {
    demo_error "$required_command is required for verified Grafana plugin installation"
    exit 1
  }
done
TARBALL="$RUNTIME_DIR/grafana-$VERSION.linux-$GRAFANA_ARCH_VALUE.tar.gz"
demo_require_real_directory "$ROOT_DIR/demo" "demo directory"
demo_secure_directory "$RUNTIME_DIR" "Grafana runtime directory"
demo_secure_directory "$GRAFANA_HOME" "verified Grafana installation"
demo_secure_directory "$GRAFANA_HOME/bin" "verified Grafana binary directory"
demo_ensure_child_directory "$RUNTIME_DIR" "$CLI_HOME" "Grafana CLI HOME"
demo_ensure_child_directory "$RUNTIME_DIR" "$CLI_TMP" "Grafana CLI temporary directory"
demo_secure_directory "$CLI_HOME" "Grafana CLI HOME"
demo_secure_directory "$CLI_TMP" "Grafana CLI temporary directory"
demo_write_trusted_grafana_config "$RUNTIME_DIR" "$GRAFANA_CONFIG"
demo_prepare_clean_environment CLI_ENV "$CLI_HOME" "$CLI_TMP"

if ! demo_regular_file_link_count_one "$GRAFANA_BIN" || [[ ! -x "$GRAFANA_BIN" ]]; then
  demo_error "verified Grafana executable not found at $GRAFANA_BIN"
  exit 1
fi
if ! demo_regular_file_link_count_one "$INSTALL_STATE"; then
  demo_error "Grafana install state not found at $INSTALL_STATE"
  exit 1
fi
demo_validate_file_size_cap "$TARBALL" 536870912 "cached Grafana archive"
if ! demo_verify_sha256 "$TARBALL" "$EXPECTED_INSTALL_SHA"; then
  demo_error "cached Grafana archive checksum does not match the trusted artifact"
  exit 1
fi

PARENT_TREE_SHA="${ASYNCQ_DEMO_VERIFIED_INSTALL_TREE_SHA256:-}"
PARENT_PID="${ASYNCQ_DEMO_VERIFIED_INSTALL_PARENT_PID:-}"
PARENT_START="${ASYNCQ_DEMO_VERIFIED_INSTALL_PARENT_START:-}"
if [[ -n "$PARENT_TREE_SHA" || -n "$PARENT_PID" || -n "$PARENT_START" ]]; then
  if [[ -z "$PARENT_TREE_SHA" || -z "$PARENT_PID" || -z "$PARENT_START" ]] ||
    ! demo_validate_sha256 "$PARENT_TREE_SHA" >/dev/null 2>&1 ||
    [[ "$PPID" != "$PARENT_PID" ]] ||
    [[ "$(demo_observe_process_start_time "$PARENT_PID")" != "$PARENT_START" ]]; then
    demo_error "incomplete or invalid parent install verification"
    exit 1
  fi
  CURRENT_STATE_FINGERPRINT="$(
    demo_read_grafana_install_state "$GRAFANA_HOME" "$EXPECTED_INSTALL_SHA"
  )"
  if [[ "$CURRENT_STATE_FINGERPRINT" != "${PARENT_TREE_SHA,,}" ]]; then
    demo_error "Grafana install state changed after parent verification"
    exit 1
  fi
else
  if ! demo_validate_grafana_install_state \
    "$GRAFANA_HOME" \
    "$EXPECTED_INSTALL_SHA" >/dev/null; then
    demo_error "cached Grafana installation differs from its verified install state"
    exit 1
  fi
fi

if [[ -e "$PLUGIN_ROOT" || -L "$PLUGIN_ROOT" ]]; then
  demo_require_real_directory "$PLUGIN_ROOT" "Grafana plugin directory"
else
  demo_ensure_child_directory "$RUNTIME_DIR" "$PLUGIN_ROOT" "Grafana plugin directory"
fi
demo_secure_directory "$PLUGIN_ROOT" "Grafana plugin directory"

install_plugin() {
  local id="$1"
  local version="$2"
  local plugin_dir="$PLUGIN_ROOT/$id"
  local plugin_json="$plugin_dir/plugin.json"

  if [[ -e "$plugin_dir" || -L "$plugin_dir" ]]; then
    demo_secure_directory "$plugin_dir" "business plugin $id directory"
    if [[ -e "$plugin_json" || -L "$plugin_json" ]] &&
      [[ ! -f "$plugin_json" || -L "$plugin_json" ]]; then
      demo_error "business plugin $id manifest must be a regular file"
      return 1
    fi
  fi

  if demo_plugin_manifest_matches "$plugin_json" "$id" "$version"; then
    echo "Business plugin $id $version is already installed"
    return
  fi

  echo "Installing business plugin $id $version"
  "${CLI_ENV[@]}" timeout --foreground --kill-after=10s 5m \
    "$GRAFANA_BIN" cli \
    --homepath "$GRAFANA_HOME" \
    --config "$GRAFANA_CONFIG" \
    --pluginsDir "$PLUGIN_ROOT" \
    plugins install "$id" "$version"

  demo_secure_directory "$plugin_dir" "installed business plugin $id directory"
  if ! demo_plugin_manifest_matches "$plugin_json" "$id" "$version"; then
    demo_error "Grafana CLI did not install the expected $id $version manifest"
    return 1
  fi
}

# The Grafana CLI retains its default plugin-signature verification. These
# exact public plugin IDs and versions are intentionally pinned for the demo.
install_plugin volkovlabs-table-panel 3.6.5
install_plugin volkovlabs-echarts-panel 7.2.5
install_plugin volkovlabs-form-panel 6.3.4
