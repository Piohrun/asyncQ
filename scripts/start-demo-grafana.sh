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

DEMO_DIR="$ROOT_DIR/demo"
COMPOSE_FILE="$DEMO_DIR/docker-compose.yml"
COMPOSE_PROJECT="asyncq-demo"
RUNTIME_DIR="$DEMO_DIR/runtime"
Q_PORT="${ASYNCQ_DEMO_Q_PORT:-5000}"
Q_PID_FILE="$DEMO_DIR/logs/q.pid"
Q_START_FILE="$Q_PID_FILE.start"
Q_RUNNER="$ROOT_DIR/scripts/run-demo-q-session.sh"
Q_SCRIPT="$DEMO_DIR/q/asyncq_demo.q"
READY_SECONDS="${ASYNCQ_DEMO_DOCKER_READY_TIMEOUT_SECONDS:-60}"
GRAFANA_VERSION="13.1.1"
PINNED_IMAGE="grafana/grafana:13.1.1@sha256:7cb8c64c4d57a57e734073f3cc94620adb24a0acb929bd80ba9f14017e3a975b"
READY=0
ACTIVE_COMMAND_PID=""
ACTIVE_COMMAND_START=""
CAPTURE_FILE=""
DOCKER_CAPTURED=""
COMPOSE_SERVICE_OWNED=0
OWNED_SERVICE_ID=""
CREATE_ATTEMPTED=0
OWNERSHIP_LABEL="io.asyncq.demo.ownership-token"
OWNERSHIP_TOKEN=""
DOCKER_ENGINE_HOST=""
DOCKER_CONFIG_DIR="$RUNTIME_DIR/docker-cli"
DOCKER_HOME="$RUNTIME_DIR/.docker-home"
DOCKER_TMP="$RUNTIME_DIR/.docker-tmp"
BUILD_HOME="$RUNTIME_DIR/.build-home"
BUILD_TMP="$RUNTIME_DIR/.build-tmp"
COMMAND_HOME="$RUNTIME_DIR/.command-home"
COMMAND_TMP="$RUNTIME_DIR/.command-tmp"
DOCKER_DATASOURCE_FILE="$RUNTIME_DIR/docker-datasource-$Q_PORT.yml"
BUILD_GOARCH=""
BUILD_OUTPUTS_READY=0
DOCKER_ENV=()
BUILD_ENV=()
COMMAND_ENV=()

initialize_clean_child_environments() {
  local context="${DOCKER_CONTEXT:-}"
  local config_file="$DOCKER_CONFIG_DIR/config.json"
  local temporary

  if [[ -n "$context" && "$context" != "default" ]]; then
    demo_error "DOCKER_CONTEXT redirection is not accepted; select a local Unix engine with DOCKER_HOST"
    return 1
  fi
  DOCKER_ENGINE_HOST="${DOCKER_HOST:-unix:///var/run/docker.sock}"
  DOCKER_ENGINE_HOST="$(
    demo_validate_local_docker_host "$DOCKER_ENGINE_HOST"
  )" || return 1

  demo_ensure_child_directory "$RUNTIME_DIR" "$DOCKER_CONFIG_DIR" "private Docker configuration"
  demo_ensure_child_directory "$RUNTIME_DIR" "$DOCKER_HOME" "Docker CLI HOME"
  demo_ensure_child_directory "$RUNTIME_DIR" "$DOCKER_TMP" "Docker CLI temporary directory"
  demo_ensure_child_directory "$RUNTIME_DIR" "$BUILD_HOME" "plugin build HOME"
  demo_ensure_child_directory "$RUNTIME_DIR" "$BUILD_TMP" "plugin build temporary directory"
  demo_ensure_child_directory "$RUNTIME_DIR" "$COMMAND_HOME" "command supervisor HOME"
  demo_ensure_child_directory "$RUNTIME_DIR" "$COMMAND_TMP" "command supervisor temporary directory"
  demo_secure_directory "$DOCKER_CONFIG_DIR" "private Docker configuration"
  demo_secure_directory "$DOCKER_HOME" "Docker CLI HOME"
  demo_secure_directory "$DOCKER_TMP" "Docker CLI temporary directory"
  demo_secure_directory "$BUILD_HOME" "plugin build HOME"
  demo_secure_directory "$BUILD_TMP" "plugin build temporary directory"
  demo_secure_directory "$COMMAND_HOME" "command supervisor HOME"
  demo_secure_directory "$COMMAND_TMP" "command supervisor temporary directory"

  demo_require_regular_output \
    "$DOCKER_CONFIG_DIR" \
    "$config_file" \
    "private Docker CLI configuration" || return 1
  temporary="$(mktemp "$DOCKER_CONFIG_DIR/.config.XXXXXX")" || return 1
  if ! printf '%s\n' '{}' >"$temporary" ||
    ! chmod 600 -- "$temporary" ||
    ! mv -f -- "$temporary" "$config_file"; then
    rm -f -- "$temporary"
    return 1
  fi

  demo_prepare_clean_environment DOCKER_ENV "$DOCKER_HOME" "$DOCKER_TMP"
  DOCKER_ENV+=(
    "DOCKER_CONFIG=$DOCKER_CONFIG_DIR"
    "DOCKER_HOST=$DOCKER_ENGINE_HOST"
  )
  demo_prepare_clean_build_environment BUILD_ENV "$BUILD_HOME" "$BUILD_TMP"
  demo_validate_safe_path "$PATH"
  COMMAND_ENV=(
    env -i
    "PATH=$PATH"
    "HOME=$COMMAND_HOME"
    "TMPDIR=$COMMAND_TMP"
    LANG=C
    LC_ALL=C
  )
}

