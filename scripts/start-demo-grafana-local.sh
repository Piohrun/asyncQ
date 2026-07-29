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

DEFAULT_VERSION="$(demo_grafana_pinned_version)"
VERSION="${GRAFANA_VERSION:-$DEFAULT_VERSION}"
PORT="${GRAFANA_PORT:-3000}"
Q_PORT="${ASYNCQ_DEMO_Q_PORT:-5000}"
RUNTIME_DIR="$ROOT_DIR/demo/runtime"
PLUGIN_ROOT="$RUNTIME_DIR/plugins"
PROVISIONING_DIR="$RUNTIME_DIR/provisioning"
LOG_DIR="$RUNTIME_DIR/logs"
PID_FILE="$RUNTIME_DIR/grafana.pid"
START_FILE="$PID_FILE.start"
LOG_FILE="$LOG_DIR/grafana.log"
WORK_DIR=""
DOWNLOAD_TMP=""
STARTED_PID=""
STARTED_START=""
BUILD_PID=""
BUILD_START=""
BUILD_STATUS_FILE=""
BUILD_HOME="$RUNTIME_DIR/.build-home"
BUILD_TMP="$RUNTIME_DIR/.build-tmp"
GRAFANA_PROCESS_HOME="$RUNTIME_DIR/.grafana-home"
GRAFANA_PROCESS_TMP="$RUNTIME_DIR/.grafana-tmp"
GRAFANA_CONFIG="$RUNTIME_DIR/grafana.ini"
BUILD_GOARCH=""
BUILD_ENV=()
GRAFANA_BASE_ENV=()

ensure_install_work_dir() {
  if [[ -z "$WORK_DIR" ]]; then
    WORK_DIR="$(mktemp -d "$RUNTIME_DIR/.grafana-install.XXXXXX")" || return 1
    DOWNLOAD_TMP="$WORK_DIR/grafana.tar.gz"
  fi
}

remove_build_status_file() {
  local path="$BUILD_STATUS_FILE"

  [[ -n "$path" ]] || return 0
  BUILD_STATUS_FILE=""
  if [[ "${path%/*}" != "$RUNTIME_DIR" ||
    ! "${path##*/}" =~ ^\.plugin-build-status\.[A-Za-z0-9]{6}$ ]] ||
    ! demo_regular_file_link_count_one "$path"; then
    demo_error "refusing to remove unsafe plugin-build status file: $path"
    return 1
  fi
  rm -- "$path"
}

stop_active_build() {
  local cleanup_status=0

  if [[ -n "$BUILD_PID" ]]; then
    if [[ -n "$BUILD_START" ]] &&
      demo_process_instance_matches "$BUILD_PID" "$BUILD_START" "$$"; then
      if demo_stop_process_group "$BUILD_PID" "$BUILD_START" "$$"; then
        wait "$BUILD_PID" 2>/dev/null || true
      else
        demo_terminate_owned_child "$BUILD_PID" 1 || true
        cleanup_status=1
      fi
    else
      # $! remains this shell's exact unreaped child even without /proc identity.
      demo_terminate_owned_child "$BUILD_PID" 1 || true
      cleanup_status=1
    fi
  fi
  BUILD_PID=""
  BUILD_START=""
  remove_build_status_file || cleanup_status=1
  return "$cleanup_status"
}