remove_capture_file() {
  local path="$CAPTURE_FILE"

  [[ -n "$path" ]] || return 0
  CAPTURE_FILE=""
  if [[ "${path%/*}" != "$DEMO_DIR" ||
    ! "${path##*/}" =~ ^\.docker-compose-output\.[A-Za-z0-9]{6}$ ]] ||
    ! demo_regular_file_link_count_one "$path"; then
    demo_error "refusing to remove unsafe Docker Compose capture file: $path"
    return 1
  fi
  rm -- "$path"
}

docker_command_capture() {
  local label="$1"
  local max_bytes="$2"
  local status=0
  shift 2

  DOCKER_CAPTURED=""
  CAPTURE_FILE="$(mktemp "$DEMO_DIR/.docker-compose-output.XXXXXX")" || return 1
  if ! demo_capture_bounded_command_output \
    "$CAPTURE_FILE" \
    "$max_bytes" \
    "$label" \
    "$@"; then
    status=1
  else
    DOCKER_CAPTURED="$(<"$CAPTURE_FILE")"
  fi
  remove_capture_file || status=1
  return "$status"
}

docker_capture() {
  local label="$1"
  local max_bytes="$2"
  shift 2

  docker_command_capture \
    "$label" \
    "$max_bytes" \
    "${DOCKER_ENV[@]}" \
    COMPOSE_DISABLE_ENV_FILE=1 \
    ASYNCQ_DEMO_COMPOSE_OWNERSHIP_TOKEN=manual \
    "ASYNCQ_DEMO_Q_PORT=$Q_PORT" \
    timeout --foreground --kill-after=1s 3s \
    docker compose \
    --file "$COMPOSE_FILE" \
    --project-directory "$DEMO_DIR" \
    --project-name "$COMPOSE_PROJECT" \
    "$@"
}

docker_capture_with_ownership() {
  local ownership_value="$1"
  local label="$2"
  local max_bytes="$3"
  shift 3

  [[ "$ownership_value" == "manual" ||
    "$ownership_value" =~ ^[0-9a-f]{64}$ ]] || return 1
  docker_command_capture \
    "$label" \
    "$max_bytes" \
    "${DOCKER_ENV[@]}" \
    COMPOSE_DISABLE_ENV_FILE=1 \
    "ASYNCQ_DEMO_COMPOSE_OWNERSHIP_TOKEN=$ownership_value" \
    "ASYNCQ_DEMO_Q_PORT=$Q_PORT" \
    timeout --foreground --kill-after=1s 3s \
    docker compose \
    --file "$COMPOSE_FILE" \
    --project-directory "$DEMO_DIR" \
    --project-name "$COMPOSE_PROJECT" \
    "$@"
}

docker_read_service_id() {
  if ! docker_capture "Docker Compose service identity" 4096 ps --all -q grafana; then
    return 2
  fi
  if [[ -z "$DOCKER_CAPTURED" ]]; then
    return 1
  fi
  if [[ ! "$DOCKER_CAPTURED" =~ ^[0-9a-f]{64}$ ]]; then
    demo_error "Docker Compose returned an invalid or ambiguous Grafana service identity"
    return 2
  fi
  SERVICE_ID="$DOCKER_CAPTURED"
}

docker_container_has_ownership_token() {
  local container_id="$1"

  [[ "$container_id" =~ ^[0-9a-f]{64}$ &&
    "$OWNERSHIP_TOKEN" =~ ^[0-9a-f]{64}$ ]] || return 1
  docker_command_capture \
    "Docker container ownership identity" \
    1024 \
    "${DOCKER_ENV[@]}" timeout --foreground --kill-after=1s 3s \
    docker container inspect \
    --format "{{.Id}} {{index .Config.Labels \"$OWNERSHIP_LABEL\"}}" \
    "$container_id" || return 1
  [[ "$DOCKER_CAPTURED" == "$container_id $OWNERSHIP_TOKEN" ]]
}

docker_find_token_container() {
  [[ "$OWNERSHIP_TOKEN" =~ ^[0-9a-f]{64}$ ]] || return 2
  docker_command_capture \
    "Docker ownership-token container identity" \
    4096 \
    "${DOCKER_ENV[@]}" timeout --foreground --kill-after=1s 3s \
    docker container ls \
    --all \
    --quiet \
    --no-trunc \
    --filter "label=$OWNERSHIP_LABEL=$OWNERSHIP_TOKEN" || return 2
  if [[ -z "$DOCKER_CAPTURED" ]]; then
    return 1
  fi
  if [[ ! "$DOCKER_CAPTURED" =~ ^[0-9a-f]{64}$ ]]; then
    demo_error "Docker returned an invalid or ambiguous ownership-token container identity"
    return 2
  fi
  TOKEN_CONTAINER_ID="$DOCKER_CAPTURED"
  docker_container_has_ownership_token "$TOKEN_CONTAINER_ID" || return 2
}

docker_exact_container_exists() {
  local container_id="$1"

  [[ "$container_id" =~ ^[0-9a-f]{64}$ ]] || return 2
  docker_command_capture \
    "exact Docker container identity" \
    4096 \
    "${DOCKER_ENV[@]}" timeout --foreground --kill-after=1s 3s \
    docker container ls \
    --all \
    --quiet \
    --no-trunc \
    --filter "id=$container_id" || return 2
  if [[ -z "$DOCKER_CAPTURED" ]]; then
    return 1
  fi
  [[ "$DOCKER_CAPTURED" == "$container_id" ]] || return 2
}

docker_expected_config_hash() {
  local ownership_value="$1"

  docker_capture_with_ownership \
    "$ownership_value" \
    "pinned Docker Compose service hash" \
    4096 \
    config --hash grafana || return 1
  [[ "$DOCKER_CAPTURED" =~ ^grafana[[:space:]]([0-9a-f]{64})$ ]] || return 1
  EXPECTED_CONFIG_HASH="${BASH_REMATCH[1]}"
}

docker_container_environment_is_safe() {
  local container_id="$1"
  local address_count=0
  local port_count=0
  local q_port_count=0
  local item

  docker_command_capture \
    "Docker container environment" \
    65536 \
    "${DOCKER_ENV[@]}" timeout --foreground --kill-after=1s 3s \
    docker container inspect \
    --format '{{range .Config.Env}}{{println .}}{{end}}' \
    "$container_id" || return 1
  while IFS= read -r item; do
    case "$item" in
      GF_SERVER_HTTP_ADDR=127.0.0.1)
        address_count=$((address_count + 1))
        ;;
      GF_SERVER_HTTP_ADDR=*)
        return 1
        ;;
      GF_SERVER_HTTP_PORT=3000)
        port_count=$((port_count + 1))
        ;;
      GF_SERVER_HTTP_PORT=*)
        return 1
        ;;
      "ASYNCQ_DEMO_Q_PORT=$Q_PORT")
        q_port_count=$((q_port_count + 1))
        ;;
      ASYNCQ_DEMO_Q_PORT=*)
        return 1
        ;;
    esac
  done <<<"$DOCKER_CAPTURED"
  ((address_count == 1 && port_count == 1 && q_port_count == 1))
}