run_bounded_plugin_build() {
  local attempt
  local build_status=""
  local deadline
  local state
  local stat_line
  local completion_verified=0
  local verified=0

  BUILD_STATUS_FILE="$(mktemp "$RUNTIME_DIR/.plugin-build-status.XXXXXX")" || return 1
  chmod 600 -- "$BUILD_STATUS_FILE"

  # The verified session leader remains stopped after the command finishes so
  # resistant descendants can still be terminated as one owned process group.
  # shellcheck disable=SC2016 # The child Bash expands its own PID and arguments.
  "${BUILD_ENV[@]}" setsid bash -c '
    status_file="$1"
    shift
    kill -STOP "$$" || exit 125
    "$@"
    status=$?
    printf "%s\n" "$status" >"$status_file" || status=125
    kill -STOP "$$" || exit 125
    exit "$status"
  ' asyncq-plugin-build \
    "$BUILD_STATUS_FILE" \
    timeout --foreground --kill-after=10s 10m \
    bash "$ROOT_DIR/scripts/build-demo-plugin.sh" &
  BUILD_PID="$!"
  if ! BUILD_START="$(demo_pid_start_time "$BUILD_PID")"; then
    demo_error "plugin build supervisor exited before its identity could be captured"
    stop_active_build || true
    return 1
  fi
  for ((attempt = 0; attempt < 100; attempt++)); do
    if demo_process_instance_matches "$BUILD_PID" "$BUILD_START" "$$" &&
      demo_process_group_is_safe "$BUILD_PID" &&
      [[ -r "/proc/$BUILD_PID/stat" ]]; then
      stat_line="$(<"/proc/$BUILD_PID/stat")"
      state="$(demo_proc_stat_state "$stat_line" 2>/dev/null || true)"
      if [[ "$state" == "T" || "$state" == "t" ]]; then
        verified=1
        break
      fi
    fi
    sleep 0.01
  done
  if ((verified == 0)); then
    demo_error "plugin build supervisor identity could not be verified"
    stop_active_build || true
    return 1
  fi
  if ! kill -CONT "$BUILD_PID"; then
    demo_error "could not release the verified plugin build supervisor"
    stop_active_build || true
    return 1
  fi

  deadline=$((SECONDS + 620))
  while ((SECONDS < deadline)); do
    if ! demo_regular_file_link_count_one "$BUILD_STATUS_FILE"; then
      demo_error "plugin build status file became unsafe"
      stop_active_build || true
      return 1
    fi
    if [[ -s "$BUILD_STATUS_FILE" ]]; then
      build_status="$(<"$BUILD_STATUS_FILE")"
      if [[ ! "$build_status" =~ ^([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])$ ]] ||
        ! demo_process_instance_matches "$BUILD_PID" "$BUILD_START" "$$" ||
        [[ ! -r "/proc/$BUILD_PID/stat" ]]; then
        demo_error "plugin build supervisor returned invalid completion state"
        stop_active_build || true
        return 1
      fi
      stat_line="$(<"/proc/$BUILD_PID/stat")"
      state="$(demo_proc_stat_state "$stat_line" 2>/dev/null || true)"
      if [[ "$state" == "T" || "$state" == "t" ]]; then
        completion_verified=1
        break
      fi
    elif ! demo_process_instance_is_running "$BUILD_PID" "$BUILD_START" "$$"; then
      demo_error "plugin build supervisor exited without a valid result"
      stop_active_build || true
      return 1
    fi
    sleep 0.05
  done
  if ((completion_verified == 0)); then
    demo_error "plugin build supervisor exceeded its bounded completion window"
    stop_active_build || true
    return 1
  fi
  if ! stop_active_build; then
    demo_error "could not terminate every process in the completed plugin build group"
    return 1
  fi
  return "$build_status"
}

cleanup() {
  local status=$?

  stop_active_build || true
  if [[ -n "$STARTED_PID" ]]; then
    if [[ -n "$STARTED_START" ]] &&
      demo_process_instance_matches "$STARTED_PID" "$STARTED_START" "$$" &&
      demo_grafana_process_home "$STARTED_PID" "$ROOT_DIR" "$RUNTIME_DIR" >/dev/null; then
      if ! demo_stop_process_group \
        "$STARTED_PID" \
        "$STARTED_START" \
        "$$" \
        demo_grafana_process_home \
        "$ROOT_DIR" \
        "$RUNTIME_DIR"; then
        demo_terminate_owned_child "$STARTED_PID" 1 || true
      else
        wait "$STARTED_PID" 2>/dev/null || true
      fi
    else
      # $! remains this shell's exact unreaped child even without /proc identity.
      demo_terminate_owned_child "$STARTED_PID" 1 || true
    fi
  fi
  if [[ -n "$WORK_DIR" ]]; then
    demo_safe_remove_install_workdir "$RUNTIME_DIR" "$WORK_DIR" || true
  fi
  trap - EXIT INT TERM
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

demo_validate_release_token "$VERSION"
demo_validate_port "GRAFANA_PORT" "$PORT"
demo_validate_port "ASYNCQ_DEMO_Q_PORT" "$Q_PORT"
for required_command in curl timeout tar head wc env node setsid; do
  command -v "$required_command" >/dev/null 2>&1 || {
    demo_error "$required_command is required for the local Grafana demo"
    exit 1
  }
done

case "${ASYNCQ_DEMO_INSTALL_BUSINESS_PLUGINS:-1}" in
  0 | 1)
    ;;
  *)
    demo_error "ASYNCQ_DEMO_INSTALL_BUSINESS_PLUGINS must be 0 or 1"
    exit 1
    ;;
esac

if [[ -n "${GRAFANA_ARCH+x}" ]]; then
  GRAFANA_ARCH_VALUE="$(demo_resolve_grafana_arch "$GRAFANA_ARCH")"
else
  GRAFANA_ARCH_VALUE="$(demo_resolve_grafana_arch)"
fi

CALLER_SHA="${GRAFANA_SHA256:-}"
IFS=$'\t' read -r EXPECTED_SHA URL < <(
  demo_resolve_grafana_artifact "$VERSION" "$GRAFANA_ARCH_VALUE" "$CALLER_SHA"
)
[[ -n "$EXPECTED_SHA" && -n "$URL" ]] || exit 1

INSTALL_DIR="$RUNTIME_DIR/grafana-$VERSION"
EXPECTED_TOP="grafana-$VERSION"
TARBALL="$RUNTIME_DIR/grafana-$VERSION.linux-$GRAFANA_ARCH_VALUE.tar.gz"
INSTALL_STATE="$INSTALL_DIR/.asyncq-demo-install-state"

demo_require_real_directory "$ROOT_DIR/demo" "demo directory"
demo_ensure_child_directory "$ROOT_DIR/demo" "$RUNTIME_DIR" "Grafana runtime directory"
demo_ensure_child_directory "$RUNTIME_DIR" "$LOG_DIR" "Grafana log directory"
demo_ensure_child_directory "$RUNTIME_DIR" "$BUILD_HOME" "plugin build HOME"
demo_ensure_child_directory "$RUNTIME_DIR" "$BUILD_TMP" "plugin build temporary directory"
demo_ensure_child_directory "$RUNTIME_DIR" "$GRAFANA_PROCESS_HOME" "Grafana process HOME"
demo_ensure_child_directory "$RUNTIME_DIR" "$GRAFANA_PROCESS_TMP" "Grafana process temporary directory"
demo_secure_directory "$RUNTIME_DIR" "Grafana runtime directory"
demo_secure_directory "$LOG_DIR" "Grafana log directory"
demo_secure_directory "$BUILD_HOME" "plugin build HOME"
demo_secure_directory "$BUILD_TMP" "plugin build temporary directory"
demo_secure_directory "$GRAFANA_PROCESS_HOME" "Grafana process HOME"
demo_secure_directory "$GRAFANA_PROCESS_TMP" "Grafana process temporary directory"
demo_secure_regular_output "$LOG_DIR" "$LOG_FILE" "Grafana log"
demo_write_trusted_grafana_config "$RUNTIME_DIR" "$GRAFANA_CONFIG"
demo_prepare_clean_build_environment BUILD_ENV "$BUILD_HOME" "$BUILD_TMP"
demo_prepare_clean_environment \
  GRAFANA_BASE_ENV \
  "$GRAFANA_PROCESS_HOME" \
  "$GRAFANA_PROCESS_TMP"
demo_regular_file_link_count_one "$ROOT_DIR/scripts/build-demo-plugin.sh" || {
  demo_error "plugin build script is linked or unsafe"
  exit 1
}
[[ -r "$ROOT_DIR/scripts/build-demo-plugin.sh" ]] || {
  demo_error "plugin build script is not readable"
  exit 1
}
demo_prepare_build_output_roots "$ROOT_DIR"
BUILD_GOARCH="$(demo_resolve_build_goarch)"