docker_container_matches_pinned_config() {
  local container_id="$1"
  local actual_config_hash
  local actual_image_id
  local actual_image_ref
  local actual_network_mode
  local actual_project
  local actual_q_port
  local actual_service
  local host_pid
  local inspected_id
  local ownership_value

  docker_command_capture \
    "Docker container pinned configuration identity" \
    8192 \
    "${DOCKER_ENV[@]}" timeout --foreground --kill-after=1s 3s \
    docker container inspect \
    --format "{{.Id}} {{index .Config.Labels \"$OWNERSHIP_LABEL\"}} {{index .Config.Labels \"io.asyncq.demo.q-port\"}} {{index .Config.Labels \"com.docker.compose.config-hash\"}} {{index .Config.Labels \"com.docker.compose.project\"}} {{index .Config.Labels \"com.docker.compose.service\"}} {{.Config.Image}} {{.Image}} {{.HostConfig.NetworkMode}} {{.State.Pid}}" \
    "$container_id" || return 1
  read -r \
    inspected_id \
    ownership_value \
    actual_q_port \
    actual_config_hash \
    actual_project \
    actual_service \
    actual_image_ref \
    actual_image_id \
    actual_network_mode \
    host_pid <<<"$DOCKER_CAPTURED"
  [[ "$inspected_id" == "$container_id" &&
    "$container_id" =~ ^[0-9a-f]{64}$ ]] || return 1
  [[ "$ownership_value" == "manual" ||
    "$ownership_value" =~ ^[0-9a-f]{64}$ ]] || return 1
  [[
    "$actual_q_port" == "$Q_PORT" &&
    "$actual_config_hash" =~ ^[0-9a-f]{64}$ &&
    "$actual_project" == "$COMPOSE_PROJECT" &&
    "$actual_service" == "grafana" &&
    "$actual_image_ref" == "$PINNED_IMAGE" &&
    "$actual_image_id" =~ ^sha256:[0-9a-f]{64}$ &&
    "$actual_network_mode" == "host" &&
    "$host_pid" =~ ^[1-9][0-9]{0,9}$ ]] || return 1

  docker_expected_config_hash "$ownership_value" || return 1
  [[ "$actual_config_hash" == "$EXPECTED_CONFIG_HASH" ]] || return 1
  docker_command_capture \
    "pinned Docker image identity" \
    1024 \
    "${DOCKER_ENV[@]}" timeout --foreground --kill-after=1s 3s \
    docker image inspect \
    --format '{{.Id}}' "$PINNED_IMAGE" || return 1
  [[ "$DOCKER_CAPTURED" == "$actual_image_id" ]] || return 1
  docker_container_environment_is_safe "$container_id" || return 1

}

docker_service_is_strictly_ready() {
  local expected_id="$1"

  validate_docker_bind_publication || return 1
  docker_container_matches_pinned_config "$expected_id" || return 1
  docker_capture \
    "running Docker Compose service identity" \
    4096 \
    ps --status running -q grafana || return 1
  [[ "$DOCKER_CAPTURED" == "$expected_id" ]] || return 1
  demo_verified_q_runner_on_port \
    "$Q_PID_FILE" \
    "$Q_START_FILE" \
    "$ROOT_DIR" \
    "$Q_RUNNER" \
    "$Q_SCRIPT" \
    "$Q_PORT" >/dev/null || return 1
  demo_ipv4_loopback_port_is_reachable "$Q_PORT" || return 1
  demo_fetch_grafana_health 3000 "$GRAFANA_VERSION" "$DEMO_DIR" || return 1
  demo_fetch_grafana_demo_identity 3000 "$DEMO_DIR"
}