if PID="$(demo_read_pid_file "$PID_FILE" 2>/dev/null)"; then
  if kill -0 "$PID" 2>/dev/null; then
    RUNNING_HOME="$(demo_grafana_process_home "$PID" "$ROOT_DIR" "$RUNTIME_DIR" 2>/dev/null || true)"
    if [[ -n "$RUNNING_HOME" ]] &&
      demo_process_group_is_safe "$PID" &&
      { [[ ! -e "$START_FILE" && ! -L "$START_FILE" ]] || demo_pid_state_matches "$PID" "$START_FILE"; }; then
      if [[ "$RUNNING_HOME" != "$INSTALL_DIR" ]]; then
        demo_error "verified Grafana PID $PID uses a different version; stop it before changing GRAFANA_VERSION"
        exit 1
      fi
      if ! demo_grafana_process_is_ready \
        "$PID" "$PORT" "$ROOT_DIR" "$RUNTIME_DIR" "$INSTALL_DIR" "$VERSION" "$LOG_DIR" "$Q_PORT"; then
        demo_error "verified Grafana PID $PID is not healthy on loopback or was provisioned for a different q port"
        exit 1
      fi
      if [[ ! -e "$START_FILE" ]]; then
        demo_write_pid_state "$PID_FILE" "$START_FILE" "$PID"
      fi
      chmod 600 -- "$PID_FILE" "$START_FILE"
      echo "Grafana demo is already running with PID $PID"
      echo "URL: http://127.0.0.1:$PORT/d/asyncq-kdb-demo/asyncq-kdb-demo"
      exit 0
    fi
    echo "Ignoring Grafana PID file because PID $PID does not identify this demo process" >&2
  else
    echo "Clearing stale Grafana PID file for PID $PID"
  fi
  demo_clear_pid_state "$PID_FILE" "$START_FILE"
elif [[ -e "$PID_FILE" || -L "$PID_FILE" || -e "$START_FILE" || -L "$START_FILE" ]]; then
  echo "Clearing invalid Grafana PID state" >&2
  demo_clear_pid_state "$PID_FILE" "$START_FILE"
fi

run_bounded_plugin_build
demo_publish_build_output_trees "$ROOT_DIR" "$BUILD_GOARCH"
demo_validate_build_output_publication "$ROOT_DIR" "$BUILD_GOARCH"

demo_ensure_child_directory "$RUNTIME_DIR" "$PLUGIN_ROOT" "Grafana plugin directory"
demo_ensure_child_directory "$RUNTIME_DIR" "$PROVISIONING_DIR" "Grafana provisioning directory"
demo_ensure_child_directory "$RUNTIME_DIR" "$RUNTIME_DIR/data" "Grafana data directory"
demo_ensure_child_directory "$PROVISIONING_DIR" "$PROVISIONING_DIR/alerting" "alerting provisioning directory"
demo_ensure_child_directory "$PROVISIONING_DIR" "$PROVISIONING_DIR/dashboards" "dashboard provisioning directory"
demo_ensure_child_directory "$PROVISIONING_DIR" "$PROVISIONING_DIR/datasources" "datasource provisioning directory"
demo_ensure_child_directory "$PROVISIONING_DIR" "$PROVISIONING_DIR/plugins" "plugin provisioning directory"
demo_secure_directory "$PLUGIN_ROOT" "Grafana plugin directory"
demo_secure_directory "$PROVISIONING_DIR" "Grafana provisioning directory"
demo_secure_directory "$RUNTIME_DIR/data" "Grafana data directory"
demo_secure_directory "$PROVISIONING_DIR/alerting" "alerting provisioning directory"
demo_secure_directory "$PROVISIONING_DIR/dashboards" "dashboard provisioning directory"
demo_secure_directory "$PROVISIONING_DIR/datasources" "datasource provisioning directory"
demo_secure_directory "$PROVISIONING_DIR/plugins" "plugin provisioning directory"