validate_docker_bind_publication() {
  ((BUILD_OUTPUTS_READY == 1)) || return 1
  demo_validate_build_output_publication "$ROOT_DIR" "$BUILD_GOARCH" ||
    return 1
  demo_validate_docker_static_publication "$ROOT_DIR" || return 1
  demo_regular_file_link_count_one "$DOCKER_DATASOURCE_FILE" || return 1
  [[ "$(stat -c '%a' -- "$DOCKER_DATASOURCE_FILE")" == "644" ]]
}

prepare_docker_datasource() {
  local source_file="$DEMO_DIR/grafana/provisioning/datasources/asyncq.yml"
  local output_file="$DOCKER_DATASOURCE_FILE"
  local output_tmp

  demo_require_regular_output \
    "$RUNTIME_DIR" \
    "$output_file" \
    "generated Docker datasource provisioning" || return 1
  output_tmp="$(mktemp "$RUNTIME_DIR/.docker-datasource.XXXXXX")" || return 1
  if ! demo_render_loopback_datasource "$source_file" "$output_tmp" "$Q_PORT" ||
    ! demo_set_public_regular_file_mode \
      "$RUNTIME_DIR" \
      "$output_tmp" \
      "temporary Docker datasource provisioning"; then
    rm -- "$output_tmp"
    demo_error "could not generate loopback-only Docker datasource provisioning"
    return 1
  fi
  if [[ -e "$output_file" ]]; then
    if ! demo_node -e '
      const fs = require("fs");
      process.exit(
        fs.readFileSync(process.argv[1]).equals(fs.readFileSync(process.argv[2]))
          ? 0
          : 1
      );
    ' "$output_file" "$output_tmp"; then
      rm -- "$output_tmp"
      demo_error "existing Docker datasource provisioning differs from the canonical q port $Q_PORT configuration"
      return 1
    fi
    rm -- "$output_tmp"
  else
    mv -- "$output_tmp" "$output_file"
  fi
  demo_set_public_regular_file_mode \
    "$RUNTIME_DIR" \
    "$output_file" \
    "generated Docker datasource provisioning"
}

stop_active_command() {
  [[ -n "$ACTIVE_COMMAND_PID" ]] || return 0

  if [[ -n "$ACTIVE_COMMAND_START" ]] &&
    demo_process_instance_matches \
      "$ACTIVE_COMMAND_PID" "$ACTIVE_COMMAND_START" "$$"; then
    if ! demo_stop_process_group \
      "$ACTIVE_COMMAND_PID" "$ACTIVE_COMMAND_START" "$$"; then
      demo_terminate_owned_child "$ACTIVE_COMMAND_PID" 1 || true
    else
      wait "$ACTIVE_COMMAND_PID" 2>/dev/null || true
    fi
  else
    # $! remains this shell's exact unreaped child if identity capture failed.
    demo_terminate_owned_child "$ACTIVE_COMMAND_PID" 1 || true
  fi
  ACTIVE_COMMAND_PID=""
  ACTIVE_COMMAND_START=""
}

run_owned_command() {
  local attempt
  local stat_line
  local state
  local status
  local verified=0

  # shellcheck disable=SC2016 # The child Bash expands its own PID and arguments.
  "${COMMAND_ENV[@]}" \
    setsid bash -c 'kill -STOP "$$"; exec "$@"' asyncq-owned-command "$@" &
  ACTIVE_COMMAND_PID="$!"
  if ! ACTIVE_COMMAND_START="$(demo_pid_start_time "$ACTIVE_COMMAND_PID")"; then
    demo_error "owned startup command exited before its identity could be captured"
    stop_active_command
    return 1
  fi
  for ((attempt = 0; attempt < 100; attempt++)); do
    if demo_process_instance_matches \
      "$ACTIVE_COMMAND_PID" "$ACTIVE_COMMAND_START" "$$" &&
      demo_process_group_is_safe "$ACTIVE_COMMAND_PID" &&
      [[ -r "/proc/$ACTIVE_COMMAND_PID/stat" ]]; then
      stat_line="$(<"/proc/$ACTIVE_COMMAND_PID/stat")"
      state="$(demo_proc_stat_state "$stat_line" 2>/dev/null || true)"
      if [[ "$state" == "T" || "$state" == "t" ]]; then
        verified=1
        break
      fi
    fi
    sleep 0.01
  done
  if ((verified == 0)); then
    demo_error "owned startup command process identity could not be verified"
    stop_active_command
    return 1
  fi
  if ! kill -CONT "$ACTIVE_COMMAND_PID"; then
    demo_error "could not release the verified owned startup command"
    stop_active_command
    return 1
  fi
  if wait "$ACTIVE_COMMAND_PID"; then
    status=0
  else
    status=$?
  fi
  ACTIVE_COMMAND_PID=""
  ACTIVE_COMMAND_START=""
  return "$status"
}