CACHED_INSTALL_VALID=0
VERIFIED_INSTALL_FINGERPRINT=""
if [[ -e "$INSTALL_DIR" || -L "$INSTALL_DIR" ]] &&
  ! demo_require_real_directory "$INSTALL_DIR" "cached Grafana installation"; then
  exit 1
fi
if [[ -e "$INSTALL_STATE" || -L "$INSTALL_STATE" ]] &&
  ! demo_regular_file_link_count_one "$INSTALL_STATE"; then
  demo_error "Grafana install state is linked or unsafe: $INSTALL_STATE"
  exit 1
fi

if [[ -e "$TARBALL" || -L "$TARBALL" ]]; then
  demo_validate_file_size_cap "$TARBALL" 536870912 "cached Grafana archive"
  echo "Verifying cached Grafana archive"
fi
if [[ ! -e "$TARBALL" && ! -L "$TARBALL" ]] ||
  ! demo_verify_sha256 "$TARBALL" "$EXPECTED_SHA"; then
  ensure_install_work_dir
  echo "Downloading Grafana $VERSION for linux-$GRAFANA_ARCH_VALUE"
  curl --disable \
    --fail \
    --location \
    --proto '=https' \
    --tlsv1.2 \
    --retry 3 \
    --retry-all-errors \
    --retry-delay 2 \
    --connect-timeout 10 \
    --max-time 300 \
    --max-filesize 536870912 \
    --output "$DOWNLOAD_TMP" \
    "$URL"
  demo_validate_file_size_cap "$DOWNLOAD_TMP" 536870912 "downloaded Grafana archive"
  if ! demo_verify_sha256 "$DOWNLOAD_TMP" "$EXPECTED_SHA"; then
    demo_error "downloaded Grafana archive checksum does not match GRAFANA_SHA256"
    exit 1
  fi
  mv -f -- "$DOWNLOAD_TMP" "$TARBALL"
fi

demo_validate_file_size_cap "$TARBALL" 536870912 "cached Grafana archive"
if ! demo_verify_sha256 "$TARBALL" "$EXPECTED_SHA"; then
  demo_error "cached Grafana archive checksum does not match the expected value"
  exit 1
fi
if VERIFIED_INSTALL_FINGERPRINT="$(
  demo_validate_grafana_install_state "$INSTALL_DIR" "$EXPECTED_SHA"
)"; then
  CACHED_INSTALL_VALID=1
fi

if ((CACHED_INSTALL_VALID == 0)); then
  ensure_install_work_dir
  CANDIDATE="$(
    demo_extract_verified_grafana_archive \
      "$TARBALL" \
      "$EXPECTED_TOP" \
      "$WORK_DIR"
  )"
  VERIFIED_INSTALL_FINGERPRINT="$(
    demo_write_grafana_install_state "$CANDIDATE" "$EXPECTED_SHA"
  )"

  if ! demo_replace_install_directory "$RUNTIME_DIR" "$INSTALL_DIR" "$CANDIDATE"; then
    exit 1
  fi
fi
if [[ -n "$WORK_DIR" ]]; then
  demo_safe_remove_install_workdir "$RUNTIME_DIR" "$WORK_DIR"
  WORK_DIR=""
fi

demo_secure_directory "$INSTALL_DIR" "verified Grafana installation"
demo_secure_directory "$INSTALL_DIR/bin" "verified Grafana binary directory"
demo_regular_file_link_count_one "$INSTALL_DIR/bin/grafana" || {
  demo_error "Grafana executable is linked or unsafe: $INSTALL_DIR/bin/grafana"
  exit 1
}
demo_regular_file_link_count_one "$INSTALL_STATE" || {
  demo_error "Grafana install state is linked or unsafe: $INSTALL_STATE"
  exit 1
}
chmod 600 -- "$INSTALL_STATE"
CURRENT_STATE_FINGERPRINT="$(
  demo_read_grafana_install_state "$INSTALL_DIR" "$EXPECTED_SHA"
)"
if [[ "$CURRENT_STATE_FINGERPRINT" != "$VERIFIED_INSTALL_FINGERPRINT" ]]; then
  demo_error "Grafana install state changed after verification"
  exit 1