cleanup_owned_compose_service() {
  local state_status
  local cleanup_status=0

  if [[ -z "$OWNED_SERVICE_ID" ]]; then
    if docker_find_token_container; then
      OWNED_SERVICE_ID="$TOKEN_CONTAINER_ID"
      COMPOSE_SERVICE_OWNED=1
    else
      state_status=$?
      case "$state_status" in
        1)
          return 0
          ;;
        *)
          demo_error "could not safely identify a token-owned Grafana container for cleanup"
          return 1
          ;;
      esac
    fi
  fi

  if docker_exact_container_exists "$OWNED_SERVICE_ID"; then
    :
  else
    state_status=$?
    case "$state_status" in
      1)
        return 0
        ;;
      *)
        demo_error "could not verify the exact owned Grafana container before cleanup"
        return 1
        ;;
    esac
  fi

  if ! docker_container_has_ownership_token "$OWNED_SERVICE_ID"; then
    demo_error "owned Grafana container no longer bears this invocation's token; refusing removal"
    return 1
  fi
  if ! "${DOCKER_ENV[@]}" timeout --foreground --kill-after=5s 30s \
    docker container rm \
    --force "$OWNED_SERVICE_ID" >/dev/null; then
    demo_error "Docker could not remove the exact token-owned Grafana container $OWNED_SERVICE_ID"
    cleanup_status=1
  fi
  if docker_exact_container_exists "$OWNED_SERVICE_ID"; then
    demo_error "token-owned Grafana container $OWNED_SERVICE_ID remains after cleanup"
    cleanup_status=1
  else
    state_status=$?
    if [[ "$state_status" != "1" ]]; then
      demo_error "could not verify that the token-owned Grafana container was removed"
      cleanup_status=1
    fi
  fi
  return "$cleanup_status"
}

docker_diagnostics() {
  local diagnostics

  set +e
  diagnostics="$(
    {
      "${DOCKER_ENV[@]}" \
        COMPOSE_DISABLE_ENV_FILE=1 \
        ASYNCQ_DEMO_COMPOSE_OWNERSHIP_TOKEN=manual \
        "ASYNCQ_DEMO_Q_PORT=$Q_PORT" \
        timeout --foreground --kill-after=2s 5s \
        docker compose \
        --file "$COMPOSE_FILE" \
        --project-directory "$DEMO_DIR" \
        --project-name "$COMPOSE_PROJECT" \
        ps --all
      "${DOCKER_ENV[@]}" \
        COMPOSE_DISABLE_ENV_FILE=1 \
        ASYNCQ_DEMO_COMPOSE_OWNERSHIP_TOKEN=manual \
        "ASYNCQ_DEMO_Q_PORT=$Q_PORT" \
        timeout --foreground --kill-after=2s 5s \
        docker compose \
        --file "$COMPOSE_FILE" \
        --project-directory "$DEMO_DIR" \
        --project-name "$COMPOSE_PROJECT" \
        logs --no-color --tail 80 grafana
    } 2>&1 | head -c 32768
  )"
  set -e
  if [[ -n "$diagnostics" ]]; then
    printf '%s\n' "$diagnostics" >&2
  fi
}