fi

if [[ "${ASYNCQ_DEMO_INSTALL_BUSINESS_PLUGINS:-1}" != "0" ]]; then
  VERIFIED_PARENT_START="$(demo_observe_process_start_time "$$")" || {
    demo_error "could not record the verified install parent's process identity"
    exit 1
  }
  "${GRAFANA_BASE_ENV[@]}" \
    "GRAFANA_VERSION=$VERSION" \
    "GRAFANA_ARCH=$GRAFANA_ARCH_VALUE" \
    "GRAFANA_SHA256=$EXPECTED_SHA" \
    "ASYNCQ_DEMO_VERIFIED_INSTALL_TREE_SHA256=$VERIFIED_INSTALL_FINGERPRINT" \
    "ASYNCQ_DEMO_VERIFIED_INSTALL_PARENT_PID=$$" \
    "ASYNCQ_DEMO_VERIFIED_INSTALL_PARENT_START=$VERIFIED_PARENT_START" \
    bash "$ROOT_DIR/scripts/install-demo-business-plugins.sh"
fi

demo_validate_build_output_publication "$ROOT_DIR" "$BUILD_GOARCH"
demo_require_replaceable_symlink \
  "$PLUGIN_ROOT" \
  "$PLUGIN_ROOT/asyncq-kdbbackend-datasource" \
  "datasource plugin link"
demo_require_replaceable_symlink \
  "$PLUGIN_ROOT" \
  "$PLUGIN_ROOT/asyncq-masterdata-panel" \
  "master-data plugin link"
demo_require_replaceable_symlink \
  "$PLUGIN_ROOT" \
  "$PLUGIN_ROOT/asyncq-excel-report-panel" \
  "Excel-report plugin link"
ln -sfn -- "$ROOT_DIR/dist" "$PLUGIN_ROOT/asyncq-kdbbackend-datasource"
ln -sfn -- "$ROOT_DIR/dist-panel/asyncq-masterdata-panel" "$PLUGIN_ROOT/asyncq-masterdata-panel"
ln -sfn -- "$ROOT_DIR/dist-panel/asyncq-excel-report-panel" "$PLUGIN_ROOT/asyncq-excel-report-panel"

demo_secure_regular_output \
  "$PROVISIONING_DIR/datasources" \
  "$PROVISIONING_DIR/datasources/asyncq.yml" \
  "generated datasource provisioning"
DATASOURCE_OUTPUT="$PROVISIONING_DIR/datasources/asyncq.yml"
DATASOURCE_TMP="$(mktemp "$PROVISIONING_DIR/datasources/.asyncq-datasource.XXXXXX")"
if ! demo_render_loopback_datasource \
  "$ROOT_DIR/demo/grafana/provisioning/datasources/asyncq.yml" \
  "$DATASOURCE_TMP" \
  "$Q_PORT" ||
  ! chmod 600 -- "$DATASOURCE_TMP" ||
  ! mv -f -- "$DATASOURCE_TMP" "$DATASOURCE_OUTPUT"; then
  rm -f -- "$DATASOURCE_TMP"
  demo_error "could not atomically generate local datasource provisioning"
  exit 1
fi
demo_regular_file_link_count_one "$DATASOURCE_OUTPUT" || exit 1

demo_write_dashboard_provider_config \
  "$PROVISIONING_DIR/dashboards" \
  "$PROVISIONING_DIR/dashboards/dashboards.yml" \
  "$ROOT_DIR"

cd "$ROOT_DIR"
"${GRAFANA_BASE_ENV[@]}" \
  GF_AUTH_ANONYMOUS_ENABLED=true \
  GF_AUTH_ANONYMOUS_ORG_ROLE=Admin \
  GF_DATABASE_TYPE=sqlite3 \
  GF_LOG_LEVEL=info \
  "ASYNCQ_DEMO_Q_PORT=$Q_PORT" \
  "ASYNCQ_DEMO_TEMPLATE_DIR=$ROOT_DIR/demo/templates" \
  "GF_PATHS_DATA=$RUNTIME_DIR/data" \
  "GF_PATHS_LOGS=$LOG_DIR" \
  "GF_PATHS_PLUGINS=$PLUGIN_ROOT" \
  "GF_PATHS_PROVISIONING=$PROVISIONING_DIR" \
  GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=asyncq-kdbbackend-datasource,asyncq-masterdata-panel,asyncq-excel-report-panel \
  GF_SECURITY_ADMIN_PASSWORD=admin \
  GF_SECURITY_ADMIN_USER=admin \
  GF_SERVER_HTTP_ADDR=127.0.0.1 \
  "GF_SERVER_HTTP_PORT=$PORT" \
  "GF_SERVER_ROOT_URL=http://127.0.0.1:$PORT" \
  GF_USERS_DEFAULT_THEME=light \
  setsid "$INSTALL_DIR/bin/grafana" \
    server \
    --homepath "$INSTALL_DIR" \
    --config "$GRAFANA_CONFIG" \
    >"$LOG_FILE" 2>&1 &

STARTED_PID="$!"
if ! STARTED_START="$(demo_pid_start_time "$STARTED_PID")"; then
  demo_error "Grafana exited before its process identity could be captured; see $LOG_FILE"
  exit 1
fi
READY=0
READINESS_DEADLINE=$((SECONDS + 60))
while ((SECONDS < READINESS_DEADLINE)); do
  if ! demo_process_instance_is_running "$STARTED_PID" "$STARTED_START" "$$"; then
    if demo_process_instance_matches "$STARTED_PID" "$STARTED_START" "$$"; then
      wait "$STARTED_PID" || true
    fi
    demo_error "Grafana exited before becoming ready; see $LOG_FILE"
    exit 1
  fi
  if demo_grafana_process_is_ready \
    "$STARTED_PID" "$PORT" "$ROOT_DIR" "$RUNTIME_DIR" "$INSTALL_DIR" "$VERSION" "$LOG_DIR" "$Q_PORT"; then
    READY=1
    break
  fi
  sleep 0.25
done
if ((READY == 0)); then
  demo_error "Grafana did not own a healthy IPv4-loopback listener on port $PORT within 60 seconds; see $LOG_FILE"
  exit 1
fi
if ! demo_process_instance_matches "$STARTED_PID" "$STARTED_START" "$$" ||
  ! demo_grafana_process_is_ready \
    "$STARTED_PID" "$PORT" "$ROOT_DIR" "$RUNTIME_DIR" "$INSTALL_DIR" "$VERSION" "$LOG_DIR" "$Q_PORT" ||
  ! demo_process_group_is_safe "$STARTED_PID"; then
  demo_error "Grafana process identity could not be verified after launch"
  exit 1
fi

demo_write_pid_state "$PID_FILE" "$START_FILE" "$STARTED_PID"
PID="$STARTED_PID"

echo "Started Grafana demo locally with PID $PID"
echo "URL: http://127.0.0.1:$PORT/d/asyncq-kdb-demo/asyncq-kdb-demo"
echo "Compatibility matrix: http://127.0.0.1:$PORT/d/asyncq-compat-matrix/asyncq-panopticon-compatibility-matrix"
echo "Panopticon tests: http://127.0.0.1:$PORT/d/asyncq-pano-compat/asyncq-panopticon-compatibility-tests"
echo "Async tests: http://127.0.0.1:$PORT/d/asyncq-async-tests/asyncq-async-execution-tests"
echo "Master data/cache controls: http://127.0.0.1:$PORT/d/asyncq-masterdata-cache/asyncq-master-data-and-cache-controls"
echo "Excel reporting: http://127.0.0.1:$PORT/d/asyncq-excel-report/asyncq-excel-reporting"
echo "Business Suite smoke: http://127.0.0.1:$PORT/d/asyncq-business-suite/asyncq-business-suite-smoke"
echo "WARNING: anonymous users have Admin privileges. Grafana is bound to 127.0.0.1 for local demo use only."
echo "Login: admin / admin, or use anonymous admin access."
echo "Log: $LOG_FILE"
STARTED_PID=""
STARTED_START=""