print_ready() {
  echo "Grafana is ready at http://127.0.0.1:3000"
  echo "WARNING: anonymous users have Admin privileges. Grafana is verified to listen only on host loopback."
  echo "Login: admin / admin, or use anonymous admin access."
  echo "Dashboard: http://127.0.0.1:3000/d/asyncq-kdb-demo/asyncq-kdb-demo"
  echo "Compatibility matrix: http://127.0.0.1:3000/d/asyncq-compat-matrix/asyncq-panopticon-compatibility-matrix"
  echo "Panopticon tests: http://127.0.0.1:3000/d/asyncq-pano-compat/asyncq-panopticon-compatibility-tests"
  echo "Async tests: http://127.0.0.1:3000/d/asyncq-async-tests/asyncq-async-execution-tests"
  echo "Master data/cache controls: http://127.0.0.1:3000/d/asyncq-masterdata-cache/asyncq-master-data-and-cache-controls"
  echo "Excel reporting: http://127.0.0.1:3000/d/asyncq-excel-report/asyncq-excel-reporting"
  echo "Business Suite smoke: http://127.0.0.1:3000/d/asyncq-business-suite/asyncq-business-suite-smoke"
}

# shellcheck disable=SC2329 # Invoked by the EXIT trap.
cleanup() {
  local status=$?

  trap - EXIT INT TERM
  stop_active_command || true
  remove_capture_file || true
  if ((COMPOSE_SERVICE_OWNED == 1 || CREATE_ATTEMPTED == 1)); then
    if ! cleanup_owned_compose_service; then
      demo_error "automatic Docker cleanup failed; inspect the fixed '$COMPOSE_PROJECT' project with '$COMPOSE_FILE' and remove only its exact Grafana container"
    fi
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ ! "$READY_SECONDS" =~ ^[1-9][0-9]{0,2}$ ]] ||
  ((10#$READY_SECONDS > 300)); then
  demo_error "ASYNCQ_DEMO_DOCKER_READY_TIMEOUT_SECONDS must be in 1..300"
  exit 2
fi
demo_validate_port "ASYNCQ_DEMO_Q_PORT" "$Q_PORT"
for required_command in docker curl timeout node setsid head wc; do
  command -v "$required_command" >/dev/null 2>&1 || {
    demo_error "$required_command is required for the Docker Grafana demo"
    exit 1
  }
done
demo_require_real_directory "$DEMO_DIR" "demo directory"
demo_regular_file_link_count_one "$COMPOSE_FILE" || {
  demo_error "Docker Compose file is linked or unsafe: $COMPOSE_FILE"
  exit 1
}
demo_ensure_child_directory "$DEMO_DIR" "$RUNTIME_DIR" "Docker demo runtime directory"
demo_secure_directory "$RUNTIME_DIR" "Docker demo runtime directory"
demo_prepare_build_output_roots "$ROOT_DIR"
BUILD_GOARCH="$(demo_resolve_build_goarch)"
demo_publish_docker_static_trees "$ROOT_DIR"
if demo_build_output_trees_complete "$ROOT_DIR" "$BUILD_GOARCH"; then
  demo_publish_build_output_trees "$ROOT_DIR" "$BUILD_GOARCH"
  BUILD_OUTPUTS_READY=1
fi
initialize_clean_child_environments
if ! demo_verified_q_runner_on_port \
  "$Q_PID_FILE" \
  "$Q_START_FILE" \
  "$ROOT_DIR" \
  "$Q_RUNNER" \
  "$Q_SCRIPT" \
  "$Q_PORT" >/dev/null; then
  demo_error "the exact recorded q demo is not healthy on IPv4 loopback port $Q_PORT"
  exit 1
fi
prepare_docker_datasource
cd "$DEMO_DIR"
docker_capture "Docker Compose version" 4096 version

SERVICE_ID=""
if docker_read_service_id; then
  if docker_service_is_strictly_ready "$SERVICE_ID"; then
    echo "Reusing existing verified Grafana Compose service $SERVICE_ID"
    print_ready
    exit 0
  fi
  demo_error "an existing Grafana Compose service is present but is not strictly healthy; it was preserved"
  demo_error "inspect it with 'docker compose --file $COMPOSE_FILE --project-directory $DEMO_DIR --project-name $COMPOSE_PROJECT ps --all' and stop it explicitly before retrying"
  exit 1
else
  SERVICE_STATE=$?
  if [[ "$SERVICE_STATE" != "1" ]]; then
    demo_error "could not determine whether a Grafana Compose service already exists"
    exit 1
  fi
fi

run_owned_command \
  "${BUILD_ENV[@]}" timeout --foreground --kill-after=10s 10m \
  bash "$ROOT_DIR/scripts/build-demo-plugin.sh"
demo_publish_build_output_trees "$ROOT_DIR" "$BUILD_GOARCH"
demo_validate_build_output_publication "$ROOT_DIR" "$BUILD_GOARCH"
BUILD_OUTPUTS_READY=1

# Recheck after the potentially long build so a concurrently created service is preserved.
if docker_read_service_id; then
  demo_error "a Grafana Compose service appeared during the build; it was preserved and startup was not attempted"
  exit 1
else
  SERVICE_STATE=$?
  if [[ "$SERVICE_STATE" != "1" ]]; then
    demo_error "could not re-verify Grafana Compose service absence"
    exit 1
  fi
fi

OWNERSHIP_TOKEN="$(
  demo_node -e 'process.stdout.write(require("crypto").randomBytes(32).toString("hex"))'
)"
[[ "$OWNERSHIP_TOKEN" =~ ^[0-9a-f]{64}$ ]] || {
  demo_error "could not generate a valid Docker ownership token"
  exit 1
}
validate_docker_bind_publication
CREATE_ATTEMPTED=1
if ! run_owned_command \
  "${DOCKER_ENV[@]}" \
  COMPOSE_DISABLE_ENV_FILE=1 \
  "ASYNCQ_DEMO_COMPOSE_OWNERSHIP_TOKEN=$OWNERSHIP_TOKEN" \
  "ASYNCQ_DEMO_Q_PORT=$Q_PORT" \
  timeout --foreground --kill-after=10s 2m \
  docker compose \
  --file "$COMPOSE_FILE" \
  --project-directory "$DEMO_DIR" \
  --project-name "$COMPOSE_PROJECT" \
  up --no-start --no-recreate --no-deps grafana; then
  demo_error "Docker Compose failed to create the Grafana service"
  docker_diagnostics
  exit 1
fi

if docker_find_token_container; then
  :
else
  TOKEN_STATE=$?
  if [[ "$TOKEN_STATE" == "1" ]] && docker_read_service_id; then
    demo_error "an unowned Grafana Compose service won the create race; it was preserved"
  else
    demo_error "Docker Compose did not create exactly one container bearing this invocation's ownership token"
  fi
  exit 1
fi
OWNED_SERVICE_ID="$TOKEN_CONTAINER_ID"
COMPOSE_SERVICE_OWNED=1
if ! docker_read_service_id || [[ "$SERVICE_ID" != "$OWNED_SERVICE_ID" ]]; then
  demo_error "the token-owned container is not the exact current Grafana Compose service"
  exit 1
fi
if ! run_owned_command \
  "${DOCKER_ENV[@]}" timeout --foreground --kill-after=10s 2m \
  docker container start "$OWNED_SERVICE_ID"; then
  demo_error "Docker failed to start token-owned Grafana container $OWNED_SERVICE_ID"
  docker_diagnostics
  exit 1
fi

READINESS_DEADLINE=$((SECONDS + 10#$READY_SECONDS))
while ((SECONDS < READINESS_DEADLINE)); do
  if docker_read_service_id; then
    if [[ "$SERVICE_ID" != "$OWNED_SERVICE_ID" ]]; then
      demo_error "Grafana service identity changed during startup; refusing to manage the replacement"
      exit 1
    fi
    if docker_container_has_ownership_token "$OWNED_SERVICE_ID" &&
      docker_service_is_strictly_ready "$SERVICE_ID"; then
      READY=1
      break
    fi
  else
    SERVICE_STATE=$?
    if [[ "$SERVICE_STATE" != "1" ]]; then
      demo_error "could not inspect the newly started Grafana Compose service"
      break
    fi
  fi
  sleep 0.25
done

if ((READY == 0)); then
  demo_error "Grafana Compose service did not become a healthy loopback-only service within $READY_SECONDS seconds"
  docker_diagnostics
  exit 1
fi

COMPOSE_SERVICE_OWNED=0
CREATE_ATTEMPTED=0
print_ready
