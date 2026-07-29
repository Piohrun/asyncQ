#!/bin/bash
# shellcheck disable=SC2016,SC2329
set -euo pipefail
umask 077

SCRIPT_SOURCE="${BASH_SOURCE[0]}"
SCRIPT_PARENT="${SCRIPT_SOURCE%/*}"
[[ "$SCRIPT_PARENT" != "$SCRIPT_SOURCE" ]] || SCRIPT_PARENT=.
ROOT_DIR="$(
  builtin cd -- "$SCRIPT_PARENT/../.." &&
    builtin pwd -P
)" || {
  printf 'Error: could not resolve the repository root\n' >&2
  exit 1
}
# shellcheck source=scripts/demo-launch-common.sh
source "$ROOT_DIR/scripts/demo-launch-common.sh"
demo_validate_safe_path "$PATH"

TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/asyncq-demo-launch-test.XXXXXX")"
if [[ "${ASYNCQ_KEEP_LAUNCHER_TEST_DIR:-0}" == "1" ]]; then
  trap 'printf "preserved test fixture: %s\n" "$TEST_DIR" >&2' EXIT
else
  trap 'rm -rf -- "$TEST_DIR"' EXIT
fi

PASSED=0

pass() {
  PASSED=$((PASSED + 1))
}

test_pid_is_live() {
  local pid="$1"
  local stat_line
  local state

  [[ "$pid" =~ ^[1-9][0-9]*$ && -r "/proc/$pid/stat" ]] || return 1
  stat_line="$(<"/proc/$pid/stat")"
  state="$(demo_proc_stat_state "$stat_line")" || return 1
  [[ "$state" != "Z" && "$state" != "X" ]]
}

expect_success() {
  local label="$1"
  shift

  if "$@" >/dev/null 2>&1; then
    pass
  else
    echo "not ok: $label" >&2
    exit 1
  fi
}

expect_failure() {
  local label="$1"
  shift

  if "$@" >/dev/null 2>&1; then
    echo "not ok: $label unexpectedly succeeded" >&2
    exit 1
  else
    pass
  fi
}

expect_output() {
  local label="$1"
  local expected="$2"
  shift 2
  local actual

  if ! actual="$("$@" 2>/dev/null)"; then
    echo "not ok: $label failed" >&2
    exit 1
  fi
  if [[ "$actual" != "$expected" ]]; then
    echo "not ok: $label returned '$actual', expected '$expected'" >&2
    exit 1
  fi
  pass
}

expect_success "minimum port" demo_validate_port TEST_PORT 1
expect_success "maximum port" demo_validate_port TEST_PORT 65535
for hostile_port in 0 65536 01 +1 -1 ' 1' '1 ' '1;id' 1.0; do
  expect_failure "hostile port $hostile_port" demo_validate_port TEST_PORT "$hostile_port"
done

expect_success \
  "absolute PATH entries without empty components" \
  demo_validate_safe_path \
  /usr/local/bin:/usr/bin
expect_success \
  "absolute PATH entries may contain ordinary spaces" \
  demo_validate_safe_path \
  '/opt/Tool Chain/bin:/usr/bin'
for hostile_path in \
  '' \
  ':/usr/bin' \
  '/usr/bin:' \
  '/usr/bin::/bin' \
  'bin:/usr/bin' \
  '.:/usr/bin' \
  '/usr/bin: /bin' \
  '/usr/bin/../bin' \
  $'/usr/bin:\n/bin' \
  $'/usr/bin:\t/bin'; do
  expect_failure \
    "unsafe PATH is rejected without normalization: ${hostile_path:-<empty>}" \
    demo_validate_safe_path \
    "$hostile_path"
done

clean_environment_rejects_trailing_empty_path_before_cwd_lookup() {
  local fixture="$TEST_DIR/clean-environment-path"
  local cwd="$fixture/cwd"
  local -a child_environment=(unchanged)

  mkdir -p "$cwd" "$fixture/home" "$fixture/tmp"
  chmod 700 "$fixture/home" "$fixture/tmp"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "printf executed >\"$fixture/probe-executed.marker\"" \
    >"$cwd/asyncq-probe"
  chmod +x "$cwd/asyncq-probe"

  if PATH=/usr/bin: \
    demo_prepare_clean_environment \
    child_environment \
    "$fixture/home" \
    "$fixture/tmp" >/dev/null 2>&1; then
    (
      cd "$cwd"
      "${child_environment[@]}" /usr/bin/bash -c \
        'command -v asyncq-probe >"$1"; asyncq-probe' \
        bash \
        "$fixture/probe-resolved.marker"
    ) >/dev/null 2>&1 || true
    return 1
  fi
  [[ ! -e "$fixture/probe-resolved.marker" &&
    ! -e "$fixture/probe-executed.marker" ]]
}

expect_success \
  "clean child construction rejects a trailing-empty PATH before cwd lookup or execution" \
  clean_environment_rejects_trailing_empty_path_before_cwd_lookup

clean_environment_preserves_spaced_path_and_resolves_command() {
  local fixture="$TEST_DIR/clean environment spaced path"
  local bin_dir="$fixture/Tool Chain/bin"
  local safe_path="$bin_dir:/usr/bin"
  local marker="$fixture/probe-path.marker"
  local -a child_environment=()

  mkdir -p "$bin_dir" "$fixture/home" "$fixture/tmp"
  chmod 700 "$fixture/home" "$fixture/tmp"
  printf '%s\n' \
    '#!/bin/bash' \
    'printf "%s" "$PATH" >"$1"' \
    >"$bin_dir/asyncq-space-probe"
  chmod +x "$bin_dir/asyncq-space-probe"

  PATH="$safe_path" \
    demo_prepare_clean_environment \
    child_environment \
    "$fixture/home" \
    "$fixture/tmp" &&
    [[ "${child_environment[2]}" == "PATH=$safe_path" ]] &&
    "${child_environment[@]}" asyncq-space-probe "$marker" &&
    [[ "$(<"$marker")" == "$safe_path" ]]
}

expect_success \
  "clean child preserves a spaced absolute PATH byte-for-byte and resolves its command" \
  clean_environment_preserves_spaced_path_and_resolves_command

all_executable_bootstraps_reject_poisoned_path_before_lookup() {
  local fixture="$TEST_DIR/bootstrap-poisoned-path"
  local cwd="$fixture/cwd"
  local entrypoint
  local entrypoint_name
  local marker="$fixture/prevalidation-command.marker"
  local mode
  local stderr_file
  local planted_name
  local shebang
  local -a entrypoints=()
  local -a entrypoint_args=()

  mkdir -p \
    "$fixture/scripts" \
    "$fixture/tests/shell" \
    "$fixture/demo/logs" \
    "$fixture/demo/runtime" \
    "$fixture/demo/q" \
    "$fixture/state" \
    "$cwd"
  cp \
    "$ROOT_DIR/scripts/build-demo-plugin.sh" \
    "$ROOT_DIR/scripts/demo-launch-common.sh" \
    "$ROOT_DIR/scripts/install-demo-business-plugins.sh" \
    "$ROOT_DIR/scripts/run-demo-q-session.sh" \
    "$ROOT_DIR/scripts/start-demo-grafana-local.sh" \
    "$ROOT_DIR/scripts/start-demo-grafana.sh" \
    "$ROOT_DIR/scripts/start-demo-local.sh" \
    "$ROOT_DIR/scripts/start-demo-q.sh" \
    "$ROOT_DIR/scripts/stop-demo-grafana-local.sh" \
    "$ROOT_DIR/scripts/stop-demo-local.sh" \
    "$ROOT_DIR/scripts/stop-demo-q.sh" \
    "$fixture/scripts/"
  cp "$ROOT_DIR/tests/shell/demo-launchers-security.sh" \
    "$fixture/tests/shell/"
  printf 'fixture\n' >"$fixture/demo/q/asyncq_demo.q"
  IFS= read -r shebang <"$fixture/scripts/demo-launch-common.sh"
  [[ "$shebang" == '#!/bin/bash' &&
    ! -x "$ROOT_DIR/scripts/demo-launch-common.sh" &&
    ! -x "$fixture/scripts/demo-launch-common.sh" ]] || return 1

  for planted_name in \
    bash dirname asyncq-probe \
    npm uname docker curl timeout node env setsid tar head wc q \
    mktemp mkdir chmod rm stat; do
    printf '%s\n' \
      '#!/bin/bash' \
      "printf '%s\\n' '$planted_name' >>'$marker'" \
      'exit 97' \
      >"$cwd/$planted_name"
    chmod +x "$cwd/$planted_name"
  done

  entrypoints=(
    "$fixture/scripts/build-demo-plugin.sh"
    "$fixture/scripts/install-demo-business-plugins.sh"
    "$fixture/scripts/run-demo-q-session.sh"
    "$fixture/scripts/start-demo-grafana-local.sh"
    "$fixture/scripts/start-demo-grafana.sh"
    "$fixture/scripts/start-demo-local.sh"
    "$fixture/scripts/start-demo-q.sh"
    "$fixture/scripts/stop-demo-grafana-local.sh"
    "$fixture/scripts/stop-demo-local.sh"
    "$fixture/scripts/stop-demo-q.sh"
    "$fixture/tests/shell/demo-launchers-security.sh"
  )

  for entrypoint in "${entrypoints[@]}"; do
    IFS= read -r shebang <"$entrypoint"
    [[ "$shebang" == '#!/bin/bash' && -x "$entrypoint" ]] || return 1
    entrypoint_name="${entrypoint##*/}"
    entrypoint_args=()
    if [[ "$entrypoint_name" == "run-demo-q-session.sh" ]]; then
      entrypoint_args=(
        /usr/bin/false
        "$fixture/demo/q/asyncq_demo.q"
        5126
        "$fixture/state"
      )
    fi
    for mode in direct explicit-bash; do
      stderr_file="$fixture/$entrypoint_name.$mode.stderr"
      if (
        builtin cd -- "$cwd"
        if [[ "$mode" == direct ]]; then
          PATH=:/usr/bin "$entrypoint" "${entrypoint_args[@]}"
        else
          PATH=:/usr/bin /bin/bash "$entrypoint" "${entrypoint_args[@]}"
        fi
      ) 2>"$stderr_file"; then
        return 1
      fi
      grep -Fxq \
        'Error: PATH must not contain empty entries' \
        "$stderr_file" || return 1
      [[ ! -e "$marker" ]] || return 1
    done
  done

  [[ ! -e "$fixture/demo/logs/q.pid" &&
    ! -e "$fixture/demo/logs/q.pid.start" &&
    ! -e "$fixture/demo/runtime/grafana.pid" &&
    ! -e "$fixture/demo/runtime/grafana.pid.start" &&
    ! -e "$fixture/docker.commands.nul" &&
    ! -e "$fixture/build-child-launched.marker" &&
    ! -e "$fixture/up.marker" &&
    ! -e "$fixture/service.id" ]] &&
    ! compgen -G "$fixture/state/.q-session.*" >/dev/null &&
    ! compgen -G "$fixture/demo/.docker-compose-output.*" >/dev/null &&
    ! compgen -G "$fixture/demo/runtime/.plugin-build-status.*" >/dev/null
}

expect_success \
  "every executable bootstrap rejects poisoned PATH before interpreter, utility, child, or capture dispatch" \
  all_executable_bootstraps_reject_poisoned_path_before_lookup

build_launcher_rejects_unsafe_output_trees_before_tools() {
  local mode
  local fixture
  local fake_bin
  local outside

  for mode in \
    dist-root-symlink \
    master-root-symlink \
    excel-root-symlink \
    dist-nested-symlink \
    master-hardlink \
    excel-fifo; do
    fixture="$TEST_DIR/build-output-$mode"
    fake_bin="$fixture/fake-bin"
    outside="$fixture/outside"
    mkdir -p "$fixture/scripts" "$fake_bin" "$outside"
    cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
    cp "$ROOT_DIR/scripts/build-demo-plugin.sh" "$fixture/scripts/"
    printf 'preserve\n' >"$outside/victim"
    printf '%s\n' \
      '#!/bin/bash' \
      "printf dispatched >\"$fixture/npm-dispatched.marker\"" \
      'exit 97' \
      >"$fake_bin/npm"
    printf '%s\n' \
      '#!/bin/bash' \
      "printf dispatched >\"$fixture/go-dispatched.marker\"" \
      'exit 98' \
      >"$fake_bin/go"
    chmod +x "$fixture/scripts/build-demo-plugin.sh" "$fake_bin/npm" "$fake_bin/go"

    case "$mode" in
      dist-root-symlink)
        ln -s "$outside" "$fixture/dist"
        ;;
      master-root-symlink)
        mkdir -p "$fixture/dist" "$fixture/dist-panel"
        ln -s "$outside" "$fixture/dist-panel/asyncq-masterdata-panel"
        ;;
      excel-root-symlink)
        mkdir -p \
          "$fixture/dist" \
          "$fixture/dist-panel/asyncq-masterdata-panel"
        ln -s "$outside" "$fixture/dist-panel/asyncq-excel-report-panel"
        ;;
      dist-nested-symlink)
        mkdir -p "$fixture/dist"
        ln -s "$outside/victim" "$fixture/dist/module.js"
        ;;
      master-hardlink)
        mkdir -p \
          "$fixture/dist" \
          "$fixture/dist-panel/asyncq-masterdata-panel"
        ln "$outside/victim" \
          "$fixture/dist-panel/asyncq-masterdata-panel/module.js"
        ;;
      excel-fifo)
        mkdir -p \
          "$fixture/dist" \
          "$fixture/dist-panel/asyncq-masterdata-panel" \
          "$fixture/dist-panel/asyncq-excel-report-panel"
        mkfifo "$fixture/dist-panel/asyncq-excel-report-panel/module.js"
        ;;
    esac

    if PATH="$fake_bin:/usr/bin" \
      "$fixture/scripts/build-demo-plugin.sh" >/dev/null 2>&1; then
      return 1
    fi
    [[ "$(<"$outside/victim")" == "preserve" &&
      ! -e "$fixture/npm-dispatched.marker" &&
      ! -e "$fixture/go-dispatched.marker" ]] || return 1
  done
}

expect_success \
  "standalone build rejects root/nested links, hardlinks, and FIFOs across every output tree before npm or Go" \
  build_launcher_rejects_unsafe_output_trees_before_tools

local_launcher_rejects_unsafe_output_before_build_session() {
  local fixture="$TEST_DIR/local-unsafe-build-output"
  local outside="$fixture/outside"

  mkdir -p "$fixture/scripts" "$fixture/demo/runtime" "$outside"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/start-demo-grafana-local.sh" "$fixture/scripts/"
  printf 'preserve\n' >"$outside/victim"
  ln -s "$outside" "$fixture/dist"
  printf '%s\n' \
    '#!/bin/bash' \
    "printf dispatched >\"$fixture/build-session-dispatched.marker\"" \
    'exit 99' \
    >"$fixture/scripts/build-demo-plugin.sh"
  chmod +x "$fixture/scripts/"*.sh

  ! ASYNCQ_DEMO_INSTALL_BUSINESS_PLUGINS=0 \
    "$fixture/scripts/start-demo-grafana-local.sh" >/dev/null 2>&1 &&
    [[ "$(<"$outside/victim")" == "preserve" &&
      ! -e "$fixture/build-session-dispatched.marker" &&
      ! -e "$fixture/demo/runtime/grafana.pid" &&
      ! -e "$fixture/demo/runtime/grafana.pid.start" ]] &&
    ! compgen -G "$fixture/demo/runtime/.plugin-build-status.*" >/dev/null
}

expect_success \
  "local launcher rejects an unsafe output root before starting its bounded build session" \
  local_launcher_rejects_unsafe_output_before_build_session

datasource_renderer_threads_validated_q_port() {
  local output="$TEST_DIR/rendered-datasource.yml"

  : >"$output"
  demo_render_loopback_datasource \
    "$ROOT_DIR/demo/grafana/provisioning/datasources/asyncq.yml" \
    "$output" \
    5432 &&
    [[ "$(grep -c '^      host: 127\.0\.0\.1$' "$output")" == 1 &&
      "$(grep -c '^      port: 5432$' "$output")" == 1 ]] &&
    ! grep -Eq '^      (host: host\.docker\.internal|port: 5000)$' "$output"
}

expect_success \
  "datasource rendering threads one validated q port onto IPv4 loopback" \
  datasource_renderer_threads_validated_q_port

expect_success "plain release version" demo_validate_release_token 13.1.1
expect_success "security release version" demo_validate_release_token 13.1.1+security-01
RELEASE_64="1.2.3+$(printf '%058d' 0 | tr '0' a)"
RELEASE_65="${RELEASE_64}a"
expect_success "64-byte release token boundary" demo_validate_release_token "$RELEASE_64"
expect_failure "65-byte release token boundary" demo_validate_release_token "$RELEASE_65"
for hostile_version in '../13.1.1' '13.1.1/../../tmp' '13.1.1 beta' $'13.1.1\nnext' '13.1' '.13.1.1' '13.1.1;id'; do
  expect_failure "hostile version $hostile_version" demo_validate_release_token "$hostile_version"
done

for hostile_pid in 0 1 01 -1 +2 99999999999 "$$" "$PPID" '2;id' $'2\n3'; do
  expect_failure "hostile PID $hostile_pid" demo_valid_signal_pid "$hostile_pid"
done
printf '%s\n' '01' >"$TEST_DIR/hostile.pid"
expect_failure "non-canonical PID file" demo_read_pid_file "$TEST_DIR/hostile.pid"
printf '%s\n' "$$" >"$TEST_DIR/current.pid"
expect_failure "current-process PID file" demo_read_pid_file "$TEST_DIR/current.pid"
expect_output \
  "proc stat parsing with spaces and closing parentheses in comm" \
  424242 \
  demo_proc_stat_start_time \
  '123 (worker ) name with spaces) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242'
expect_failure \
  "truncated proc stat parsing" \
  demo_proc_stat_start_time \
  '123 (worker) S 1 2 3'
expect_output \
  "proc stat parent parsing" \
  1 \
  demo_proc_stat_parent_pid \
  '123 (worker ) name with spaces) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242'
expect_output \
  "proc stat state parsing" \
  S \
  demo_proc_stat_state \
  '123 (worker ) name with spaces) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242'
expect_success \
  "rollback gate accepts only the invocation's newly started PID" \
  demo_should_rollback_reported_process \
  started \
  234567 \
  234567
expect_failure \
  "rollback gate preserves a pre-existing PID" \
  demo_should_rollback_reported_process \
  existing \
  234567 \
  234567
expect_failure \
  "rollback gate rejects a changed current PID" \
  demo_should_rollback_reported_process \
  started \
  234567 \
  234568

strict_process_instance_gate_is_enforced() {
  local pid
  local start=""
  local wrong_start
  local attempt
  local result=1

  setsid sleep 30 &
  pid="$!"
  for ((attempt = 0; attempt < 50; attempt++)); do
    start="$(demo_pid_start_time "$pid" 2>/dev/null || true)"
    if [[ -n "$start" ]] && demo_process_group_is_safe "$pid"; then
      break
    fi
    sleep 0.02
  done
  if [[ -z "$start" ]] ||
    ! demo_process_instance_matches "$pid" "$start" "$$"; then
    kill -KILL -- "-$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  fi

  wrong_start=$((10#$start + 1))
  if demo_stop_process_group "$pid" "$wrong_start" "$$" ||
    ! kill -0 "$pid" 2>/dev/null ||
    demo_stop_process_group "$pid" "$start" 1 ||
    ! kill -0 "$pid" 2>/dev/null; then
    kill -KILL -- "-$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  fi

  if demo_stop_process_group "$pid" "$start" "$$"; then
    result=0
  fi
  wait "$pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "signals require the same process start time and parent identity" \
  strict_process_instance_gate_is_enforced

process_table_failure_is_fail_closed() (
  local status

  demo_capture_process_table() {
    return 1
  }
  if demo_process_group_has_live_members 234567; then
    return 1
  else
    status=$?
  fi
  [[ "$status" == "2" ]]
)

expect_success \
  "process-table failure is not interpreted as an empty process group" \
  process_table_failure_is_fail_closed

process_table_failure_prevents_initial_signal() (
  local pid
  local start

  setsid sleep 30 &
  pid="$!"
  start="$(demo_pid_start_time "$pid")" || return 1
  demo_capture_process_table() {
    return 1
  }
  if demo_stop_process_group "$pid" "$start" "$$" || ! kill -0 "$pid" 2>/dev/null; then
    kill -KILL -- "-$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  fi
  kill -KILL -- "-$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
)

expect_success \
  "failed group inspection prevents TERM" \
  process_table_failure_prevents_initial_signal

leader_exit_with_resistant_member_is_bounded() {
  local leader_pid
  local leader_start
  local child_pid=""
  local attempt

  CHILD_PID_FILE="$TEST_DIR/group-child.pid" \
    setsid bash -c '
      trap "exit 0" TERM
      node -e "const fs=require(\"fs\"); process.on(\"SIGTERM\",()=>{}); fs.writeFileSync(process.env.CHILD_PID_FILE,String(process.pid)); setInterval(()=>{},1000)" &
      wait
    ' &
  leader_pid="$!"
  leader_start="$(demo_pid_start_time "$leader_pid")" || return 1
  for ((attempt = 0; attempt < 50; attempt++)); do
    if [[ -s "$TEST_DIR/group-child.pid" ]]; then
      child_pid="$(<"$TEST_DIR/group-child.pid")"
      break
    fi
    sleep 0.02
  done
  if ! demo_valid_signal_pid "$child_pid"; then
    kill -KILL -- "-$leader_pid" 2>/dev/null || true
    wait "$leader_pid" 2>/dev/null || true
    return 1
  fi
  if ! demo_stop_process_group "$leader_pid" "$leader_start" "$$"; then
    kill -KILL -- "-$leader_pid" 2>/dev/null || true
    wait "$leader_pid" 2>/dev/null || true
    return 1
  fi
  wait "$leader_pid" 2>/dev/null || true
  ! kill -0 "$child_pid" 2>/dev/null
}

expect_success \
  "TERM-resistant verified group members are killed after their leader exits" \
  leader_exit_with_resistant_member_is_bounded

printf 'known bytes\n' >"$TEST_DIR/checksum-input"
expect_success \
  "matching checksum" \
  demo_verify_sha256 \
  "$TEST_DIR/checksum-input" \
  "$(demo_file_sha256 "$TEST_DIR/checksum-input")"
expect_failure \
  "checksum mismatch" \
  demo_verify_sha256 \
  "$TEST_DIR/checksum-input" \
  aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

expect_output \
  "explicit pinned amd64 uses the exact official artifact" \
  $'e47443214da0de041ffb29633d0977ce31ba7c8c569f09974ef5294a8ce32f08\thttps://dl.grafana.com/grafana/release/13.1.1/grafana_13.1.1_29761037902_linux_amd64.tar.gz' \
  demo_resolve_grafana_artifact \
  13.1.1 \
  amd64
expect_failure \
  "pinned artifact rejects a caller-supplied wrong checksum" \
  demo_resolve_grafana_artifact \
  13.1.1 \
  amd64 \
  aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
expect_failure \
  "custom Grafana versions require a caller checksum" \
  demo_resolve_grafana_artifact \
  13.2.0 \
  amd64

mkdir -p "$TEST_DIR/path-parent" "$TEST_DIR/outside"
chmod 755 "$TEST_DIR/path-parent"
expect_success \
  "secure existing runtime directory permissions" \
  demo_secure_directory \
  "$TEST_DIR/path-parent" \
  "test directory"
expect_output \
  "secured runtime directory mode" \
  700 \
  stat -c '%a' "$TEST_DIR/path-parent"
ln -s "$TEST_DIR/outside" "$TEST_DIR/path-parent/symlink-dir"
expect_failure \
  "symlinked runtime child directory" \
  demo_ensure_child_directory \
  "$TEST_DIR/path-parent" \
  "$TEST_DIR/path-parent/symlink-dir" \
  "test directory"
ln -s "$TEST_DIR/outside/log" "$TEST_DIR/path-parent/symlink-log"
expect_failure \
  "symlinked runtime output" \
  demo_require_regular_output \
  "$TEST_DIR/path-parent" \
  "$TEST_DIR/path-parent/symlink-log" \
  "test output"
printf 'shared\n' >"$TEST_DIR/outside/hardlinked-log"
ln "$TEST_DIR/outside/hardlinked-log" "$TEST_DIR/path-parent/hardlinked-log"
expect_failure \
  "hard-linked runtime output" \
  demo_require_regular_output \
  "$TEST_DIR/path-parent" \
  "$TEST_DIR/path-parent/hardlinked-log" \
  "test output"

private_temp_cleanup_rejects_traversal() {
  local runtime="$TEST_DIR/temp-runtime"
  local victim="$TEST_DIR/temp-victim"
  local valid_work="$runtime/.grafana-install.Ab12Cd"
  local valid_q="$runtime/.q-session.Zx98Yw"

  mkdir -p "$runtime" "$victim" "$valid_work" "$valid_q"
  if demo_safe_remove_install_workdir \
    "$runtime" \
    "$runtime/.grafana-install.Ab12Cd/../../temp-victim" >/dev/null 2>&1 ||
    [[ ! -d "$victim" ]] ||
    demo_safe_remove_q_session_directory \
      "$runtime" \
      "$runtime/.q-session.Zx98Yw/../../temp-victim" >/dev/null 2>&1 ||
    [[ ! -d "$victim" ]]; then
    return 1
  fi
  demo_safe_remove_install_workdir "$runtime" "$valid_work" &&
    demo_safe_remove_q_session_directory "$runtime" "$valid_q" &&
    [[ ! -e "$valid_work" && ! -e "$valid_q" ]]
}

expect_success \
  "private temporary cleanup requires an exact generated immediate child" \
  private_temp_cleanup_rejects_traversal

install_recovery_survives_double_move_failure() (
  local runtime="$TEST_DIR/replacement-runtime"
  local install="$runtime/grafana-13.1.1"
  local candidate="$TEST_DIR/replacement-candidate"
  local -a recoveries=()

  mkdir -p "$install" "$candidate"
  printf 'previous\n' >"$install/marker"
  printf 'candidate\n' >"$candidate/marker"
  demo_move_path() {
    if [[ "$1" == "$install" ]]; then
      command mv -- "$1" "$2"
    else
      return 1
    fi
  }
  if demo_replace_install_directory "$runtime" "$install" "$candidate" >/dev/null 2>&1; then
    return 1
  fi
  recoveries=("$runtime"/.grafana-recovery.*)
  ((${#recoveries[@]} == 1)) &&
    [[ -d "${recoveries[0]}" && "$(<"${recoveries[0]}/marker")" == "previous" ]] &&
    [[ -d "$candidate" && "$(<"$candidate/marker")" == "candidate" ]] &&
    [[ ! -e "$install" ]]
)

expect_success \
  "failed candidate install and restore preserve the prior install in recovery" \
  install_recovery_survives_double_move_failure

install_replacement_removes_recovery_only_after_success() (
  local runtime="$TEST_DIR/replacement-success-runtime"
  local install="$runtime/grafana-13.1.1"
  local candidate="$TEST_DIR/replacement-success-candidate"

  mkdir -p "$install" "$candidate"
  printf 'previous\n' >"$install/marker"
  printf 'candidate\n' >"$candidate/marker"
  demo_replace_install_directory "$runtime" "$install" "$candidate" &&
    [[ "$(<"$install/marker")" == "candidate" ]] &&
    ! compgen -G "$runtime/.grafana-recovery.*" >/dev/null
)

expect_success \
  "successful install replacement removes the preserved previous install afterward" \
  install_replacement_removes_recovery_only_after_success

dashboard_config_escapes_hostile_root() {
  local parent="$TEST_DIR/dashboard-config"
  local output="$parent/dashboards.yml"
  local hostile_root='/tmp/AsyncQ root:#"quoted'
  local before

  mkdir -p "$parent"
  demo_write_dashboard_provider_config "$parent" "$output" "$hostile_root" || return 1
  node - "$output" "$hostile_root/demo/grafana/provisioning/dashboards/json" <<'NODE' || return 1
const fs = require('fs');
const yaml = require('yaml');
const [path, expected] = process.argv.slice(2);
const parsed = yaml.parse(fs.readFileSync(path, 'utf8'));
if (parsed.providers[0].options.path !== expected) process.exit(1);
NODE
  before="$(demo_file_sha256 "$output")"
  if demo_write_dashboard_provider_config "$parent" "$output" $'/tmp/control\npath'; then
    return 1
  fi
  [[ "$(demo_file_sha256 "$output")" == "$before" ]]
}

expect_success \
  "dashboard provisioning is valid escaped YAML and rejects control paths atomically" \
  dashboard_config_escapes_hostile_root

pid_state_hardlinks_fail_closed() {
  local state_dir="$TEST_DIR/pid-state"
  local outside_pid="$TEST_DIR/outside/shared.pid"
  local pid_file="$state_dir/demo.pid"
  local start_file="$pid_file.start"
  local pid
  local start

  mkdir -p "$state_dir"
  sleep 30 &
  pid="$!"
  start="$(demo_pid_start_time "$pid")" || {
    kill -TERM "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  }

  if ! demo_write_pid_state "$pid_file" "$start_file" "$pid" ||
    [[ "$(demo_read_pid_file "$pid_file")" != "$pid" ]] ||
    [[ "$(demo_read_start_file "$start_file")" != "$start" ]] ||
    ! demo_clear_pid_state "$pid_file" "$start_file"; then
    kill -TERM "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  fi

  printf '%s\n' "$pid" >"$outside_pid"
  ln "$outside_pid" "$pid_file"
  printf '%s\n' "$start" >"$start_file"
  if demo_read_pid_file "$pid_file" >/dev/null 2>&1 ||
    demo_clear_pid_state "$pid_file" "$start_file" >/dev/null 2>&1 ||
    demo_write_pid_state "$pid_file" "$start_file" "$pid" >/dev/null 2>&1 ||
    [[ ! -e "$pid_file" || ! -e "$start_file" ]] ||
    [[ "$(<"$outside_pid")" != "$pid" ]]; then
    kill -TERM "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  fi

  rm -- "$pid_file" "$start_file"
  ln "$outside_pid" "$start_file"
  if demo_read_start_file "$start_file" >/dev/null 2>&1; then
    kill -TERM "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  fi
  rm -- "$start_file"
  ln -s "$outside_pid" "$pid_file"
  if demo_clear_pid_state "$pid_file" "$start_file" >/dev/null 2>&1 ||
    [[ ! -L "$pid_file" ]]; then
    kill -TERM "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  fi

  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

expect_success \
  "PID state rejects hardlinks and symlinks without partial removal or overwrite" \
  pid_state_hardlinks_fail_closed

mkdir -p "$TEST_DIR/plugin"
printf '%s\n' \
  '{"id":"volkovlabs-table-panel","info":{"version":"3.6.5"}}' \
  >"$TEST_DIR/plugin/plugin.json"
expect_success \
  "structural plugin manifest identity" \
  demo_plugin_manifest_matches \
  "$TEST_DIR/plugin/plugin.json" \
  volkovlabs-table-panel \
  3.6.5
expect_failure \
  "structural plugin manifest version mismatch" \
  demo_plugin_manifest_matches \
  "$TEST_DIR/plugin/plugin.json" \
  volkovlabs-table-panel \
  9.9.9
ln -s "$TEST_DIR/plugin/plugin.json" "$TEST_DIR/plugin/symlink-plugin.json"
expect_failure \
  "symlinked plugin manifest" \
  demo_plugin_manifest_matches \
  "$TEST_DIR/plugin/symlink-plugin.json" \
  volkovlabs-table-panel \
  3.6.5
truncate -s 1048577 "$TEST_DIR/plugin/oversized-plugin.json"
expect_failure \
  "oversized plugin manifest is rejected before Node reads it" \
  demo_plugin_manifest_matches \
  "$TEST_DIR/plugin/oversized-plugin.json" \
  volkovlabs-table-panel \
  3.6.5

node_inherited_startup_hooks_are_cleared() {
  local fixture="$TEST_DIR/node-startup-hooks"
  local payload="$fixture/payload.js"
  local marker="$fixture/executed.marker"

  mkdir -p "$fixture/node-path"
  printf '%s\n' \
    'require("fs").writeFileSync(process.env.ASYNCQ_NODE_MARKER, "executed\\n");' \
    >"$payload"
  ASYNCQ_NODE_MARKER="$marker" \
    NODE_OPTIONS="--require=$payload" \
    NODE_PATH="$fixture/node-path" \
    demo_node -e '
      if (process.env.NODE_OPTIONS !== undefined) process.exit(1);
      if (process.env.NODE_PATH !== undefined) process.exit(1);
    ' &&
    [[ ! -e "$marker" ]]
}

expect_success \
  "production Node helper clears inherited NODE_OPTIONS and NODE_PATH before startup" \
  node_inherited_startup_hooks_are_cleared

private_npm_configs_fail_closed_and_survive_repeated_preparation() {
  local fixture="$TEST_DIR/private-npm-configs"
  local private_home="$fixture/home"
  local private_tmp="$fixture/tmp"
  local victim="$fixture/victim"
  local user_config="$private_home/.npmrc-user"
  local global_config="$private_home/.npmrc-global"
  local -a build_environment=()

  mkdir -p "$private_home" "$private_tmp"
  chmod 700 "$private_home" "$private_tmp"
  printf 'preserve\n' >"$victim"

  ln -s "$victim" "$user_config"
  if demo_prepare_clean_build_environment \
    build_environment \
    "$private_home" \
    "$private_tmp" >/dev/null 2>&1; then
    return 1
  fi
  [[ "$(<"$victim")" == "preserve" && -L "$user_config" ]] || return 1
  rm -- "$user_config"

  ln "$victim" "$global_config"
  if demo_prepare_clean_build_environment \
    build_environment \
    "$private_home" \
    "$private_tmp" >/dev/null 2>&1; then
    return 1
  fi
  [[ "$(<"$victim")" == "preserve" &&
    "$(stat -c '%h' -- "$victim")" == 2 ]] || return 1
  rm -- "$global_config"

  demo_prepare_clean_build_environment \
    build_environment \
    "$private_home" \
    "$private_tmp" || return 1
  printf 'poisoned\n' >"$user_config"
  printf 'poisoned\n' >"$global_config"
  demo_prepare_clean_build_environment \
    build_environment \
    "$private_home" \
    "$private_tmp" || return 1
  demo_prepare_clean_build_environment \
    build_environment \
    "$private_home" \
    "$private_tmp" || return 1

  "${build_environment[@]}" bash -c '
    user="$HOME/.npmrc-user"
    global="$HOME/.npmrc-global"
    [[ "$NPM_CONFIG_USERCONFIG" == "$user" &&
      "$NPM_CONFIG_GLOBALCONFIG" == "$global" &&
      "$user" != "$global" ]] || exit 1
    for config in "$user" "$global"; do
      [[ -f "$config" && ! -L "$config" &&
        "$(stat -c "%h:%a:%s" -- "$config")" == "1:600:0" ]] || exit 1
    done
    [[ "$(stat -c "%d:%i" -- "$user")" != "$(stat -c "%d:%i" -- "$global")" ]]
  '
}

expect_success \
  "clean build preparation rejects linked npm configs and atomically recreates distinct empty 0600 configs on repeats" \
  private_npm_configs_fail_closed_and_survive_repeated_preparation

real_npm_accepts_exact_clean_build_environment() {
  local fixture="$TEST_DIR/real-npm-clean-build"
  local -a build_environment=()
  local version

  command -v npm >/dev/null 2>&1 || return 0
  mkdir -p "$fixture/home" "$fixture/tmp"
  chmod 700 "$fixture/home" "$fixture/tmp"
  demo_prepare_clean_build_environment \
    build_environment \
    "$fixture/home" \
    "$fixture/tmp" || return 1
  version="$("${build_environment[@]}" npm --version)" || return 1
  [[ "$version" =~ ^[0-9]+(\.[0-9]+){1,3}([+-][0-9A-Za-z.-]+)?$ ]] || return 1
  demo_prepare_clean_build_environment \
    build_environment \
    "$fixture/home" \
    "$fixture/tmp" || return 1
  [[ "$("${build_environment[@]}" npm --version)" == "$version" ]]
}

expect_success \
  "real npm starts twice under the exact isolated BUILD_ENV without config aliasing" \
  real_npm_accepts_exact_clean_build_environment

mkdir -p "$TEST_DIR/safe/grafana-13.1.1/bin" "$TEST_DIR/archive-work-safe"
printf '#!/bin/sh\n' >"$TEST_DIR/safe/grafana-13.1.1/bin/grafana"
chmod +x "$TEST_DIR/safe/grafana-13.1.1/bin/grafana"
tar -czf "$TEST_DIR/safe.tar.gz" -C "$TEST_DIR/safe" grafana-13.1.1
expect_success \
  "safe single-root archive" \
  demo_validate_grafana_archive \
  "$TEST_DIR/safe.tar.gz" \
  grafana-13.1.1 \
  "$TEST_DIR/archive-work-safe"

tar_inherited_options_cannot_execute_during_listing_or_extraction() {
  local fixture="$TEST_DIR/tar-startup-hooks"
  local source="$fixture/source"
  local work="$fixture/work"
  local archive="$fixture/grafana.tar.gz"
  local payload="$fixture/payload"
  local marker="$fixture/executed.marker"
  local candidate

  mkdir -p "$source/grafana-13.1.1/bin" "$work"
  printf '%s\n' '#!/usr/bin/env bash' "printf executed >\"$marker\"" >"$payload"
  chmod +x "$payload"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' \
    >"$source/grafana-13.1.1/bin/grafana"
  chmod +x "$source/grafana-13.1.1/bin/grafana"
  env -u TAR_OPTIONS -u GZIP \
    tar -czf "$archive" -C "$source" grafana-13.1.1

  candidate="$(
    TAR_OPTIONS="--checkpoint=1 --checkpoint-action=exec=$payload" \
      GZIP=--verbose \
      demo_extract_verified_grafana_archive \
      "$archive" \
      grafana-13.1.1 \
      "$work"
  )" &&
    [[ -x "$candidate/bin/grafana" && ! -e "$marker" ]]
}

expect_success \
  "archive listing and extraction clear executable TAR_OPTIONS and inherited gzip options" \
  tar_inherited_options_cannot_execute_during_listing_or_extraction

grafana_install_state_detects_tamper_and_rejects_link_attacks() {
  local fixture="$TEST_DIR/grafana-install-state"
  local install="$fixture/grafana-13.2.0"
  local state="$install/.asyncq-demo-install-state"
  local victim="$fixture/victim"
  local archive_sha
  local tree_sha

  mkdir -p "$install/bin" "$install/conf"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$install/bin/grafana"
  chmod +x "$install/bin/grafana"
  printf 'original\n' >"$install/conf/defaults.ini"
  archive_sha="$(printf 'archive\n' | sha256sum | awk '{print $1}')"
  tree_sha="$(demo_write_grafana_install_state "$install" "$archive_sha")" ||
    return 1
  [[ "$(demo_validate_grafana_install_state "$install" "$archive_sha")" == "$tree_sha" ]] ||
    return 1
  printf 'tampered\n' >"$install/conf/defaults.ini"
  if demo_validate_grafana_install_state \
    "$install" \
    "$archive_sha" >/dev/null 2>&1; then
    return 1
  fi
  printf 'original\n' >"$install/conf/defaults.ini"
  [[ "$(demo_validate_grafana_install_state "$install" "$archive_sha")" == "$tree_sha" ]] ||
    return 1

  printf 'preserve\n' >"$victim"
  rm -- "$state"
  ln -s "$victim" "$state"
  if demo_read_grafana_install_state \
    "$install" \
    "$archive_sha" >/dev/null 2>&1 ||
    demo_write_grafana_install_state \
      "$install" \
      "$archive_sha" >/dev/null 2>&1; then
    return 1
  fi
  [[ "$(<"$victim")" == "preserve" && -L "$state" ]] || return 1
  rm -- "$state"
  ln "$victim" "$state"
  if demo_read_grafana_install_state \
    "$install" \
    "$archive_sha" >/dev/null 2>&1 ||
    demo_write_grafana_install_state \
      "$install" \
      "$archive_sha" >/dev/null 2>&1; then
    return 1
  fi
  [[ "$(<"$victim")" == "preserve" &&
    "$(stat -c '%h' -- "$victim")" == 2 ]] || return 1
  rm -- "$state"
}

expect_success \
  "private Grafana install state detects content tamper and rejects symlink/hardlink replacement without clobbering victims" \
  grafana_install_state_detects_tamper_and_rejects_link_attacks

ln "$TEST_DIR/safe.tar.gz" "$TEST_DIR/safe-hardlinked.tar.gz"
mkdir "$TEST_DIR/archive-work-hardlinked"
expect_failure \
  "hard-linked archive" \
  demo_validate_grafana_archive \
  "$TEST_DIR/safe-hardlinked.tar.gz" \
  grafana-13.1.1 \
  "$TEST_DIR/archive-work-hardlinked"

mkdir -p "$TEST_DIR/traversal-source" "$TEST_DIR/archive-work-traversal"
printf 'escape\n' >"$TEST_DIR/traversal-source/payload"
tar \
  -czf "$TEST_DIR/traversal.tar.gz" \
  --transform='s#^payload#../escape#' \
  -C "$TEST_DIR/traversal-source" \
  payload
expect_failure \
  "archive traversal" \
  demo_validate_grafana_archive \
  "$TEST_DIR/traversal.tar.gz" \
  grafana-13.1.1 \
  "$TEST_DIR/archive-work-traversal"

mkdir -p "$TEST_DIR/link-source/grafana-13.1.1" "$TEST_DIR/archive-work-link"
ln -s ../../outside "$TEST_DIR/link-source/grafana-13.1.1/unsafe-link"
tar -czf "$TEST_DIR/link.tar.gz" -C "$TEST_DIR/link-source" grafana-13.1.1
expect_failure \
  "archive unsafe link" \
  demo_validate_grafana_archive \
  "$TEST_DIR/link.tar.gz" \
  grafana-13.1.1 \
  "$TEST_DIR/archive-work-link"

mkdir -p "$TEST_DIR/control-source/grafana-13.1.1" "$TEST_DIR/archive-work-control"
printf 'unsafe\n' >"$TEST_DIR/control-source/grafana-13.1.1/"$'line\nbreak'
tar -czf "$TEST_DIR/control.tar.gz" -C "$TEST_DIR/control-source" grafana-13.1.1
expect_failure \
  "archive newline pathname" \
  demo_validate_grafana_archive \
  "$TEST_DIR/control.tar.gz" \
  grafana-13.1.1 \
  "$TEST_DIR/archive-work-control"

printf '123456\n' >"$TEST_DIR/oversized-download"
expect_failure \
  "compressed archive byte cap" \
  demo_validate_file_size_cap \
  "$TEST_DIR/oversized-download" \
  4 \
  "test archive"

bounded_listing_capture_enforces_exact_disk_ceiling() {
  local fixture="$TEST_DIR/bounded-listing-capture"
  local exact="$fixture/exact.txt"
  local over="$fixture/over.txt"

  mkdir -p "$fixture"
  demo_capture_bounded_command_output \
    "$exact" \
    4096 \
    "fixture archive entry listing" \
    head -c 4096 /dev/zero || return 1
  [[ "$(stat -c '%s' -- "$exact")" == "4096" ]] || return 1
  if demo_capture_bounded_command_output \
    "$over" \
    4096 \
    "fixture archive entry listing" \
    head -c 4097 /dev/zero; then
    return 1
  fi
  [[ "$(stat -c '%s' -- "$over")" == "4096" ]]
}

expect_success \
  "archive listing capture accepts the exact byte boundary and rejects overflow without growing the file" \
  bounded_listing_capture_enforces_exact_disk_ceiling

grafana_archive_rejects_mocked_listing_over_cap() {
  local fixture="$TEST_DIR/archive-listing-over-cap"
  local fake_bin="$fixture/fake-bin"
  local work_dir="$fixture/work"
  local archive="$fixture/mock.tar.gz"

  mkdir -p "$fake_bin" "$work_dir"
  printf 'mock\n' >"$archive"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'head -c 67108865 /dev/zero' \
    >"$fake_bin/tar"
  chmod +x "$fake_bin/tar"

  if PATH="$fake_bin:$PATH" \
    demo_validate_grafana_archive \
    "$archive" \
    grafana-13.1.1 \
    "$work_dir"; then
    return 1
  fi
  [[ "$(stat -c '%s' -- "$work_dir/archive-entries.txt")" == "67108864" ]]
}

expect_success \
  "Grafana archive validation rejects a mocked over-cap tar listing at the disk ceiling" \
  grafana_archive_rejects_mocked_listing_over_cap

printf '%s\n' \
  '-rw-r--r-- 0/0 60 2026-01-01 00:00:00 +0000 grafana-13.1.1/a' \
  '-rw-r--r-- 0/0 60 2026-01-01 00:00:00 +0000 grafana-13.1.1/b' \
  >"$TEST_DIR/archive-metadata.txt"
expect_output \
  "archive metadata counts bounded regular members" \
  2 \
  demo_validate_archive_metadata_file \
  "$TEST_DIR/archive-metadata.txt" \
  2 \
  60 \
  120
expect_failure \
  "archive member-count cap" \
  demo_validate_archive_metadata_file \
  "$TEST_DIR/archive-metadata.txt" \
  1 \
  60 \
  120
expect_failure \
  "archive per-member byte cap" \
  demo_validate_archive_metadata_file \
  "$TEST_DIR/archive-metadata.txt" \
  2 \
  59 \
  120
expect_failure \
  "archive aggregate byte cap" \
  demo_validate_archive_metadata_file \
  "$TEST_DIR/archive-metadata.txt" \
  2 \
  60 \
  119
printf '%s\n' \
  '-rw-r--r-- 0/0 999999999999999999999999 2026-01-01 00:00:00 +0000 grafana-13.1.1/huge' \
  >"$TEST_DIR/archive-metadata-huge.txt"
expect_failure \
  "archive metadata rejects overflow-sized members before arithmetic" \
  demo_validate_archive_metadata_file \
  "$TEST_DIR/archive-metadata-huge.txt" \
  1 \
  629145600 \
  2147483648
printf '%s\n' \
  '-rw-r--r-- 0/0 1073741824 2026-01-01 00:00:00 +0000 grafana-13.1.1/a' \
  '-rw-r--r-- 0/0 1073741824 2026-01-01 00:00:00 +0000 grafana-13.1.1/b' \
  >"$TEST_DIR/archive-metadata-exact-total.txt"
expect_output \
  "archive aggregate accepts the exact configured total boundary" \
  2 \
  demo_validate_archive_metadata_file \
  "$TEST_DIR/archive-metadata-exact-total.txt" \
  2 \
  1073741824 \
  2147483648
printf '%s\n' \
  '-rw-r--r-- 0/0 1073741824 2026-01-01 00:00:00 +0000 grafana-13.1.1/a' \
  '-rw-r--r-- 0/0 1073741825 2026-01-01 00:00:00 +0000 grafana-13.1.1/b' \
  >"$TEST_DIR/archive-metadata-over-total.txt"
expect_failure \
  "archive aggregate rejects one byte beyond the total before addition" \
  demo_validate_archive_metadata_file \
  "$TEST_DIR/archive-metadata-over-total.txt" \
  2 \
  1073741825 \
  2147483648
expect_failure \
  "archive bound values above shell LONG_MAX are rejected before arithmetic" \
  demo_validate_archive_metadata_file \
  "$TEST_DIR/archive-metadata.txt" \
  2 \
  9223372036854775808 \
  9223372036854775808

mkdir -p "$TEST_DIR/q-state" "$TEST_DIR/q-home" "$TEST_DIR/q-lic"
printf '%s\n' '#!/usr/bin/env bash' \
  "printf executed >\"$TEST_DIR/q-startup-hook.marker\"" \
  >"$TEST_DIR/q-hostile-init"
cp "$TEST_DIR/q-hostile-init" "$TEST_DIR/q-home/q.q"
chmod +x "$TEST_DIR/q-hostile-init" "$TEST_DIR/q-home/q.q"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'case "${QINIT:-}" in' \
  "  $TEST_DIR/q-state/.q-session.??????/qinit.q) ;;" \
  "  *) bash \"$TEST_DIR/q-hostile-init\"; exit 91 ;;" \
  'esac' \
  '[[ -f "$QINIT" && ! -L "$QINIT" && ! -s "$QINIT" ]] || exit 92' \
  "[[ \"\${QHOME:-}\" == \"$TEST_DIR/q-home\" ]]" \
  "[[ \"\${QLIC:-}\" == \"$TEST_DIR/q-lic\" ]]" \
  'for name in QARGS QCFG QUDSPATH PYKX_QARGS PYKX_Q_EXECUTABLE KX_QINIT; do' \
  '  [[ ! -v "$name" ]] || exit 93' \
  'done' \
  "printf safe >\"$TEST_DIR/q-environment-safe.marker\"" \
  "printf '%s\\n' \"\$@\" >\"$TEST_DIR/q-argv.actual\"" \
  >"$TEST_DIR/fake-q"
chmod +x "$TEST_DIR/fake-q"
QHOME="$TEST_DIR/q-home" \
  QLIC="$TEST_DIR/q-lic" \
  QINIT="$TEST_DIR/q-hostile-init" \
  QARGS='-p 0.0.0.0:9999' \
  QCFG="$TEST_DIR/q-hostile-init" \
  QUDSPATH="$TEST_DIR" \
  PYKX_QARGS='-p 9999' \
  PYKX_Q_EXECUTABLE="$TEST_DIR/q-hostile-init" \
  KX_QINIT="$TEST_DIR/q-hostile-init" \
  bash "$ROOT_DIR/scripts/run-demo-q-session.sh" \
  "$TEST_DIR/fake-q" \
  "$ROOT_DIR/demo/q/asyncq_demo.q" \
  5000 \
  "$TEST_DIR/q-state"
printf '%s\n' \
  "$ROOT_DIR/demo/q/asyncq_demo.q" \
  -p \
  127.0.0.1:5000 \
  -T \
  30 \
  -w \
  1024 \
  -u \
  1 \
  >"$TEST_DIR/q-argv.expected"
expect_success \
  "q hardening argv binds IPv4 loopback with direct argument passing" \
  cmp -s "$TEST_DIR/q-argv.expected" "$TEST_DIR/q-argv.actual"
expect_success \
  "q receives only canonical QHOME/QLIC and a trusted empty private QINIT" \
  test -f "$TEST_DIR/q-environment-safe.marker"
expect_failure \
  "hostile q startup files and environment hooks are not executed" \
  test -e "$TEST_DIR/q-startup-hook.marker"

q_launch_paths_reject_trailing_empty_path_before_child_dispatch() {
  local fixture="$TEST_DIR/q-trailing-empty-path"
  local cwd="$fixture/cwd"
  local fake_bin="$fixture/fake-bin"
  local state="$fixture/state"

  mkdir -p \
    "$fixture/scripts" \
    "$fixture/demo/q" \
    "$cwd" \
    "$fake_bin" \
    "$state"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/run-demo-q-session.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/start-demo-q.sh" "$fixture/scripts/"
  printf 'fixture\n' >"$fixture/demo/q/asyncq_demo.q"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "printf launched >\"$fixture/q-child-launched.marker\"" \
    'if resolved="$(command -v asyncq-probe 2>/dev/null)"; then' \
    "  printf '%s\\n' \"\$resolved\" >\"$fixture/probe-resolved.marker\"" \
    '  asyncq-probe' \
    'fi' \
    'exit 99' \
    >"$fake_bin/q"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "printf executed >\"$fixture/probe-executed.marker\"" \
    >"$cwd/asyncq-probe"
  chmod +x "$fixture/scripts/"*.sh "$fake_bin/q" "$cwd/asyncq-probe"

  if (
    cd "$cwd"
    PATH="$fake_bin:/usr/bin:" \
      ASYNCQ_DEMO_Q_PORT=5124 \
      "$fixture/scripts/start-demo-q.sh"
  ) >/dev/null 2>&1; then
    return 1
  fi
  if (
    cd "$cwd"
    PATH="$fake_bin:/usr/bin:" \
      "$fixture/scripts/run-demo-q-session.sh" \
      "$fake_bin/q" \
      "$fixture/demo/q/asyncq_demo.q" \
      5124 \
      "$state"
  ) >/dev/null 2>&1; then
    return 1
  fi
  [[ ! -e "$fixture/q-child-launched.marker" &&
    ! -e "$fixture/probe-resolved.marker" &&
    ! -e "$fixture/probe-executed.marker" &&
    ! -e "$fixture/demo/logs/q.pid" &&
    ! -e "$fixture/demo/logs/q.pid.start" ]] &&
    ! compgen -G "$state/.q-session.*" >/dev/null
}

expect_success \
  "q launcher and session runner reject trailing-empty PATH before resolving a cwd-only probe" \
  q_launch_paths_reject_trailing_empty_path_before_child_dispatch

q_launcher_cleans_runner_and_child_startup_environment() {
  local fixture="$TEST_DIR/q-launcher-clean-environment"
  local port

  mkdir -p \
    "$fixture/scripts" \
    "$fixture/demo/q" \
    "$fixture/q-home" \
    "$fixture/q-lic" \
    "$fixture/fake-bin"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/run-demo-q-session.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/start-demo-q.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/stop-demo-q.sh" "$fixture/scripts/"
  printf 'fixture\n' >"$fixture/demo/q/asyncq_demo.q"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    "fixture='$fixture'" \
    'case "${QINIT:-}" in "$fixture"/demo/logs/.q-session.??????/qinit.q) ;; *) exit 81 ;; esac' \
    '[[ -f "$QINIT" && ! -s "$QINIT" ]] || exit 82' \
    '[[ "${QHOME:-}" == "$fixture/q-home" && "${QLIC:-}" == "$fixture/q-lic" ]] || exit 83' \
    'for name in QARGS QCFG QUDSPATH PYKX_QARGS PYKX_Q_EXECUTABLE KX_QINIT BASH_ENV ENV NODE_OPTIONS NODE_PATH; do [[ ! -v "$name" ]] || exit 84; done' \
    'printf safe >"$fixture/q-child-environment-safe.marker"' \
    'exec node -e '"'"'const net=require("net"); const a=process.argv; const endpoint=a[a.indexOf("-p")+1]; const p=Number(endpoint.split(":").pop()); net.createServer(()=>{}).listen(p,"127.0.0.1");'"'"' "$@"' \
    >"$fixture/fake-bin/q"
  chmod +x "$fixture/scripts/"*.sh "$fixture/fake-bin/q"
  printf '%s\n' \
    'case "$0" in' \
    "  *run-demo-q-session.sh | */q) printf executed >\"$fixture/q-bash-hook.marker\" ;;" \
    'esac' \
    >"$fixture/bash-env-hook"
  printf '%s\n' \
    "require('fs').writeFileSync('$fixture/q-node-hook.marker', 'executed');" \
    >"$fixture/node-hook.js"
  printf 'hostile\n' >"$fixture/q-home/q.q"
  printf 'hostile\n' >"$fixture/hostile-qinit.q"
  port="$(
    demo_node -e '
      const net=require("net");
      const server=net.createServer();
      server.listen(0,"127.0.0.1",()=>{
        const port=server.address().port;
        server.close(()=>process.stdout.write(String(port)));
      });
    '
  )"
  demo_validate_port "fixture q port" "$port" >/dev/null 2>&1 || return 1

  if ! PATH="$fixture/fake-bin:$PATH" \
    ASYNCQ_DEMO_Q_PORT="$port" \
    QHOME="$fixture/q-home" \
    QLIC="$fixture/q-lic" \
    QINIT="$fixture/hostile-qinit.q" \
    QARGS='-p 0.0.0.0:9999' \
    QCFG="$fixture/hostile-qinit.q" \
    QUDSPATH="$fixture" \
    PYKX_QARGS='-p 9999' \
    BASH_ENV="$fixture/bash-env-hook" \
    ENV="$fixture/bash-env-hook" \
    NODE_OPTIONS="--require=$fixture/node-hook.js" \
    NODE_PATH="$fixture" \
    "$fixture/scripts/start-demo-q.sh" >/dev/null 2>&1; then
    return 1
  fi
  [[ -f "$fixture/q-child-environment-safe.marker" &&
    ! -e "$fixture/q-bash-hook.marker" &&
    ! -e "$fixture/q-node-hook.marker" ]] || return 1
  PATH="$fixture/fake-bin:$PATH" \
    ASYNCQ_DEMO_Q_PORT="$port" \
    "$fixture/scripts/stop-demo-q.sh" >/dev/null 2>&1
}

expect_success \
  "q launcher clears Bash/Node/q startup hooks while preserving canonical QHOME and QLIC" \
  q_launcher_cleans_runner_and_child_startup_environment

q_normalized_live_argv_is_verified() {
  local fixture="$TEST_DIR/q-normalized-argv"
  local runner_pid
  local child_pid=""
  local attempt
  local result=1

  mkdir -p "$fixture/state"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'q_script="$1"' \
    'shift' \
    'endpoint="$2"' \
    'exec node -e '"'"'const net=require("net"); const a=process.argv; const p=Number(a[a.indexOf("-p")+2]); net.createServer(()=>{}).listen(p,"127.0.0.1");'"'"' "$q_script" -p "${endpoint%:*}" "${endpoint##*:}" "${@:3}"' \
    >"$fixture/normalized-q"
  chmod +x "$fixture/normalized-q"
  bash "$ROOT_DIR/scripts/run-demo-q-session.sh" \
    "$fixture/normalized-q" \
    "$ROOT_DIR/demo/q/asyncq_demo.q" \
    5003 \
    "$fixture/state" &
  runner_pid="$!"
  for ((attempt = 0; attempt < 100; attempt++)); do
    child_pid="$(
      demo_q_runner_child_pid \
        "$runner_pid" \
        "$ROOT_DIR/demo/q/asyncq_demo.q" \
        5003 2>/dev/null || true
    )"
    if [[ -n "$child_pid" ]] &&
      demo_process_owns_ipv4_loopback_listener "$child_pid" 5003; then
      result=0
      break
    fi
    sleep 0.02
  done
  kill -TERM "$runner_pid" 2>/dev/null || true
  wait "$runner_pid" 2>/dev/null || true
  kill -KILL "$child_pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "q live-argv verification accepts q's exact host-and-port normalization" \
  q_normalized_live_argv_is_verified

runner_cleanup_is_bounded() {
  local runner_pid
  local child_pid=""
  local attempt

  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "printf '%s\\n' \"\$\$\" >\"$TEST_DIR/term-resistant.pid\"" \
    "exec node -e 'process.on(\"SIGTERM\",()=>{}); setInterval(()=>{},1000);' \"\$@\"" \
    >"$TEST_DIR/term-resistant-q"
  chmod +x "$TEST_DIR/term-resistant-q"

  bash "$ROOT_DIR/scripts/run-demo-q-session.sh" \
    "$TEST_DIR/term-resistant-q" \
    "$ROOT_DIR/demo/q/asyncq_demo.q" \
    5000 \
    "$TEST_DIR/q-state" &
  runner_pid="$!"

  for ((attempt = 0; attempt < 50; attempt++)); do
    if [[ -s "$TEST_DIR/term-resistant.pid" ]]; then
      child_pid="$(<"$TEST_DIR/term-resistant.pid")"
      break
    fi
    sleep 0.05
  done
  if ! demo_valid_signal_pid "$child_pid"; then
    kill -KILL "$runner_pid" 2>/dev/null || true
    wait "$runner_pid" 2>/dev/null || true
    return 1
  fi
  if [[ "$(
    demo_q_runner_child_pid \
      "$runner_pid" \
      "$ROOT_DIR/demo/q/asyncq_demo.q" \
      5000 2>/dev/null || true
  )" != "$child_pid" ]]; then
    kill -KILL "$runner_pid" "$child_pid" 2>/dev/null || true
    wait "$runner_pid" 2>/dev/null || true
    return 1
  fi

  kill -TERM "$runner_pid"
  for ((attempt = 0; attempt < 50; attempt++)); do
    kill -0 "$runner_pid" 2>/dev/null || break
    sleep 0.1
  done
  if kill -0 "$runner_pid" 2>/dev/null; then
    kill -KILL "$runner_pid" "$child_pid" 2>/dev/null || true
    wait "$runner_pid" 2>/dev/null || true
    return 1
  fi
  wait "$runner_pid" 2>/dev/null || true
  ! kill -0 "$child_pid" 2>/dev/null
}

expect_success \
  "q runner bounds cleanup and escalates a TERM-resistant known child" \
  runner_cleanup_is_bounded

q_runner_capture_failure_reaps_owned_child() {
  local fixture="$TEST_DIR/q-capture-failure"
  local child_pid=""

  mkdir -p "$fixture/scripts" "$fixture/state"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/run-demo-q-session.sh" "$fixture/scripts/"
  printf '%s\n' \
    '' \
    'demo_pid_start_time() {' \
    '  local attempt' \
    '  for ((attempt = 0; attempt < 100; attempt++)); do' \
    '    [[ -s "${OWNED_CHILD_FILE:-}" ]] && break' \
    '    sleep 0.01' \
    '  done' \
    '  return 1' \
    '}' \
    >>"$fixture/scripts/demo-launch-common.sh"
  printf 'fixture\n' >"$fixture/demo.q"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'trap "" TERM' \
    "printf '%s\\n' \"\$\$\" >\"$fixture/child.pid\"" \
    "exec node -e 'process.on(\"SIGTERM\",()=>{}); setInterval(()=>{},1000)' \"\$@\"" \
    >"$fixture/fake-q"
  chmod +x "$fixture/fake-q"

  if OWNED_CHILD_FILE="$fixture/child.pid" \
    bash "$fixture/scripts/run-demo-q-session.sh" \
    "$fixture/fake-q" \
    "$fixture/demo.q" \
    5000 \
    "$fixture/state"; then
    return 1
  fi
  child_pid="$(<"$fixture/child.pid")"
  demo_valid_signal_pid "$child_pid" && ! kill -0 "$child_pid" 2>/dev/null
}

expect_success \
  "q runner reaps its exact child when start-time capture fails" \
  q_runner_capture_failure_reaps_owned_child

owned_group_capture_failure_cleanup_is_bounded() {
  local leader_pid
  local child_pid=""
  local attempt

  CHILD_PID_FILE="$TEST_DIR/owned-group-child.pid" \
    setsid bash -c '
      trap "" TERM
      node -e "const fs=require(\"fs\"); process.on(\"SIGTERM\",()=>{}); fs.writeFileSync(process.env.CHILD_PID_FILE,String(process.pid)); setInterval(()=>{},1000)" &
      wait
    ' &
  leader_pid="$!"
  for ((attempt = 0; attempt < 50; attempt++)); do
    if [[ -s "$TEST_DIR/owned-group-child.pid" ]]; then
      child_pid="$(<"$TEST_DIR/owned-group-child.pid")"
      break
    fi
    sleep 0.02
  done
  if ! demo_valid_signal_pid "$child_pid"; then
    kill -KILL -- "-$leader_pid" 2>/dev/null || true
    wait "$leader_pid" 2>/dev/null || true
    return 1
  fi
  demo_terminate_owned_child "$leader_pid" 1
  ! kill -0 "$child_pid" 2>/dev/null
}

expect_success \
  "setsid launcher fallback reaps an invocation-owned group without start time" \
  owned_group_capture_failure_cleanup_is_bounded

q_stop_preserves_mismatched_live_state() {
  local fixture="$TEST_DIR/stop-mismatch-repo"
  local runner_pid
  local runner_start=""
  local attempt
  local result=1

  mkdir -p "$fixture/scripts" "$fixture/demo/logs" "$fixture/demo/q"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/run-demo-q-session.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/stop-demo-q.sh" "$fixture/scripts/"
  printf 'test fixture\n' >"$fixture/demo/q/asyncq_demo.q"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "exec node -e 'const net=require(\"net\"); const a=process.argv; const endpoint=a[a.indexOf(\"-p\")+1]; const p=Number(endpoint.split(\":\").pop()); net.createServer(()=>{}).listen(p,\"127.0.0.1\");' \"\$@\"" \
    >"$fixture/fake-q"
  chmod +x "$fixture/fake-q"

  (
    cd "$fixture"
    exec setsid bash \
      "$fixture/scripts/run-demo-q-session.sh" \
      "$fixture/fake-q" \
      "$fixture/demo/q/asyncq_demo.q" \
      5000 \
      "$fixture/demo/logs"
  ) &
  runner_pid="$!"
  for ((attempt = 0; attempt < 50; attempt++)); do
    runner_start="$(demo_pid_start_time "$runner_pid" 2>/dev/null || true)"
    if [[ -n "$runner_start" ]] &&
      demo_q_demo_process_matches \
        "$runner_pid" \
        "$fixture" \
        "$fixture/scripts/run-demo-q-session.sh" \
        "$fixture/demo/q/asyncq_demo.q"; then
      break
    fi
    sleep 0.02
  done
  if [[ -z "$runner_start" ]]; then
    kill -KILL -- "-$runner_pid" 2>/dev/null || true
    wait "$runner_pid" 2>/dev/null || true
    return 1
  fi

  printf '%s\n' "$runner_pid" >"$fixture/demo/logs/q.pid"
  printf '%s\n' "$((10#$runner_start + 1))" >"$fixture/demo/logs/q.pid.start"
  chmod 600 "$fixture/demo/logs/q.pid" "$fixture/demo/logs/q.pid.start"

  if ! "$fixture/scripts/stop-demo-q.sh" >/dev/null 2>&1 &&
    kill -0 "$runner_pid" 2>/dev/null &&
    [[ -f "$fixture/demo/logs/q.pid" && -f "$fixture/demo/logs/q.pid.start" ]]; then
    result=0
  fi

  demo_stop_process_group "$runner_pid" "$runner_start" "$$" >/dev/null 2>&1 || true
  wait "$runner_pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "q stop refuses to signal or remove state on process-instance mismatch" \
  q_stop_preserves_mismatched_live_state

q_stop_requires_recorded_start_file() {
  local fixture="$TEST_DIR/stop-missing-start-repo"
  local runner_pid
  local runner_start=""
  local attempt
  local result=1

  mkdir -p "$fixture/scripts" "$fixture/demo/logs" "$fixture/demo/q"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/run-demo-q-session.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/stop-demo-q.sh" "$fixture/scripts/"
  printf 'test fixture\n' >"$fixture/demo/q/asyncq_demo.q"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "exec node -e 'const net=require(\"net\"); const a=process.argv; const endpoint=a[a.indexOf(\"-p\")+1]; const p=Number(endpoint.split(\":\").pop()); net.createServer(()=>{}).listen(p,\"127.0.0.1\");' \"\$@\"" \
    >"$fixture/fake-q"
  chmod +x "$fixture/fake-q"

  (
    cd "$fixture"
    exec setsid bash \
      "$fixture/scripts/run-demo-q-session.sh" \
      "$fixture/fake-q" \
      "$fixture/demo/q/asyncq_demo.q" \
      5001 \
      "$fixture/demo/logs"
  ) &
  runner_pid="$!"
  for ((attempt = 0; attempt < 100; attempt++)); do
    runner_start="$(demo_pid_start_time "$runner_pid" 2>/dev/null || true)"
    if [[ -n "$runner_start" ]] &&
      demo_q_demo_process_matches \
        "$runner_pid" \
        "$fixture" \
        "$fixture/scripts/run-demo-q-session.sh" \
        "$fixture/demo/q/asyncq_demo.q" \
        5001; then
      break
    fi
    sleep 0.02
  done
  if [[ -z "$runner_start" ]]; then
    kill -KILL -- "-$runner_pid" 2>/dev/null || true
    wait "$runner_pid" 2>/dev/null || true
    return 1
  fi
  printf '%s\n' "$runner_pid" >"$fixture/demo/logs/q.pid"
  chmod 600 "$fixture/demo/logs/q.pid"

  if ! "$fixture/scripts/stop-demo-q.sh" >/dev/null 2>&1 &&
    kill -0 "$runner_pid" 2>/dev/null &&
    [[ -f "$fixture/demo/logs/q.pid" && ! -e "$fixture/demo/logs/q.pid.start" ]]; then
    result=0
  fi
  demo_stop_process_group "$runner_pid" "$runner_start" "$$" >/dev/null 2>&1 || true
  wait "$runner_pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "q stop refuses a live verified demo when recorded start state is missing" \
  q_stop_requires_recorded_start_file

q_demo_matcher_rejects_wildcard_existing_process() {
  local fixture="$TEST_DIR/q-wildcard-existing"
  local runner_pid
  local runner_start=""
  local child_pid=""
  local attempt
  local result=1

  mkdir -p "$fixture/scripts" "$fixture/demo/logs" "$fixture/demo/q"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/run-demo-q-session.sh" "$fixture/scripts/"
  printf 'test fixture\n' >"$fixture/demo/q/asyncq_demo.q"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "exec node -e 'const net=require(\"net\"); const a=process.argv; const endpoint=a[a.indexOf(\"-p\")+1]; const p=Number(endpoint.split(\":\").pop()); net.createServer(()=>{}).listen(p,\"0.0.0.0\");' \"\$@\"" \
    >"$fixture/fake-q"
  chmod +x "$fixture/fake-q"

  (
    cd "$fixture"
    exec setsid bash \
      "$fixture/scripts/run-demo-q-session.sh" \
      "$fixture/fake-q" \
      "$fixture/demo/q/asyncq_demo.q" \
      5002 \
      "$fixture/demo/logs"
  ) &
  runner_pid="$!"
  for ((attempt = 0; attempt < 100; attempt++)); do
    runner_start="$(demo_pid_start_time "$runner_pid" 2>/dev/null || true)"
    child_pid="$(
      demo_q_runner_child_pid \
        "$runner_pid" \
        "$fixture/demo/q/asyncq_demo.q" \
        5002 2>/dev/null || true
    )"
    if [[ -n "$runner_start" && -n "$child_pid" ]] &&
      demo_process_owns_listen_port "$child_pid" 5002; then
      break
    fi
    sleep 0.02
  done
  if [[ -n "$runner_start" && -n "$child_pid" ]] &&
    demo_q_runner_process_matches \
      "$runner_pid" \
      "$fixture" \
      "$fixture/scripts/run-demo-q-session.sh" \
      "$fixture/demo/q/asyncq_demo.q" \
      5002 \
      "$fixture/fake-q" &&
    demo_process_owns_listen_port "$child_pid" 5002 &&
    ! demo_q_demo_process_matches \
      "$runner_pid" \
      "$fixture" \
      "$fixture/scripts/run-demo-q-session.sh" \
      "$fixture/demo/q/asyncq_demo.q" \
      5002 \
      "$fixture/fake-q"; then
    result=0
  fi
  if [[ -n "$runner_start" ]]; then
    demo_stop_process_group "$runner_pid" "$runner_start" "$$" >/dev/null 2>&1 || true
  else
    kill -KILL -- "-$runner_pid" 2>/dev/null || true
  fi
  wait "$runner_pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "canonical existing-q verification rejects a wildcard listener despite exact loopback argv" \
  q_demo_matcher_rejects_wildcard_existing_process

listener_ownership_is_detected() {
  local listener_pid
  local recorded_pid=""
  local port=""
  local attempt
  local result=1

  # shellcheck disable=SC2016 # JavaScript template expansion, not shell expansion.
  DEMO_TEST_ENV=listener-owned node -e '
    const fs = require("fs");
    const net = require("net");
    const output = process.argv[1];
    const server = net.createServer();
    server.listen(0, "127.0.0.1", () => {
      fs.writeFileSync(output, `${process.pid} ${server.address().port}`);
    });
  ' "$TEST_DIR/listener.info" &
  listener_pid="$!"

  for ((attempt = 0; attempt < 50; attempt++)); do
    if [[ -s "$TEST_DIR/listener.info" ]]; then
      read -r recorded_pid port <"$TEST_DIR/listener.info"
      break
    fi
    sleep 0.05
  done
  if [[ "$recorded_pid" == "$listener_pid" ]] &&
    demo_process_owns_listen_port "$listener_pid" "$port" &&
    demo_process_owns_ipv4_loopback_listener "$listener_pid" "$port" &&
    demo_ipv4_loopback_port_is_reachable "$port" &&
    [[ "$(demo_process_environment_value "$listener_pid" DEMO_TEST_ENV)" == "listener-owned" ]]; then
    result=0
  fi
  kill -TERM "$listener_pid" 2>/dev/null || true
  wait "$listener_pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "listener readiness is tied to the child socket inode" \
  listener_ownership_is_detected

grafana_readiness_rejects_q_port_mismatch() (
  local listener_pid
  local recorded_pid=""
  local port=""
  local attempt
  local result=1

  demo_grafana_process_home() {
    printf '%s\n' /verified-grafana-home
  }
  demo_fetch_grafana_health() {
    return 0
  }
  demo_grafana_process_environment_is_exact() {
    [[ "$6" == 5432 ]]
  }
  GF_SERVER_HTTP_ADDR=127.0.0.1 \
    GF_SERVER_HTTP_PORT=40123 \
    ASYNCQ_DEMO_Q_PORT=5432 \
    node -e '
      const fs = require("fs");
      const net = require("net");
      const output = process.argv[1];
      const server = net.createServer();
      server.listen(0, "127.0.0.1", () => {
        fs.writeFileSync(output, `${process.pid} ${server.address().port}`);
      });
    ' "$TEST_DIR/grafana-q-port.info" &
  listener_pid="$!"
  for ((attempt = 0; attempt < 50; attempt++)); do
    if [[ -s "$TEST_DIR/grafana-q-port.info" ]]; then
      read -r recorded_pid port <"$TEST_DIR/grafana-q-port.info"
      break
    fi
    sleep 0.05
  done
  if [[ "$recorded_pid" == "$listener_pid" ]]; then
    # Match the dynamically assigned listener while preserving the real
    # environment read performed by demo_grafana_process_is_ready.
    demo_process_environment_value() {
      case "$2" in
        GF_SERVER_HTTP_ADDR)
          printf '%s\n' 127.0.0.1
          ;;
        GF_SERVER_HTTP_PORT)
          printf '%s\n' "$port"
          ;;
        ASYNCQ_DEMO_Q_PORT)
          printf '%s\n' 5432
          ;;
        *)
          return 1
          ;;
      esac
    }
    if demo_grafana_process_is_ready \
      "$listener_pid" "$port" /root /runtime /verified-grafana-home 13.1.1 "$TEST_DIR" 5432 &&
      ! demo_grafana_process_is_ready \
        "$listener_pid" "$port" /root /runtime /verified-grafana-home 13.1.1 "$TEST_DIR" 5000; then
      result=0
    fi
  fi
  kill -TERM "$listener_pid" 2>/dev/null || true
  wait "$listener_pid" 2>/dev/null || true
  return "$result"
)

expect_success \
  "local Grafana readiness records and enforces the selected q port" \
  grafana_readiness_rejects_q_port_mismatch

grafana_process_identity_requires_one_exact_trusted_config_argv() {
  local fixture="$TEST_DIR/grafana-exact-argv"
  local root="$fixture/root"
  local runtime="$root/demo/runtime"
  local home="$runtime/grafana-13.1.1"
  local config="$runtime/grafana.ini"
  local valid_pid
  local extra_pid
  local attempt
  local result=1

  mkdir -p "$home/bin"
  cp "$(command -v node)" "$home/bin/grafana"
  printf '%s\n' 'setInterval(() => {}, 1000);' >"$root/server"
  printf '%s\n' '# trusted test config' >"$config"
  chmod +x "$home/bin/grafana"
  (
    cd "$root"
    exec "$home/bin/grafana" \
      server --homepath "$home" --config "$config"
  ) &
  valid_pid="$!"
  (
    cd "$root"
    exec "$home/bin/grafana" \
      server --homepath "$home" --config "$config" \
      --config "$fixture/evil.ini"
  ) &
  extra_pid="$!"
  for ((attempt = 0; attempt < 100; attempt++)); do
    if kill -0 "$valid_pid" "$extra_pid" 2>/dev/null; then
      break
    fi
    sleep 0.01
  done
  if [[ "$(demo_grafana_process_home "$valid_pid" "$root" "$runtime")" == "$home" ]] &&
    ! demo_grafana_process_home "$extra_pid" "$root" "$runtime" >/dev/null 2>&1; then
    result=0
  fi
  kill -TERM "$valid_pid" "$extra_pid" 2>/dev/null || true
  wait "$valid_pid" "$extra_pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "Grafana identity accepts only server --homepath HOME --config TRUSTED_CONFIG with no trailing argv" \
  grafana_process_identity_requires_one_exact_trusted_config_argv

non_loopback_listener_is_rejected() {
  local listener_pid
  local recorded_pid=""
  local port=""
  local attempt
  local result=1

  node -e '
    const fs = require("fs");
    const net = require("net");
    const output = process.argv[1];
    const server = net.createServer();
    server.listen(0, "0.0.0.0", () => {
      fs.writeFileSync(output, `${process.pid} ${server.address().port}`);
    });
  ' "$TEST_DIR/wildcard-listener.info" &
  listener_pid="$!"
  for ((attempt = 0; attempt < 50; attempt++)); do
    if [[ -s "$TEST_DIR/wildcard-listener.info" ]]; then
      read -r recorded_pid port <"$TEST_DIR/wildcard-listener.info"
      break
    fi
    sleep 0.05
  done
  if [[ "$recorded_pid" == "$listener_pid" ]] &&
    demo_process_owns_listen_port "$listener_pid" "$port" &&
    ! demo_process_owns_ipv4_loopback_listener "$listener_pid" "$port"; then
    result=0
  fi
  kill -TERM "$listener_pid" 2>/dev/null || true
  wait "$listener_pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "Grafana listener verification rejects wildcard bindings" \
  non_loopback_listener_is_rejected

printf '%s\n' '{"commit":"abc","database":"ok","version":"13.1.1"}' >"$TEST_DIR/health-ok.json"
printf '%s\n' '{"database":"ok","version":"other"}' >"$TEST_DIR/health-wrong.json"
printf '%s\n' '<html>ok</html>' >"$TEST_DIR/health-html.txt"
expect_success \
  "Grafana health validation accepts the expected structural JSON" \
  demo_validate_grafana_health_json \
  "$TEST_DIR/health-ok.json" \
  13.1.1
expect_failure \
  "Grafana health validation rejects a wrong version" \
  demo_validate_grafana_health_json \
  "$TEST_DIR/health-wrong.json" \
  13.1.1
expect_failure \
  "Grafana health validation rejects arbitrary 2xx-style bodies" \
  demo_validate_grafana_health_json \
  "$TEST_DIR/health-html.txt" \
  13.1.1
truncate -s 4097 "$TEST_DIR/health-oversized.json"
expect_failure \
  "Grafana health validation rejects bodies over 4096 bytes" \
  demo_validate_grafana_health_json \
  "$TEST_DIR/health-oversized.json" \
  13.1.1

curl_user_config_is_disabled_before_any_other_option() {
  local fixture="$TEST_DIR/curl-disable"
  local curl_home="$fixture/home"
  local marker="$fixture/curl-trace.marker"
  local server_pid
  local port=""
  local attempt
  local result=1

  mkdir -p "$curl_home" "$fixture/work"
  printf 'trace = "%s"\n' "$marker" >"$curl_home/.curlrc"
  demo_node -e '
    const fs = require("fs");
    const http = require("http");
    const info = process.argv[1];
    const server = http.createServer((request, response) => {
      response.setHeader("content-type", "application/json");
      response.end(JSON.stringify({database: "ok", version: "13.1.1"}));
    });
    server.listen(0, "127.0.0.1", () => {
      fs.writeFileSync(info, String(server.address().port));
    });
  ' "$fixture/server.port" &
  server_pid="$!"
  for ((attempt = 0; attempt < 100; attempt++)); do
    if [[ -s "$fixture/server.port" ]]; then
      port="$(<"$fixture/server.port")"
      break
    fi
    sleep 0.02
  done
  if [[ -n "$port" ]] &&
    CURL_HOME="$curl_home" HOME="$curl_home" \
      demo_fetch_grafana_health "$port" 13.1.1 "$fixture/work" &&
    [[ ! -e "$marker" ]]; then
    result=0
  fi
  kill -TERM "$server_pid" 2>/dev/null || true
  wait "$server_pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "curl --disable is the immediate first option and blocks a real .curlrc trace side effect" \
  curl_user_config_is_disabled_before_any_other_option

expect_failure \
  "launcher rejects hostile version before build or download" \
  env GRAFANA_VERSION='../escape' "$ROOT_DIR/scripts/start-demo-grafana-local.sh"
expect_failure \
  "launcher rejects hostile Grafana port before build or download" \
  env GRAFANA_PORT='3000;id' "$ROOT_DIR/scripts/start-demo-grafana-local.sh"
expect_failure \
  "launcher requires a checksum for version overrides" \
  env GRAFANA_VERSION=13.0.2 "$ROOT_DIR/scripts/start-demo-grafana-local.sh"
expect_failure \
  "launcher rejects unsupported architecture before build or download" \
  env GRAFANA_ARCH='amd64/../../escape' "$ROOT_DIR/scripts/start-demo-grafana-local.sh"
expect_failure \
  "launcher rejects hostile q port before q startup" \
  env ASYNCQ_DEMO_Q_PORT='05000' "$ROOT_DIR/scripts/start-demo-q.sh"
expect_failure \
  "plugin installer rejects hostile version before filesystem access" \
  env GRAFANA_VERSION='../escape' "$ROOT_DIR/scripts/install-demo-business-plugins.sh"

plugin_installer_rejects_legacy_public_marker() {
  local fixture="$TEST_DIR/plugin-installer-trust"
  local home="$fixture/demo/runtime/grafana-13.1.1"

  mkdir -p "$fixture/scripts" "$home/bin"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/install-demo-business-plugins.sh" "$fixture/scripts/"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$home/bin/grafana"
  chmod +x "$home/bin/grafana"
  printf '%064d\n' 0 | tr '0' a >"$home/.asyncq-demo-sha256"
  ! env GRAFANA_ARCH=amd64 "$fixture/scripts/install-demo-business-plugins.sh" >/dev/null 2>&1
}

expect_success \
  "standalone plugin installer rejects the legacy public checksum marker" \
  plugin_installer_rejects_legacy_public_marker

plugin_installer_verifies_archive_and_cleans_cli_environment() {
  local fixture="$TEST_DIR/plugin-installer-clean-env"
  local source="$fixture/source"
  local runtime="$fixture/demo/runtime"
  local version=13.2.0
  local archive="$runtime/grafana-$version.linux-amd64.tar.gz"
  local install="$runtime/grafana-$version"
  local fake_bin="$fixture/fake-bin"
  local parent_start
  local sha
  local tree_sha

  mkdir -p \
    "$fixture/scripts" \
    "$source/grafana-$version/bin" \
    "$runtime" \
    "$fake_bin"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/install-demo-business-plugins.sh" "$fixture/scripts/"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    "fixture='$fixture'" \
    'if env | grep -q "^GF_" || [[ -v BASH_ENV || -v ENV || -v NODE_OPTIONS || -v NODE_PATH ]]; then' \
    '  printf unsafe >"$fixture/cli-environment-unsafe.marker"; exit 81' \
    'fi' \
    'runtime="$fixture/demo/runtime"' \
    'home="$runtime/grafana-13.2.0"' \
    'config="$runtime/grafana.ini"' \
    'plugins="$runtime/plugins"' \
    '[[ "$HOME" == "$runtime/.grafana-cli-home" && "$TMPDIR" == "$runtime/.grafana-cli-tmp" ]] || exit 82' \
    '[[ "$#" == 11 && "$1" == cli && "$2" == --homepath && "$3" == "$home" && "$4" == --config && "$5" == "$config" && "$6" == --pluginsDir && "$7" == "$plugins" && "$8" == plugins && "$9" == install ]] || exit 83' \
    'printf "%s\\0" "$@" >>"$fixture/cli-argv.nul"' \
    'mkdir -p -- "$plugins/${10}"' \
    'printf "{\"id\":\"%s\",\"info\":{\"version\":\"%s\"}}\\n" "${10}" "${11}" >"$plugins/${10}/plugin.json"' \
    >"$source/grafana-$version/bin/grafana"
  chmod +x "$source/grafana-$version/bin/grafana"
  env -u TAR_OPTIONS -u GZIP \
    tar -czf "$archive" -C "$source" "grafana-$version"
  cp -a "$source/grafana-$version" "$install"
  sha="$(demo_file_sha256 "$archive")"
  demo_write_grafana_install_state "$install" "$sha" >/dev/null
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "printf invoked >\"$fixture/tar-invoked.marker\"" \
    'exit 99' \
    >"$fake_bin/tar"
  chmod +x "$fake_bin/tar"
  printf '%s\n' \
    'case "$0" in' \
    "  */grafana) printf executed >\"$fixture/cli-bash-hook.marker\" ;;" \
    'esac' \
    >"$fixture/bash-env-hook"
  printf '%s\n' \
    "require('fs').writeFileSync('$fixture/cli-node-hook.marker', 'executed');" \
    >"$fixture/node-hook.js"

  GRAFANA_VERSION="$version" \
    GRAFANA_ARCH=amd64 \
    GRAFANA_SHA256="$sha" \
    GF_PATHS_CONFIG="$fixture/evil.ini" \
    GF_PLUGINS_PREINSTALL=attacker/plugin \
    GF_SERVER_HTTP_ADDR=0.0.0.0 \
    BASH_ENV="$fixture/bash-env-hook" \
    ENV="$fixture/bash-env-hook" \
    NODE_OPTIONS="--require=$fixture/node-hook.js" \
    NODE_PATH="$fixture" \
    PATH="$fake_bin:$PATH" \
    "$fixture/scripts/install-demo-business-plugins.sh" >/dev/null 2>&1 || return 1

  [[ ! -e "$fixture/cli-environment-unsafe.marker" &&
    ! -e "$fixture/cli-bash-hook.marker" &&
    ! -e "$fixture/cli-node-hook.marker" &&
    ! -e "$fixture/tar-invoked.marker" ]] || return 1
  demo_node - "$fixture/cli-argv.nul" "$runtime/grafana.ini" <<'NODE' || return 1
const fs = require('fs');
const [file, config] = process.argv.slice(2);
const args = fs.readFileSync(file).toString().split('\0');
args.pop();
if (args.length !== 33) process.exit(1);
for (let offset = 0; offset < args.length; offset += 11) {
  if (args[offset + 4] !== config) process.exit(1);
}
NODE
  demo_plugin_manifest_matches \
    "$runtime/plugins/volkovlabs-table-panel/plugin.json" \
    volkovlabs-table-panel 3.6.5 ||
    return 1
  demo_plugin_manifest_matches \
    "$runtime/plugins/volkovlabs-echarts-panel/plugin.json" \
    volkovlabs-echarts-panel 7.2.5 ||
    return 1
  demo_plugin_manifest_matches \
    "$runtime/plugins/volkovlabs-form-panel/plugin.json" \
    volkovlabs-form-panel 6.3.4 ||
    return 1

  tree_sha="$(demo_read_grafana_install_state "$install" "$sha")" || return 1
  parent_start="$(demo_observe_process_start_time "$$")" || return 1
  printf '%s\n' \
    '' \
    'demo_grafana_tree_fingerprint() {' \
    "  printf invoked >\"$fixture/tree-fingerprint-invoked.marker\"" \
    '  return 1' \
    '}' \
    >>"$fixture/scripts/demo-launch-common.sh"
  GRAFANA_VERSION="$version" \
    GRAFANA_ARCH=amd64 \
    GRAFANA_SHA256="$sha" \
    ASYNCQ_DEMO_VERIFIED_INSTALL_TREE_SHA256="$tree_sha" \
    ASYNCQ_DEMO_VERIFIED_INSTALL_PARENT_PID="$$" \
    ASYNCQ_DEMO_VERIFIED_INSTALL_PARENT_START="$parent_start" \
    PATH="$fake_bin:$PATH" \
    "$fixture/scripts/install-demo-business-plugins.sh" >/dev/null 2>&1 ||
    return 1
  [[ ! -e "$fixture/tree-fingerprint-invoked.marker" &&
    ! -e "$fixture/tar-invoked.marker" ]] &&
    demo_plugin_manifest_matches \
      "$runtime/plugins/volkovlabs-echarts-panel/plugin.json" \
      volkovlabs-echarts-panel 7.2.5 &&
    demo_plugin_manifest_matches \
      "$runtime/plugins/volkovlabs-form-panel/plugin.json" \
      volkovlabs-form-panel 6.3.4
}

expect_success \
  "Grafana CLI verifies standalone install state, skips archive extraction, and reuses a live parent's fresh tree verification" \
  plugin_installer_verifies_archive_and_cleans_cli_environment

grafana_stop_requires_recorded_start_file() {
  local fixture="$TEST_DIR/grafana-stop-missing-start"
  local pid
  local result=1

  mkdir -p "$fixture/scripts" "$fixture/demo/runtime"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/stop-demo-grafana-local.sh" "$fixture/scripts/"
  printf '%s\n' \
    '' \
    'demo_grafana_process_home() { printf "%s\n" "$3/grafana-13.1.1"; }' \
    'demo_process_group_is_safe() { return 0; }' \
    >>"$fixture/scripts/demo-launch-common.sh"
  sleep 30 &
  pid="$!"
  printf '%s\n' "$pid" >"$fixture/demo/runtime/grafana.pid"
  chmod 600 "$fixture/demo/runtime/grafana.pid"
  if ! "$fixture/scripts/stop-demo-grafana-local.sh" >/dev/null 2>&1 &&
    kill -0 "$pid" 2>/dev/null &&
    [[ -f "$fixture/demo/runtime/grafana.pid" &&
      ! -e "$fixture/demo/runtime/grafana.pid.start" ]]; then
    result=0
  fi
  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "Grafana stop refuses a live process when recorded start state is missing" \
  grafana_stop_requires_recorded_start_file

local_orchestration_rolls_back_on_failure() {
  local fixture="$TEST_DIR/local-rollback-failure"

  mkdir -p "$fixture/scripts" "$fixture/demo/logs"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/start-demo-local.sh" "$fixture/scripts/"
  printf '%s\n' \
    '' \
    'demo_verified_q_runner_pid() { printf "%s\n" 234567; }' \
    >>"$fixture/scripts/demo-launch-common.sh"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf "%s\n" "started 234567" >&3' \
    >"$fixture/scripts/start-demo-q.sh"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 7' >"$fixture/scripts/start-demo-grafana-local.sh"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf "stopped\n" >"$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/stopped.marker"' \
    >"$fixture/scripts/stop-demo-q.sh"
  chmod +x "$fixture/scripts/"*.sh
  if "$fixture/scripts/start-demo-local.sh" >/dev/null 2>&1; then
    return 1
  fi
  [[ -f "$fixture/stopped.marker" ]]
}

expect_success \
  "local orchestration rolls back its newly reported q process on failure" \
  local_orchestration_rolls_back_on_failure

local_orchestration_rolls_back_on_signal() {
  local fixture="$TEST_DIR/local-rollback-signal"
  local launcher_pid
  local grafana_pid=""
  local grandchild_pid=""
  local attempt
  local result=1

  mkdir -p "$fixture/scripts" "$fixture/demo/logs"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/start-demo-local.sh" "$fixture/scripts/"
  printf '%s\n' \
    '' \
    'demo_verified_q_runner_pid() { printf "%s\n" 234567; }' \
    >>"$fixture/scripts/demo-launch-common.sh"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf "%s\n" "started 234567" >&3' \
    >"$fixture/scripts/start-demo-q.sh"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"' \
    'printf "%s\n" "$$" >"$root/grafana-mock.pid"' \
    'bash -c '"'"'trap "" TERM; printf "%s\n" "$$" >"$1/grafana-grandchild.pid"; while :; do sleep 1; done'"'"' bash "$root" &' \
    'grandchild="$!"' \
    'trap "" TERM' \
    'printf "ready\n" >"$root/grafana-mock.ready"' \
    'wait "$grandchild"' \
    >"$fixture/scripts/start-demo-grafana-local.sh"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf "stopped\n" >"$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/stopped.marker"' \
    >"$fixture/scripts/stop-demo-q.sh"
  chmod +x "$fixture/scripts/"*.sh

  "$fixture/scripts/start-demo-local.sh" >/dev/null 2>&1 &
  launcher_pid="$!"
  for ((attempt = 0; attempt < 100; attempt++)); do
    [[ -f "$fixture/grafana-mock.ready" ]] && break
    sleep 0.02
  done
  kill -TERM "$launcher_pid" 2>/dev/null || true
  for ((attempt = 0; attempt < 800; attempt++)); do
    test_pid_is_live "$launcher_pid" || break
    sleep 0.02
  done
  if [[ -s "$fixture/grafana-mock.pid" ]]; then
    grafana_pid="$(<"$fixture/grafana-mock.pid")"
  fi
  if [[ -s "$fixture/grafana-grandchild.pid" ]]; then
    grandchild_pid="$(<"$fixture/grafana-grandchild.pid")"
  fi
  for ((attempt = 0; attempt < 200; attempt++)); do
    if ! test_pid_is_live "$grafana_pid" &&
      ! test_pid_is_live "$grandchild_pid"; then
      break
    fi
    sleep 0.02
  done
  if ! test_pid_is_live "$launcher_pid" &&
    ! test_pid_is_live "$grafana_pid" &&
    ! test_pid_is_live "$grandchild_pid" &&
    [[ -f "$fixture/stopped.marker" ]]; then
    result=0
  fi
  kill -KILL "$grafana_pid" "$grandchild_pid" 2>/dev/null || true
  wait "$launcher_pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "local orchestration kills a TERM-resistant Grafana launcher group and rolls back newly started q" \
  local_orchestration_rolls_back_on_signal

local_orchestration_preserves_existing_q_on_failure() {
  local fixture="$TEST_DIR/local-preserve-existing"

  mkdir -p "$fixture/scripts" "$fixture/demo/logs"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/start-demo-local.sh" "$fixture/scripts/"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf "%s\n" "existing 234567" >&3' \
    >"$fixture/scripts/start-demo-q.sh"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 7' >"$fixture/scripts/start-demo-grafana-local.sh"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf "unexpected\n" >"$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/stopped.marker"' \
    >"$fixture/scripts/stop-demo-q.sh"
  chmod +x "$fixture/scripts/"*.sh

  ! "$fixture/scripts/start-demo-local.sh" >/dev/null 2>&1 &&
    [[ ! -e "$fixture/stopped.marker" ]]
}

expect_success \
  "local orchestration preserves q reported as already running when Grafana fails" \
  local_orchestration_preserves_existing_q_on_failure

local_grafana_build_timeout_kills_resistant_group() {
  local fixture="$TEST_DIR/local-build-timeout"
  local fake_bin="$fixture/fake-bin"
  local real_timeout
  local real_timeout_quoted
  local build_pid=""
  local grandchild_pid=""
  local attempt
  local result=1

  real_timeout="$(command -v timeout)"
  printf -v real_timeout_quoted '%q' "$real_timeout"
  mkdir -p "$fixture/scripts" "$fixture/demo/runtime" "$fake_bin"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/start-demo-grafana-local.sh" "$fixture/scripts/"
  printf 'preserve me\n' >"$fixture/demo/runtime/prior.marker"
  printf '%s\n' \
    'case "$0" in' \
    "  *build-demo-plugin.sh | asyncq-plugin-build) printf executed >\"$fixture/build-hook.marker\" ;;" \
    'esac' \
    >"$fixture/bash-env-hook"
  printf '%s\n' \
    "require('fs').writeFileSync('$fixture/node-hook.marker', 'executed');" \
    >"$fixture/node-hook.js"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"' \
    'if [[ -v BASH_ENV || -v ENV || -n "${NODE_OPTIONS:-}" || -n "${NODE_PATH:-}" || -n "${NPM_CONFIG_SCRIPT_SHELL:-}" || -n "${npm_config_script_shell:-}" ]]; then' \
    '  printf unsafe >"$root/build-environment-unsafe.marker"; exit 81' \
    'fi' \
    '[[ "$HOME" == "$root/demo/runtime/.build-home" && "$TMPDIR" == "$root/demo/runtime/.build-tmp" ]] || exit 82' \
    '[[ "$GOENV" == off && -z "$GOFLAGS" && "$GOWORK" == off && "$GOTOOLCHAIN" == local ]] || exit 83' \
    '[[ "$GIT_CONFIG_GLOBAL" == /dev/null && "$GIT_CONFIG_NOSYSTEM" == 1 ]] || exit 84' \
    'npm_user="$HOME/.npmrc-user"; npm_global="$HOME/.npmrc-global"' \
    '[[ "$NPM_CONFIG_USERCONFIG" == "$npm_user" && "$NPM_CONFIG_GLOBALCONFIG" == "$npm_global" && "$npm_user" != "$npm_global" && -z "$NPM_CONFIG_NODE_OPTIONS" ]] || exit 85' \
    'for config in "$npm_user" "$npm_global"; do [[ -f "$config" && ! -L "$config" && "$(stat -c "%h:%a:%s" -- "$config")" == "1:600:0" ]] || exit 87; done' \
    '[[ "$(stat -c "%d:%i" -- "$npm_user")" != "$(stat -c "%d:%i" -- "$npm_global")" ]] || exit 88' \
    'declare -F asyncq_exported_build_hook >/dev/null && exit 86' \
    'node -e "if (process.env.NODE_OPTIONS || process.env.NODE_PATH) process.exit(1)"' \
    'printf safe >"$root/build-environment-safe.marker"' \
    'printf "%s\n" "$$" >"$root/build.pid"' \
    'bash -c '"'"'trap "" TERM; printf "%s\n" "$$" >"$1/build-grandchild.pid"; while :; do sleep 1; done'"'"' bash "$root" &' \
    'grandchild="$!"' \
    'trap "" TERM' \
    'wait "$grandchild"' \
    >"$fixture/scripts/build-demo-plugin.sh"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "real_timeout=$real_timeout_quoted" \
    'if [[ "${1:-}" == --foreground && "${2:-}" == --kill-after=10s && "${3:-}" == 10m ]]; then' \
    '  shift 3' \
    '  exec "$real_timeout" --foreground --kill-after=0.2s 0.2s "$@"' \
    'fi' \
    'exec "$real_timeout" "$@"' \
    >"$fake_bin/timeout"
  chmod +x "$fixture/scripts/"*.sh "$fake_bin/timeout"

  asyncq_exported_build_hook() {
    printf executed >"$fixture/exported-function.marker"
  }
  export -f asyncq_exported_build_hook
  if "$real_timeout" --foreground --kill-after=2s 15s \
    env \
    PATH="$fake_bin:$PATH" \
    BASH_ENV="$fixture/bash-env-hook" \
    ENV="$fixture/bash-env-hook" \
    NODE_OPTIONS="--require=$fixture/node-hook.js" \
    NODE_PATH="$fixture" \
    NPM_CONFIG_NODE_OPTIONS="--require=$fixture/node-hook.js" \
    npm_config_node_options="--require=$fixture/node-hook.js" \
    NPM_CONFIG_SCRIPT_SHELL="$fixture/bash-env-hook" \
    npm_config_script_shell="$fixture/bash-env-hook" \
    GOENV="$fixture/go.env" \
    GOFLAGS=-mod=vendor \
    GOWORK="$fixture/go.work" \
    GOTOOLCHAIN=auto \
    GIT_CONFIG_GLOBAL="$fixture/gitconfig" \
    "$fixture/scripts/start-demo-grafana-local.sh" >/dev/null 2>&1; then
    unset -f asyncq_exported_build_hook
    return 1
  fi
  unset -f asyncq_exported_build_hook
  [[ -s "$fixture/build.pid" ]] && build_pid="$(<"$fixture/build.pid")"
  [[ -s "$fixture/build-grandchild.pid" ]] &&
    grandchild_pid="$(<"$fixture/build-grandchild.pid")"
  for ((attempt = 0; attempt < 200; attempt++)); do
    if ! test_pid_is_live "$build_pid" && ! test_pid_is_live "$grandchild_pid"; then
      break
    fi
    sleep 0.02
  done
  if ! test_pid_is_live "$build_pid" &&
    ! test_pid_is_live "$grandchild_pid" &&
    [[ "$(<"$fixture/demo/runtime/prior.marker")" == "preserve me" &&
      -f "$fixture/build-environment-safe.marker" &&
      ! -e "$fixture/build-environment-unsafe.marker" &&
      ! -e "$fixture/build-hook.marker" &&
      ! -e "$fixture/node-hook.marker" &&
      ! -e "$fixture/exported-function.marker" &&
      ! -e "$fixture/demo/runtime/grafana.pid" &&
      ! -e "$fixture/demo/runtime/grafana.pid.start" ]] &&
    ! compgen -G "$fixture/demo/runtime/.plugin-build-status.*" >/dev/null; then
    result=0
  fi
  kill -KILL "$build_pid" "$grandchild_pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "local Grafana bounds plugin builds and kills TERM-resistant build descendants without replacing prior state" \
  local_grafana_build_timeout_kills_resistant_group

local_grafana_replaces_planted_cache_and_cleans_server_environment() {
  local fixture="$TEST_DIR/local-grafana-clean-server"
  local version=13.2.0
  local runtime="$fixture/demo/runtime"
  local source="$fixture/archive-source/grafana-$version"
  local archive="$runtime/grafana-$version.linux-amd64.tar.gz"
  local install="$runtime/grafana-$version"
  local fake_bin="$fixture/fake-bin"
  local real_tar
  local real_tar_quoted
  local sha
  local port
  local pid=""
  local -a launcher_environment=()

  real_tar="$(command -v tar)"
  printf -v real_tar_quoted '%q' "$real_tar"
  mkdir -p \
    "$fixture/scripts" \
    "$fixture/demo/grafana/provisioning/datasources" \
    "$fixture/demo/templates" \
    "$source/bin" \
    "$runtime" \
    "$fake_bin"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/start-demo-grafana-local.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/stop-demo-grafana-local.sh" "$fixture/scripts/"
  cp \
    "$ROOT_DIR/demo/grafana/provisioning/datasources/asyncq.yml" \
    "$fixture/demo/grafana/provisioning/datasources/"
  cp "$(command -v node)" "$source/bin/grafana"
  chmod +x "$source/bin/grafana"
  printf '%s\n' \
    'const fs = require("fs");' \
    'const http = require("http");' \
    'const path = require("path");' \
    'const root = process.cwd();' \
    'const env = Object.entries(process.env).map(([key, value]) => `${key}=${value}`).join("\0") + "\0";' \
    'fs.writeFileSync(path.join(root, "server-env.nul"), env);' \
    'const server = http.createServer((request, response) => {' \
    '  response.setHeader("content-type", "application/json");' \
    '  response.end(JSON.stringify({database: "ok", version: "13.2.0"}));' \
    '});' \
    'server.listen(Number(process.env.GF_SERVER_HTTP_PORT), process.env.GF_SERVER_HTTP_ADDR);' \
    >"$fixture/server"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    'root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"' \
    'mkdir -p "$root/dist" "$root/dist-panel/asyncq-masterdata-panel" "$root/dist-panel/asyncq-excel-report-panel"' \
    'case "$(uname -m)" in x86_64|amd64) goarch=amd64;; aarch64|arm64) goarch=arm64;; armv7l|armv7) goarch=arm;; *) exit 1;; esac' \
    'printf "%s\n" "{\"id\":\"asyncq-kdbbackend-datasource\"}" >"$root/dist/plugin.json"' \
    'printf "%s\n" backend >"$root/dist/gpx_asyncq-kdbbackend-datasource_linux_$goarch"' \
    'printf "%s\n" "{\"id\":\"asyncq-masterdata-panel\"}" >"$root/dist-panel/asyncq-masterdata-panel/plugin.json"' \
    'printf "%s\n" "{\"id\":\"asyncq-excel-report-panel\"}" >"$root/dist-panel/asyncq-excel-report-panel/plugin.json"' \
    >"$fixture/scripts/build-demo-plugin.sh"
  chmod +x "$fixture/scripts/"*.sh
  env -u TAR_OPTIONS -u GZIP \
    tar -czf "$archive" -C "$fixture/archive-source" "grafana-$version"
  sha="$(demo_file_sha256 "$archive")"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "real_tar=$real_tar_quoted" \
    "counter='$fixture/tar-extractions.log'" \
    'for argument in "$@"; do' \
    '  if [[ "$argument" == --extract ]]; then printf "extract\n" >>"$counter"; fi' \
    'done' \
    'exec "$real_tar" "$@"' \
    >"$fake_bin/tar"
  chmod +x "$fake_bin/tar"

  mkdir -p "$install/bin"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "printf executed >\"$fixture/planted-grafana-executed.marker\"" \
    >"$install/bin/grafana"
  chmod +x "$install/bin/grafana"
  printf '%s\n' "$sha" >"$install/.asyncq-demo-sha256"
  printf '%s\n' \
    "require('fs').writeFileSync('$fixture/server-node-hook.marker', 'executed');" \
    >"$fixture/node-hook.js"
  printf '%s\n' \
    'case "$0" in' \
    "  */grafana) printf executed >\"$fixture/server-bash-hook.marker\" ;;" \
    'esac' \
    >"$fixture/bash-env-hook"
  port="$(
    demo_node -e '
      const net = require("net");
      const server = net.createServer();
      server.listen(0, "127.0.0.1", () => {
        const port = server.address().port;
        server.close(() => process.stdout.write(String(port)));
      });
    '
  )"

  launcher_environment=(
    env
    "PATH=$fake_bin:$PATH"
    "GRAFANA_VERSION=$version"
    GRAFANA_ARCH=amd64
    "GRAFANA_SHA256=$sha"
    "GRAFANA_PORT=$port"
    ASYNCQ_DEMO_Q_PORT=5432
    ASYNCQ_DEMO_INSTALL_BUSINESS_PLUGINS=0
    "GF_PATHS_CONFIG=$fixture/evil.ini"
    GF_PLUGINS_PREINSTALL=attacker/plugin
    GF_FEATURE_TOGGLES_ENABLE=attacker
    GF_SERVER_HTTP_ADDR=0.0.0.0
    GF_SERVER_HTTP_PORT=9999
    "BASH_ENV=$fixture/bash-env-hook"
    "ENV=$fixture/bash-env-hook"
    "NODE_OPTIONS=--require=$fixture/node-hook.js"
    "NODE_PATH=$fixture"
  )

  if ! "${launcher_environment[@]}" \
    "$fixture/scripts/start-demo-grafana-local.sh" >/dev/null 2>&1; then
    return 1
  fi
  pid="$(demo_read_pid_file "$runtime/grafana.pid")" || {
    "$fixture/scripts/stop-demo-grafana-local.sh" >/dev/null 2>&1 || true
    return 1
  }
  if [[ "$(demo_grafana_process_home "$pid" "$fixture" "$runtime")" != "$install" ||
    -e "$fixture/planted-grafana-executed.marker" ||
    -e "$fixture/server-node-hook.marker" ||
    -e "$fixture/server-bash-hook.marker" ||
    "$(wc -l <"$fixture/tar-extractions.log")" != 1 ]] ||
    ! demo_validate_grafana_install_state "$install" "$sha" >/dev/null ||
    ! demo_node - "$fixture/server-env.nul" "$fixture" "$port" <<'NODE'
const fs = require('fs');
const [file, root, port] = process.argv.slice(2);
const entries = fs.readFileSync(file).toString().split('\0').filter(Boolean);
const env = Object.fromEntries(entries.map((entry) => {
  const separator = entry.indexOf('=');
  return [entry.slice(0, separator), entry.slice(separator + 1)];
}));
const gfKeys = Object.keys(env).filter((key) => key.startsWith('GF_')).sort();
const expectedKeys = [
  'GF_AUTH_ANONYMOUS_ENABLED',
  'GF_AUTH_ANONYMOUS_ORG_ROLE',
  'GF_DATABASE_TYPE',
  'GF_LOG_LEVEL',
  'GF_PATHS_DATA',
  'GF_PATHS_LOGS',
  'GF_PATHS_PLUGINS',
  'GF_PATHS_PROVISIONING',
  'GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS',
  'GF_SECURITY_ADMIN_PASSWORD',
  'GF_SECURITY_ADMIN_USER',
  'GF_SERVER_HTTP_ADDR',
  'GF_SERVER_HTTP_PORT',
  'GF_SERVER_ROOT_URL',
  'GF_USERS_DEFAULT_THEME',
].sort();
if (JSON.stringify(gfKeys) !== JSON.stringify(expectedKeys)) process.exit(1);
if (env.GF_SERVER_HTTP_ADDR !== '127.0.0.1') process.exit(1);
if (env.GF_SERVER_HTTP_PORT !== port) process.exit(1);
if (env.GF_SERVER_ROOT_URL !== `http://127.0.0.1:${port}`) process.exit(1);
if (env.ASYNCQ_DEMO_Q_PORT !== '5432') process.exit(1);
if (env.ASYNCQ_DEMO_TEMPLATE_DIR !== `${root}/demo/templates`) process.exit(1);
if (env.HOME !== `${root}/demo/runtime/.grafana-home`) process.exit(1);
if (env.TMPDIR !== `${root}/demo/runtime/.grafana-tmp`) process.exit(1);
if ('NODE_OPTIONS' in env || 'NODE_PATH' in env || 'BASH_ENV' in env || 'ENV' in env) {
  process.exit(1);
}
NODE
  then
    "$fixture/scripts/stop-demo-grafana-local.sh" >/dev/null 2>&1 || true
    kill -KILL "$pid" 2>/dev/null || true
    return 1
  fi
  "$fixture/scripts/stop-demo-grafana-local.sh" >/dev/null 2>&1 || return 1
  kill -KILL "$pid" 2>/dev/null || true

  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "printf executed >\"$fixture/tampered-grafana-executed.marker\"" \
    'exit 91' \
    >"$install/bin/grafana"
  chmod +x "$install/bin/grafana"
  if ! "${launcher_environment[@]}" \
    "$fixture/scripts/start-demo-grafana-local.sh" >/dev/null 2>&1; then
    return 1
  fi
  pid="$(demo_read_pid_file "$runtime/grafana.pid")" || return 1
  if [[ -e "$fixture/tampered-grafana-executed.marker" ||
    "$(wc -l <"$fixture/tar-extractions.log")" != 2 ]] ||
    ! demo_validate_grafana_install_state "$install" "$sha" >/dev/null; then
    "$fixture/scripts/stop-demo-grafana-local.sh" >/dev/null 2>&1 || true
    kill -KILL "$pid" 2>/dev/null || true
    return 1
  fi
  "$fixture/scripts/stop-demo-grafana-local.sh" >/dev/null 2>&1 || return 1
  kill -KILL "$pid" 2>/dev/null || true

  if ! "${launcher_environment[@]}" \
    "$fixture/scripts/start-demo-grafana-local.sh" >/dev/null 2>&1; then
    return 1
  fi
  pid="$(demo_read_pid_file "$runtime/grafana.pid")" || return 1
  if [[ "$(wc -l <"$fixture/tar-extractions.log")" != 2 ||
    -e "$fixture/tampered-grafana-executed.marker" ]]; then
    "$fixture/scripts/stop-demo-grafana-local.sh" >/dev/null 2>&1 || true
    kill -KILL "$pid" 2>/dev/null || true
    return 1
  fi
  "$fixture/scripts/stop-demo-grafana-local.sh" >/dev/null 2>&1
  kill -KILL "$pid" 2>/dev/null || true
}

expect_success \
  "local Grafana migrates a forged legacy marker, replaces tree tamper, and reuses a healthy cache without extraction" \
  local_grafana_replaces_planted_cache_and_cleans_server_environment

setup_docker_launcher_fixture() {
  local fixture="$1"
  local fake_bin="$fixture/fake-bin"
  local goarch

  goarch="$(demo_resolve_build_goarch)" || return 1
  mkdir -p \
    "$fixture/scripts" \
    "$fixture/demo/grafana" \
    "$fixture/dist" \
    "$fixture/dist-panel/asyncq-masterdata-panel" \
    "$fixture/dist-panel/asyncq-excel-report-panel" \
    "$fake_bin"
  cp "$ROOT_DIR/scripts/demo-launch-common.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/scripts/start-demo-grafana.sh" "$fixture/scripts/"
  cp "$ROOT_DIR/demo/docker-compose.yml" "$fixture/demo/"
  cp -R "$ROOT_DIR/demo/templates" "$fixture/demo/"
  cp -R "$ROOT_DIR/demo/grafana/provisioning" "$fixture/demo/grafana/"
  printf '%s\n' '{"id":"asyncq-kdbbackend-datasource"}' \
    >"$fixture/dist/plugin.json"
  printf '%s\n' 'datasource asset' >"$fixture/dist/module.js"
  printf '%s\n' 'backend' \
    >"$fixture/dist/gpx_asyncq-kdbbackend-datasource_linux_$goarch"
  printf '%s\n' '{"id":"asyncq-masterdata-panel"}' \
    >"$fixture/dist-panel/asyncq-masterdata-panel/plugin.json"
  printf '%s\n' 'master-data asset' \
    >"$fixture/dist-panel/asyncq-masterdata-panel/module.js"
  printf '%s\n' '{"id":"asyncq-excel-report-panel"}' \
    >"$fixture/dist-panel/asyncq-excel-report-panel/plugin.json"
  printf '%s\n' 'Excel-report asset' \
    >"$fixture/dist-panel/asyncq-excel-report-panel/module.js"
  chmod 700 \
    "$fixture/dist" \
    "$fixture/dist-panel" \
    "$fixture/dist-panel/asyncq-masterdata-panel" \
    "$fixture/dist-panel/asyncq-excel-report-panel"
  chmod 600 \
    "$fixture/dist/plugin.json" \
    "$fixture/dist/module.js" \
    "$fixture/dist/gpx_asyncq-kdbbackend-datasource_linux_$goarch" \
    "$fixture/dist-panel/asyncq-masterdata-panel/plugin.json" \
    "$fixture/dist-panel/asyncq-masterdata-panel/module.js" \
    "$fixture/dist-panel/asyncq-excel-report-panel/plugin.json" \
    "$fixture/dist-panel/asyncq-excel-report-panel/module.js"
  chmod 600 "$fixture/demo/templates/asyncq-demo-report-template.xlsx"
  chmod 600 \
    "$fixture/demo/grafana/provisioning/dashboards/json/asyncq-excel-report.json"
  printf '%s\n' \
    '' \
    'demo_ipv4_loopback_port_is_reachable() {' \
    '  [[ "${MOCK_Q_REACHABLE:-1}" == 1 && "$1" == "${MOCK_Q_PORT:-5000}" ]]' \
    '}' \
    'demo_verified_q_runner_on_port() {' \
    '  [[ "${MOCK_Q_REACHABLE:-1}" == 1 && "$6" == "${MOCK_Q_PORT:-5000}" ]] || return 1' \
    '  printf "%s\n" 234567' \
    '}' \
    >>"$fixture/scripts/demo-launch-common.sh"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"' \
    'if [[ -v BASH_ENV || -v ENV || -n "${NODE_OPTIONS:-}" || -n "${NODE_PATH:-}" || -n "${NPM_CONFIG_SCRIPT_SHELL:-}" || -n "${npm_config_script_shell:-}" ]]; then' \
    '  printf unsafe >"$root/build-environment-unsafe.marker"; exit 81' \
    'fi' \
    '[[ "$HOME" == "$root/demo/runtime/.build-home" && "$TMPDIR" == "$root/demo/runtime/.build-tmp" ]] || exit 82' \
    '[[ "$GOENV" == off && -z "$GOFLAGS" && "$GOWORK" == off && "$GOTOOLCHAIN" == local ]] || exit 83' \
    '[[ "$GIT_CONFIG_GLOBAL" == /dev/null && "$GIT_CONFIG_NOSYSTEM" == 1 ]] || exit 84' \
    'npm_user="$HOME/.npmrc-user"; npm_global="$HOME/.npmrc-global"' \
    '[[ "$NPM_CONFIG_USERCONFIG" == "$npm_user" && "$NPM_CONFIG_GLOBALCONFIG" == "$npm_global" && "$npm_user" != "$npm_global" && -z "$NPM_CONFIG_NODE_OPTIONS" ]] || exit 85' \
    'for config in "$npm_user" "$npm_global"; do [[ -f "$config" && ! -L "$config" && "$(stat -c "%h:%a:%s" -- "$config")" == "1:600:0" ]] || exit 87; done' \
    '[[ "$(stat -c "%d:%i" -- "$npm_user")" != "$(stat -c "%d:%i" -- "$npm_global")" ]] || exit 88' \
    'declare -F asyncq_exported_build_hook >/dev/null && exit 86' \
    'node -e "if (process.env.NODE_OPTIONS || process.env.NODE_PATH) process.exit(1)"' \
    'printf safe >"$root/build-environment-safe.marker"' \
    'case "$(uname -m)" in x86_64|amd64) goarch=amd64;; aarch64|arm64) goarch=arm64;; armv7l|armv7) goarch=arm;; *) exit 89;; esac' \
    'printf "%s\n" backend >"$root/dist/gpx_asyncq-kdbbackend-datasource_linux_$goarch"' \
    'printf "%s\n" datasource-asset >"$root/dist/module.js"' \
    'printf "%s\n" masterdata-asset >"$root/dist-panel/asyncq-masterdata-panel/module.js"' \
    'printf "%s\n" excel-asset >"$root/dist-panel/asyncq-excel-report-panel/module.js"' \
    'chmod 600 "$root/dist/gpx_asyncq-kdbbackend-datasource_linux_$goarch" "$root/dist/module.js" "$root/dist-panel/asyncq-masterdata-panel/module.js" "$root/dist-panel/asyncq-excel-report-panel/module.js"' \
    'printf "built\n" >"$root/build.marker"' \
    >"$fixture/scripts/build-demo-plugin.sh"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -u' \
    'mock_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"' \
    'mode=success' \
    '[[ ! -s "$mock_root/mock.mode" ]] || mode="$(<"$mock_root/mock.mode")"' \
    'service_file="$mock_root/service.id"' \
    'owned_id=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef' \
    'unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210' \
    'mismatch_hash=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' \
    'image_id=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' \
    'mismatch_image_id=sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd' \
    'pinned_image=grafana/grafana:13.1.1@sha256:7cb8c64c4d57a57e734073f3cc94620adb24a0acb929bd80ba9f14017e3a975b' \
    'owned_exists="$mock_root/owned.exists"' \
    'owned_token_file="$mock_root/owned.token"' \
    'owned_q_port_file="$mock_root/owned.q-port"' \
    'default_q_port=5000' \
    '[[ ! -s "$mock_root/q.port" ]] || default_q_port="$(<"$mock_root/q.port")"' \
    'container_q_port="$default_q_port"' \
    '[[ ! -s "$mock_root/container.q-port" ]] || container_q_port="$(<"$mock_root/container.q-port")"' \
    "expected_hash_for() { printf '%s:%s' \"\$1\" \"\$2\" | sha256sum | awk '{print \$1}'; }" \
    'record_args() { printf "%s\\0" "$@" >>"$mock_root/docker.commands.nul"; }' \
    'record_args "$@"' \
    'expected_docker_host="unix://$(readlink -m /var/run/docker.sock)"' \
    'if [[ "${DOCKER_HOST:-}" != "$expected_docker_host" || "${DOCKER_CONFIG:-}" != "$mock_root/demo/runtime/docker-cli" || ! -f "$DOCKER_CONFIG/config.json" ]]; then' \
    '  printf "redirected\n" >"$mock_root/docker-env-leak.marker"' \
    '  exit 89' \
    'fi' \
    'if [[ "${1:-}" == compose ]]; then' \
    '  while IFS="=" read -r name _; do' \
    '    if [[ "$name" == COMPOSE_* && "$name" != COMPOSE_DISABLE_ENV_FILE ]]; then' \
    '      printf "leaked\n" >"$mock_root/compose-env-leak.marker"' \
    '      exit 90' \
    '    fi' \
    '  done < <(env)' \
    '  if [[ "${COMPOSE_DISABLE_ENV_FILE:-}" != 1 ]]; then' \
    '    printf "leaked\n" >"$mock_root/compose-env-leak.marker"' \
    '    exit 90' \
    '  fi' \
    '  if [[ "${2:-}" != --file || "${3:-}" != "$mock_root/demo/docker-compose.yml" || "${4:-}" != --project-directory || "${5:-}" != "$mock_root/demo" || "${6:-}" != --project-name || "${7:-}" != asyncq-demo ]]; then' \
    '    printf "unpinned\n" >"$mock_root/unpinned-compose.marker"' \
    '    exit 91' \
    '  fi' \
    '  shift 7' \
    '  if (($# == 1)) && [[ "$1" == version ]]; then exit 0; fi' \
    '  if (($# == 3)) && [[ "$1" == config && "$2" == --hash && "$3" == grafana ]]; then' \
    '      printf "%s\n" "${ASYNCQ_DEMO_COMPOSE_OWNERSHIP_TOKEN:-}" >"$mock_root/config-token.seen"' \
    '      printf "%s\n" "${ASYNCQ_DEMO_Q_PORT:-}" >"$mock_root/config-q-port.seen"' \
    '      printf "%s %s\n" grafana "$(expected_hash_for "${ASYNCQ_DEMO_COMPOSE_OWNERSHIP_TOKEN:-}" "${ASYNCQ_DEMO_Q_PORT:-}")"' \
    '      exit 0' \
    '  fi' \
    '  if (($# == 4)) && [[ "$1" == ps && "$2" == --all && "$3" == -q && "$4" == grafana ]]; then' \
    '      [[ -s "$service_file" ]] && sed -n "1p" "$service_file"' \
    '      exit 0' \
    '  fi' \
    '  if (($# == 5)) && [[ "$1" == ps && "$2" == --status && "$3" == running && "$4" == -q && "$5" == grafana ]]; then' \
    '      if [[ -s "$service_file" && "$mode" != readiness-fail && "$mode" != readiness-cleanup-fail && "$mode" != replacement-before-cleanup ]]; then' \
    '        if [[ "$(<"$service_file")" != "$owned_id" || -f "$mock_root/running.marker" ]]; then sed -n "1p" "$service_file"; fi' \
    '      fi' \
    '      exit 0' \
    '  fi' \
    '  if (($# == 5)) && [[ "$1" == up && "$2" == --no-start && "$3" == --no-recreate && "$4" == --no-deps && "$5" == grafana ]]; then' \
    '      printf "create\n" >"$mock_root/up.marker"' \
    '      case "$mode" in' \
    '        concurrent-before-create)' \
    '          printf "%s\n" "$unrelated_id" >"$service_file"' \
    '          exit 0' \
    '          ;;' \
    '        fail-create-unrelated)' \
    '          printf "%s\n" "$unrelated_id" >"$service_file"' \
    '          exit 7' \
    '          ;;' \
    '      esac' \
    '      printf "%s\n" "$owned_id" >"$service_file"' \
    '      printf "%s\n" "${ASYNCQ_DEMO_COMPOSE_OWNERSHIP_TOKEN:-}" >"$owned_token_file"' \
    '      printf "%s\n" "${ASYNCQ_DEMO_Q_PORT:-}" >"$owned_q_port_file"' \
    '      printf "owned\n" >"$owned_exists"' \
    '      case "$mode" in' \
    '        fail-up) exit 7 ;;' \
    '        block-up)' \
    '          printf "%s\n" "$$" >"$mock_root/up-worker.pid"' \
    '          printf "blocked\n" >"$mock_root/up-blocked.marker"' \
    '          trap "" TERM' \
    '          while :; do sleep 1; done' \
    '          ;;' \
    '      esac' \
    '      exit 0' \
    '  fi' \
    '  if (($# == 2)) && [[ "$1" == ps && "$2" == --all ]]; then printf "mock ps\n"; exit 0; fi' \
    '  if (($# == 5)) && [[ "$1" == logs && "$2" == --no-color && "$3" == --tail && "$4" == 80 && "$5" == grafana ]]; then printf "mock logs\n"; exit 0; fi' \
    '  exit 1' \
    'fi' \
    'case "${1:-} ${2:-}" in' \
    '  "container inspect")' \
    '    (($# == 5)) && [[ "$3" == --format ]] || exit 1' \
    '    format="$4"' \
    '    target="$5"' \
    '    if [[ -s "$owned_exists" && "$target" == "$owned_id" ]]; then' \
    '      ownership="$(<"$owned_token_file")"' \
    '      q_port="$(<"$owned_q_port_file")"' \
    '    elif [[ -s "$service_file" && "$(<"$service_file")" == "$target" ]]; then' \
    '      ownership=manual' \
    '      q_port="$container_q_port"' \
    '    else' \
    '      exit 1' \
    '    fi' \
    '    if [[ "$format" == *".Config.Env"* ]]; then' \
    '      if [[ "$mode" == environment-mismatch ]]; then' \
    '        printf "%s\n" GF_SERVER_HTTP_ADDR=0.0.0.0 GF_SERVER_HTTP_PORT=3000 "ASYNCQ_DEMO_Q_PORT=$q_port"' \
    '      else' \
    '        printf "%s\n" GF_SERVER_HTTP_ADDR=127.0.0.1 GF_SERVER_HTTP_PORT=3000 "ASYNCQ_DEMO_Q_PORT=$q_port"' \
    '      fi' \
    '    elif [[ "$format" == *"com.docker.compose.config-hash"* ]]; then' \
    '      config_hash="$(expected_hash_for "$ownership" "$q_port")"' \
    '      image_ref="$pinned_image"' \
    '      container_image_id="$image_id"' \
    '      network_mode=host' \
    '      project=asyncq-demo' \
    '      service=grafana' \
    '      [[ "$mode" == config-hash-mismatch ]] && config_hash="$mismatch_hash"' \
    '      [[ "$mode" == image-ref-mismatch ]] && image_ref=grafana/grafana:latest' \
    '      [[ "$mode" == image-id-mismatch ]] && container_image_id="$mismatch_image_id"' \
    '      [[ "$mode" == network-mode-mismatch ]] && network_mode=bridge' \
    '      [[ "$mode" == project-mismatch ]] && project=attacker' \
    '      [[ "$mode" == service-mismatch ]] && service=attacker' \
    '      printf "%s %s %s %s %s %s %s %s %s %s\n" "$target" "$ownership" "$q_port" "$config_hash" "$project" "$service" "$image_ref" "$container_image_id" "$network_mode" 424242' \
    '    else' \
    '      printf "%s %s\n" "$target" "$ownership"' \
    '    fi' \
    '    exit 0' \
    '    ;;' \
    '  "container ls")' \
    '    filter="${@: -1}"' \
    '    if [[ "$mode" == replacement-before-cleanup && "$filter" == "id=$owned_id" ]]; then' \
    '      : >"$owned_exists"' \
    '      printf "%s\n" "$unrelated_id" >"$service_file"' \
    '      printf "replaced\n" >"$mock_root/replaced.marker"' \
    '    fi' \
    '    case "$filter" in' \
    '      label=*)' \
    '        if [[ -s "$owned_exists" && -s "$owned_token_file" && "$filter" == "label=io.asyncq.demo.ownership-token=$(<"$owned_token_file")" ]]; then' \
    '          printf "%s\n" "$owned_id"' \
    '        fi' \
    '        ;;' \
    '      id=*)' \
    '        target="${filter#id=}"' \
    '        if [[ -s "$owned_exists" && "$target" == "$owned_id" ]]; then' \
    '          printf "%s\n" "$owned_id"' \
    '        elif [[ -s "$service_file" && "$(<"$service_file")" == "$target" ]]; then' \
    '          printf "%s\n" "$target"' \
    '        fi' \
    '        ;;' \
    '    esac' \
    '    exit 0' \
    '    ;;' \
    '  "image inspect")' \
    '    target="${@: -1}"' \
    '    [[ "$target" == "$pinned_image" ]] || exit 1' \
    '    printf "%s\n" "$image_id"' \
    '    exit 0' \
    '    ;;' \
    '  "container start")' \
    '    target="${3:-}"' \
    '    [[ -s "$owned_exists" && "$target" == "$owned_id" ]] || exit 1' \
    '    printf "running\n" >"$mock_root/running.marker"' \
    '    printf "%s\n" "$target"' \
    '    exit 0' \
    '    ;;' \
    '  "container rm")' \
    '    target="${4:-}"' \
    '    printf "%s\n" "$target" >"$mock_root/deleted.id"' \
    '    if [[ "$target" != "$owned_id" ]]; then' \
    '      printf "unsafe\n" >"$mock_root/unrelated-delete.marker"' \
    '      exit 10' \
    '    fi' \
    '    printf "cleanup\n" >"$mock_root/cleanup.marker"' \
    '    if [[ "$mode" == readiness-cleanup-fail ]]; then exit 8; fi' \
    '    : >"$owned_exists"' \
    '    if [[ -s "$service_file" && "$(<"$service_file")" == "$owned_id" ]]; then : >"$service_file"; fi' \
    '    exit 0' \
    '    ;;' \
    'esac' \
    'exit 1' \
    >"$fake_bin/docker"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'root="${MOCK_DOCKER_ROOT:?}"' \
    'mode=success' \
    '[[ ! -s "$root/mock.mode" ]] || mode="$(<"$root/mock.mode")"' \
    'output=""' \
    'url=""' \
    'if [[ "${1:-}" != --disable ]]; then' \
    '  printf "unsafe\n" >"$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/curl-config.marker"' \
    '  exit 88' \
    'fi' \
    'shift' \
    'while (($#)); do' \
    '  if [[ "$1" == "--output" ]]; then output="$2"; shift 2' \
    '  elif [[ "$1" == http://127.0.0.1:* ]]; then url="$1"; shift' \
    '  else shift; fi' \
    'done' \
    '[[ "${MOCK_HEALTH_FAIL:-0}" == "0" ]] || exit 9' \
    'printf "%s\n" "$url" >>"$root/grafana-api-requests.log"' \
    'case "$url" in' \
    '  */api/health)' \
    '    printf "%s\n" "{\"commit\":\"abc\",\"database\":\"ok\",\"version\":\"13.1.1\"}" >"$output"' \
    '    ;;' \
    '  */api/datasources/uid/asyncq-demo)' \
    '    case "$mode" in' \
    '      datasource-missing) printf "%s\n" "{\"message\":\"not found\"}" >"$output" ;;' \
    '      datasource-malformed) printf "%s\n" "{malformed" >"$output" ;;' \
    '      *) printf "%s\n" "{\"uid\":\"asyncq-demo\",\"type\":\"asyncq-kdbbackend-datasource\"}" >"$output" ;;' \
    '    esac' \
    '    ;;' \
    '  */api/plugins)' \
    '    case "$mode" in' \
    '      plugins-missing)' \
    '        printf "%s\n" "[{\"id\":\"asyncq-kdbbackend-datasource\",\"type\":\"datasource\"},{\"id\":\"asyncq-masterdata-panel\",\"type\":\"panel\"}]" >"$output"' \
    '        ;;' \
    '      plugins-malformed) printf "%s\n" "{\"plugins\":[]}" >"$output" ;;' \
    '      *)' \
    '        printf "%s\n" "[{\"id\":\"asyncq-kdbbackend-datasource\",\"type\":\"datasource\"},{\"id\":\"asyncq-masterdata-panel\",\"type\":\"panel\"},{\"id\":\"asyncq-excel-report-panel\",\"type\":\"panel\"}]" >"$output"' \
    '        ;;' \
    '    esac' \
    '    ;;' \
    '  *) exit 10 ;;' \
    'esac' \
    >"$fake_bin/curl"
  chmod +x "$fixture/scripts/"*.sh "$fake_bin/"*
}

docker_bound_publication_modes_are_exact() {
  local fixture="$1"
  local goarch

  goarch="$(demo_resolve_build_goarch)" || return 1
  demo_node - \
    "$fixture" \
    "$goarch" <<'NODE'
const fs = require('fs');
const path = require('path');

const [root, goarch] = process.argv.slice(2);
const backend = path.join(
  root,
  'dist',
  `gpx_asyncq-kdbbackend-datasource_linux_${goarch}`,
);
const publicTrees = [
  path.join(root, 'dist'),
  path.join(root, 'dist-panel', 'asyncq-masterdata-panel'),
  path.join(root, 'dist-panel', 'asyncq-excel-report-panel'),
  path.join(root, 'demo', 'templates'),
  path.join(root, 'demo', 'grafana', 'provisioning'),
];
const visit = (entryPath) => {
  const stat = fs.lstatSync(entryPath);
  if (stat.isSymbolicLink()) process.exit(1);
  const mode = stat.mode & 0o7777;
  if (stat.isDirectory()) {
    if (mode !== 0o755 || (mode & 0o055) !== 0o055) process.exit(1);
    for (const name of fs.readdirSync(entryPath)) visit(path.join(entryPath, name));
    return;
  }
  if (!stat.isFile() || stat.nlink !== 1) process.exit(1);
  const expected = entryPath === backend ? 0o755 : 0o644;
  if (mode !== expected || (mode & 0o044) !== 0o044) process.exit(1);
  if (entryPath === backend && (mode & 0o011) !== 0o011) process.exit(1);
};
for (const tree of publicTrees) visit(tree);

const datasource = path.join(root, 'demo', 'runtime', 'docker-datasource-5000.yml');
const datasourceStat = fs.lstatSync(datasource);
if (
  !datasourceStat.isFile() ||
  datasourceStat.isSymbolicLink() ||
  datasourceStat.nlink !== 1 ||
  (datasourceStat.mode & 0o7777) !== 0o644 ||
  (datasourceStat.mode & 0o044) !== 0o044
) {
  process.exit(1);
}

const privateDirectories = [
  'demo/runtime',
  'demo/runtime/docker-cli',
  'demo/runtime/.docker-home',
  'demo/runtime/.docker-tmp',
  'demo/runtime/.build-home',
  'demo/runtime/.build-tmp',
  'demo/runtime/.command-home',
  'demo/runtime/.command-tmp',
];
for (const relative of privateDirectories) {
  const stat = fs.lstatSync(path.join(root, relative));
  if (!stat.isDirectory() || stat.isSymbolicLink() || (stat.mode & 0o7777) !== 0o700) {
    process.exit(1);
  }
}
const privateFiles = [
  'demo/runtime/docker-cli/config.json',
  'demo/runtime/.build-home/.npmrc-user',
  'demo/runtime/.build-home/.npmrc-global',
];
for (const relative of privateFiles) {
  const stat = fs.lstatSync(path.join(root, relative));
  if (
    !stat.isFile() ||
    stat.isSymbolicLink() ||
    stat.nlink !== 1 ||
    (stat.mode & 0o7777) !== 0o600
  ) {
    process.exit(1);
  }
}
NODE
}

docker_launcher_rejects_unsafe_output_before_dispatch() {
  local fixture="$TEST_DIR/docker-unsafe-build-output"
  local outside="$fixture/outside"
  local unsafe_file

  setup_docker_launcher_fixture "$fixture"
  mkdir -p "$outside"
  printf 'preserve\n' >"$outside/victim"
  unsafe_file="$fixture/dist-panel/asyncq-masterdata-panel/module.js"
  rm -- "$unsafe_file"
  ln -s "$outside/victim" "$unsafe_file"

  ! PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$outside/victim")" == "preserve" &&
      ! -e "$fixture/docker.commands.nul" &&
      ! -e "$fixture/build.marker" &&
      ! -e "$fixture/up.marker" &&
      ! -e "$fixture/service.id" ]]
}

expect_success \
  "Docker launcher rejects unsafe nested build output before build or Docker dispatch" \
  docker_launcher_rejects_unsafe_output_before_dispatch

docker_launcher_rejects_trailing_empty_path_before_command_capture() {
  local fixture="$TEST_DIR/docker-trailing-empty-path"
  local cwd="$fixture/cwd"

  setup_docker_launcher_fixture "$fixture"
  mkdir -p "$cwd"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "printf launched >\"$fixture/build-child-launched.marker\"" \
    'if resolved="$(command -v asyncq-probe 2>/dev/null)"; then' \
    "  printf '%s\\n' \"\$resolved\" >\"$fixture/probe-resolved.marker\"" \
    '  asyncq-probe' \
    'fi' \
    'exit 99' \
    >"$fixture/scripts/build-demo-plugin.sh"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    "printf executed >\"$fixture/probe-executed.marker\"" \
    >"$cwd/asyncq-probe"
  chmod +x "$fixture/scripts/build-demo-plugin.sh" "$cwd/asyncq-probe"

  if (
    cd "$cwd"
    PATH="$fixture/fake-bin:/usr/bin:" \
      MOCK_DOCKER_ROOT="$fixture" \
      ASYNCQ_DEMO_DOCKER_READY_TIMEOUT_SECONDS=1 \
      "$fixture/scripts/start-demo-grafana.sh"
  ) >/dev/null 2>&1; then
    return 1
  fi
  [[ ! -e "$fixture/docker.commands.nul" &&
    ! -e "$fixture/build-child-launched.marker" &&
    ! -e "$fixture/probe-resolved.marker" &&
    ! -e "$fixture/probe-executed.marker" &&
    ! -e "$fixture/up.marker" &&
    ! -e "$fixture/service.id" ]] &&
    ! compgen -G "$fixture/demo/.docker-compose-output.*" >/dev/null
}

expect_success \
  "Docker launcher rejects trailing-empty PATH before command capture or cwd-only probe resolution" \
  docker_launcher_rejects_trailing_empty_path_before_command_capture

docker_launcher_mock_succeeds_only_after_verified_readiness() {
  local fixture="$TEST_DIR/docker-launcher-success"
  local owned_id=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef

  setup_docker_launcher_fixture "$fixture"
  mkdir -p "$fixture/demo/runtime"
  demo_render_loopback_datasource \
    "$fixture/demo/grafana/provisioning/datasources/asyncq.yml" \
    "$fixture/demo/runtime/docker-datasource-5000.yml" \
    5000
  chmod 600 "$fixture/demo/runtime/docker-datasource-5000.yml"
  printf '%s\n' \
    'case "$0" in' \
    "  *build-demo-plugin.sh | asyncq-owned-command) printf executed >\"$fixture/build-hook.marker\" ;;" \
    'esac' \
    >"$fixture/bash-env-hook"
  printf '%s\n' \
    "require('fs').writeFileSync('$fixture/node-hook.marker', 'executed');" \
    >"$fixture/node-hook.js"
  asyncq_exported_build_hook() {
    printf executed >"$fixture/exported-function.marker"
  }
  export -f asyncq_exported_build_hook
  PATH="$fixture/fake-bin:$PATH" \
    BASH_ENV="$fixture/bash-env-hook" \
    ENV="$fixture/bash-env-hook" \
    NODE_OPTIONS="--require=$fixture/node-hook.js" \
    NODE_PATH="$fixture" \
    NPM_CONFIG_NODE_OPTIONS="--require=$fixture/node-hook.js" \
    NPM_CONFIG_SCRIPT_SHELL="$fixture/bash-env-hook" \
    GOENV="$fixture/go.env" \
    GOFLAGS=-mod=vendor \
    GOWORK="$fixture/go.work" \
    GOTOOLCHAIN=auto \
    GIT_CONFIG_GLOBAL="$fixture/gitconfig" \
    MOCK_DOCKER_ROOT="$fixture" \
    ASYNCQ_DEMO_DOCKER_READY_TIMEOUT_SECONDS=1 \
    "$fixture/scripts/start-demo-grafana.sh" 2>/dev/null |
    grep -Fq "Grafana is ready at http://127.0.0.1:3000" &&
    unset -f asyncq_exported_build_hook &&
    [[ "$(<"$fixture/service.id")" == "$owned_id" &&
      -f "$fixture/up.marker" &&
      -f "$fixture/running.marker" &&
      -f "$fixture/build-environment-safe.marker" &&
      ! -e "$fixture/build-environment-unsafe.marker" &&
      ! -e "$fixture/build-hook.marker" &&
      ! -e "$fixture/node-hook.marker" &&
      ! -e "$fixture/exported-function.marker" &&
      "$(<"$fixture/config-token.seen")" == "$(<"$fixture/owned.token")" &&
      "$(<"$fixture/config-q-port.seen")" == 5000 &&
      "$(grep -c '^      host: 127\.0\.0\.1$' "$fixture/demo/runtime/docker-datasource-5000.yml")" == 1 &&
      "$(grep -c '^      port: 5000$' "$fixture/demo/runtime/docker-datasource-5000.yml")" == 1 &&
      ! -e "$fixture/cleanup.marker" ]] &&
    docker_bound_publication_modes_are_exact "$fixture"
}

expect_success \
  "mocked Docker launcher leaves only its verified healthy new service running" \
  docker_launcher_mock_succeeds_only_after_verified_readiness

docker_launcher_preserves_healthy_existing_service() {
  local fixture="$TEST_DIR/docker-preserve-healthy"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' "$unrelated_id" >"$fixture/service.id"
  PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      "$(<"$fixture/config-token.seen")" == manual &&
      ! -e "$fixture/build.marker" &&
      ! -e "$fixture/up.marker" &&
      ! -e "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker launcher reuses a strictly pinned service without requiring access to its host /proc PID descriptors" \
  docker_launcher_preserves_healthy_existing_service

docker_launcher_rejects_incomplete_demo_api_identity() {
  local mode="$1"
  local fixture="$TEST_DIR/docker-api-identity-$mode"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' "$unrelated_id" >"$fixture/service.id"
  printf '%s\n' "$mode" >"$fixture/mock.mode"
  ! PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      ! -e "$fixture/build.marker" &&
      ! -e "$fixture/up.marker" &&
      ! -e "$fixture/cleanup.marker" ]] &&
    grep -Fq '/api/health' "$fixture/grafana-api-requests.log" &&
    grep -Fq \
      '/api/datasources/uid/asyncq-demo' \
      "$fixture/grafana-api-requests.log" &&
    grep -Fq '/api/plugins' "$fixture/grafana-api-requests.log"
}

expect_success \
  "Docker reuse rejects a health-passing service with an absent demo datasource" \
  docker_launcher_rejects_incomplete_demo_api_identity \
  datasource-missing
expect_success \
  "Docker reuse rejects a health-passing service with malformed datasource JSON" \
  docker_launcher_rejects_incomplete_demo_api_identity \
  datasource-malformed
expect_success \
  "Docker reuse rejects a health-passing service missing a required AsyncQ plugin" \
  docker_launcher_rejects_incomplete_demo_api_identity \
  plugins-missing
expect_success \
  "Docker reuse rejects a health-passing service with malformed plugin inventory JSON" \
  docker_launcher_rejects_incomplete_demo_api_identity \
  plugins-malformed

docker_launcher_cleans_health_passing_api_incomplete_service() {
  local fixture="$TEST_DIR/docker-api-incomplete-new-service"

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' plugins-missing >"$fixture/mock.mode"
  ! PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    ASYNCQ_DEMO_DOCKER_READY_TIMEOUT_SECONDS=1 \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ ! -s "$fixture/service.id" &&
      -f "$fixture/up.marker" &&
      -f "$fixture/cleanup.marker" ]] &&
    grep -Fq '/api/health' "$fixture/grafana-api-requests.log" &&
    grep -Fq '/api/plugins' "$fixture/grafana-api-requests.log"
}

expect_success \
  "Docker startup removes its owned service when health passes but plugin registration stays incomplete" \
  docker_launcher_cleans_health_passing_api_incomplete_service

docker_launcher_preserves_unhealthy_existing_service() {
  local fixture="$TEST_DIR/docker-preserve-unhealthy"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' "$unrelated_id" >"$fixture/service.id"
  printf '%s\n' network-mode-mismatch >"$fixture/mock.mode"
  ! PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      ! -e "$fixture/build.marker" &&
      ! -e "$fixture/up.marker" &&
      ! -e "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker launcher rejects and preserves a pre-existing service outside host networking" \
  docker_launcher_preserves_unhealthy_existing_service

docker_launcher_preserves_drifted_existing_service() {
  local mode="$1"
  local fixture="$TEST_DIR/docker-drift-$mode"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' "$unrelated_id" >"$fixture/service.id"
  printf '%s\n' "$mode" >"$fixture/mock.mode"
  ! PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      ! -e "$fixture/build.marker" &&
      ! -e "$fixture/up.marker" &&
      ! -e "$fixture/deleted.id" &&
      ! -e "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker launcher rejects and preserves a pre-existing service with a drifted Compose config hash" \
  docker_launcher_preserves_drifted_existing_service \
  config-hash-mismatch
expect_success \
  "Docker launcher rejects and preserves a pre-existing service using the wrong image content" \
  docker_launcher_preserves_drifted_existing_service \
  image-id-mismatch
expect_success \
  "Docker launcher rejects and preserves a pre-existing service using a swapped image reference" \
  docker_launcher_preserves_drifted_existing_service \
  image-ref-mismatch
expect_success \
  "Docker launcher rejects and preserves a container relabeled as another Compose service" \
  docker_launcher_preserves_drifted_existing_service \
  service-mismatch
expect_success \
  "Docker launcher rejects and preserves a container relabeled into another Compose project" \
  docker_launcher_preserves_drifted_existing_service \
  project-mismatch
expect_success \
  "Docker launcher rejects and preserves a pre-existing service with a non-loopback Grafana environment" \
  docker_launcher_preserves_drifted_existing_service \
  environment-mismatch

docker_launcher_requires_loopback_q_connectivity() {
  local fixture="$TEST_DIR/docker-q-unreachable"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' "$unrelated_id" >"$fixture/service.id"
  ! PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    MOCK_Q_REACHABLE=0 \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      ! -e "$fixture/build.marker" &&
      ! -e "$fixture/up.marker" &&
      ! -e "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker host-network readiness requires loopback q connectivity" \
  docker_launcher_requires_loopback_q_connectivity

docker_launcher_threads_custom_q_port() {
  local fixture="$TEST_DIR/docker-custom-q-port"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' "$unrelated_id" >"$fixture/service.id"
  printf '%s\n' 5001 >"$fixture/q.port"
  ASYNCQ_DEMO_Q_PORT=5001 \
    MOCK_Q_PORT=5001 \
    PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      "$(<"$fixture/config-q-port.seen")" == 5001 &&
      "$(grep -c '^      host: 127\.0\.0\.1$' "$fixture/demo/runtime/docker-datasource-5001.yml")" == 1 &&
      "$(grep -c '^      port: 5001$' "$fixture/demo/runtime/docker-datasource-5001.yml")" == 1 &&
      ! -e "$fixture/build.marker" &&
      ! -e "$fixture/up.marker" &&
      ! -e "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker launcher threads a custom validated q port through exact q identity, provisioning, and Compose hash input" \
  docker_launcher_threads_custom_q_port

docker_launcher_rejects_recorded_q_port_mismatch() {
  local fixture="$TEST_DIR/docker-recorded-q-port-mismatch"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' "$unrelated_id" >"$fixture/service.id"
  ! ASYNCQ_DEMO_Q_PORT=5001 \
    MOCK_Q_PORT=5000 \
    PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      ! -e "$fixture/build.marker" &&
      ! -e "$fixture/up.marker" &&
      ! -e "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker launcher fails closed when the exact recorded q runner uses another port" \
  docker_launcher_rejects_recorded_q_port_mismatch

docker_launcher_rejects_container_q_port_mismatch() {
  local fixture="$TEST_DIR/docker-container-q-port-mismatch"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' "$unrelated_id" >"$fixture/service.id"
  printf '%s\n' 5001 >"$fixture/q.port"
  printf '%s\n' 5000 >"$fixture/container.q-port"
  ! ASYNCQ_DEMO_Q_PORT=5001 \
    MOCK_Q_PORT=5001 \
    PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      ! -e "$fixture/build.marker" &&
      ! -e "$fixture/up.marker" &&
      ! -e "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker launcher rejects and preserves a container provisioned for another q port" \
  docker_launcher_rejects_container_q_port_mismatch

docker_launcher_ignores_implicit_compose_inputs() {
  local fixture="$TEST_DIR/docker-pinned-compose-inputs"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' "$unrelated_id" >"$fixture/service.id"
  printf '%s\n' \
    'services:' \
    '  grafana:' \
    '    image: attacker.invalid/grafana:latest' \
    >"$fixture/evil-compose.yml"
  cp "$fixture/evil-compose.yml" "$fixture/demo/docker-compose.override.yml"
  printf '%s\n' 'ASYNCQ_DEMO_COMPOSE_OWNERSHIP_TOKEN=attacker' >"$fixture/demo/.env"
  COMPOSE_FILE="$fixture/evil-compose.yml" \
    COMPOSE_PROJECT_NAME=attacker \
    COMPOSE_ENV_FILES="$fixture/demo/.env" \
    COMPOSE_PATH_SEPARATOR=: \
    COMPOSE_REMOVE_ORPHANS=1 \
    COMPOSE_PROFILES=attacker \
    COMPOSE_EXPERIMENTAL_DANGER=1 \
    DOCKER_CONFIG="$fixture/evil-docker-config" \
    DOCKER_CLI_PLUGIN_EXTRA_DIRS="$fixture/evil-plugins" \
    DOCKER_TLS_VERIFY=1 \
    DOCKER_CERT_PATH="$fixture/evil-certs" \
    ASYNCQ_DEMO_COMPOSE_OWNERSHIP_TOKEN=attacker \
    PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      "$(<"$fixture/config-token.seen")" == manual &&
      ! -e "$fixture/compose-env-leak.marker" &&
      ! -e "$fixture/docker-env-leak.marker" &&
      ! -e "$fixture/unpinned-compose.marker" &&
      ! -e "$fixture/deleted.id" &&
      ! -e "$fixture/build.marker" &&
      ! -e "$fixture/up.marker" &&
      ! -e "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker launcher ignores hostile Compose environment, .env, and implicit override inputs" \
  docker_launcher_ignores_implicit_compose_inputs

docker_launcher_rejects_remote_or_context_engine_redirection() {
  local mode="$1"
  local fixture="$TEST_DIR/docker-engine-redirection-$mode"

  setup_docker_launcher_fixture "$fixture"
  case "$mode" in
    tcp)
      ! DOCKER_HOST=tcp://attacker.invalid:2376 \
        PATH="$fixture/fake-bin:$PATH" \
        "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1
      ;;
    context)
      ! DOCKER_CONTEXT=attacker \
        PATH="$fixture/fake-bin:$PATH" \
        "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1
      ;;
    *)
      return 1
      ;;
  esac &&
    [[ ! -e "$fixture/docker.commands.nul" &&
      ! -e "$fixture/build.marker" &&
      ! -e "$fixture/up.marker" ]]
}

expect_success \
  "Docker launcher rejects a remote TCP Engine before any Docker invocation" \
  docker_launcher_rejects_remote_or_context_engine_redirection \
  tcp
expect_success \
  "Docker launcher rejects inherited non-default context redirection before any Docker invocation" \
  docker_launcher_rejects_remote_or_context_engine_redirection \
  context

docker_launcher_preserves_service_appearing_before_create() {
  local fixture="$TEST_DIR/docker-concurrent-before-create"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' concurrent-before-create >"$fixture/mock.mode"
  ! PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      -f "$fixture/up.marker" &&
      ! -e "$fixture/deleted.id" &&
      ! -e "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker launcher preserves an unlabeled service that wins the race immediately before create" \
  docker_launcher_preserves_service_appearing_before_create

docker_launcher_preserves_unrelated_state_after_failed_create() {
  local fixture="$TEST_DIR/docker-failed-create-unrelated"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' fail-create-unrelated >"$fixture/mock.mode"
  ! PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      ! -e "$fixture/deleted.id" &&
      ! -e "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker launcher never claims unrelated partial state after create fails" \
  docker_launcher_preserves_unrelated_state_after_failed_create

docker_launcher_preserves_replacement_before_cleanup() {
  local fixture="$TEST_DIR/docker-replacement-before-cleanup"
  local unrelated_id=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' replacement-before-cleanup >"$fixture/mock.mode"
  ! PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    ASYNCQ_DEMO_DOCKER_READY_TIMEOUT_SECONDS=1 \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ "$(<"$fixture/service.id")" == "$unrelated_id" &&
      -f "$fixture/replaced.marker" &&
      ! -e "$fixture/deleted.id" &&
      ! -e "$fixture/unrelated-delete.marker" ]]
}

expect_success \
  "Docker launcher preserves an unrelated replacement that appears immediately before cleanup" \
  docker_launcher_preserves_replacement_before_cleanup

docker_launcher_cleans_partial_failed_up() {
  local fixture="$TEST_DIR/docker-clean-failed-up"

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' fail-up >"$fixture/mock.mode"
  ! PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ ! -s "$fixture/service.id" &&
      -f "$fixture/up.marker" &&
      -f "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker launcher removes a partial service after Compose up fails" \
  docker_launcher_cleans_partial_failed_up

docker_launcher_cleans_failed_readiness() {
  local fixture="$TEST_DIR/docker-clean-readiness"

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' readiness-fail >"$fixture/mock.mode"
  ! PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    ASYNCQ_DEMO_DOCKER_READY_TIMEOUT_SECONDS=1 \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &&
    [[ ! -s "$fixture/service.id" &&
      -f "$fixture/up.marker" &&
      -f "$fixture/cleanup.marker" ]]
}

expect_success \
  "Docker launcher removes its new service when readiness fails" \
  docker_launcher_cleans_failed_readiness

docker_launcher_cleans_interrupted_up() {
  local fixture="$TEST_DIR/docker-clean-interrupted"
  local launcher_pid
  local up_worker_pid=""
  local attempt
  local result=1

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' block-up >"$fixture/mock.mode"
  PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>&1 &
  launcher_pid="$!"
  for ((attempt = 0; attempt < 300; attempt++)); do
    [[ -f "$fixture/up-blocked.marker" ]] && break
    sleep 0.02
  done
  kill -TERM "$launcher_pid" 2>/dev/null || true
  for ((attempt = 0; attempt < 800; attempt++)); do
    test_pid_is_live "$launcher_pid" || break
    sleep 0.02
  done
  [[ -s "$fixture/up-worker.pid" ]] &&
    up_worker_pid="$(<"$fixture/up-worker.pid")"
  wait "$launcher_pid" 2>/dev/null || true
  if ! test_pid_is_live "$launcher_pid" &&
    ! test_pid_is_live "$up_worker_pid" &&
    [[ ! -s "$fixture/service.id" && -f "$fixture/cleanup.marker" ]]; then
    result=0
  fi
  kill -KILL "$up_worker_pid" 2>/dev/null || true
  return "$result"
}

expect_success \
  "Docker launcher kills an interrupted owned Compose command and removes its partial service" \
  docker_launcher_cleans_interrupted_up

docker_launcher_reports_cleanup_failure() {
  local fixture="$TEST_DIR/docker-cleanup-failure"
  local stderr_file="$fixture/stderr.txt"

  setup_docker_launcher_fixture "$fixture"
  printf '%s\n' readiness-cleanup-fail >"$fixture/mock.mode"
  if PATH="$fixture/fake-bin:$PATH" \
    MOCK_DOCKER_ROOT="$fixture" \
    ASYNCQ_DEMO_DOCKER_READY_TIMEOUT_SECONDS=1 \
    "$fixture/scripts/start-demo-grafana.sh" >/dev/null 2>"$stderr_file"; then
    return 1
  fi
  [[ -s "$fixture/service.id" &&
    -f "$fixture/cleanup.marker" ]] &&
    grep -Fq "automatic Docker cleanup failed" "$stderr_file"
}

expect_success \
  "Docker launcher reports when automatic cleanup cannot remove its service" \
  docker_launcher_reports_cleanup_failure

expect_output \
  "Compose Grafana image is pinned by tag and official index digest" \
  "grafana/grafana:13.1.1@sha256:7cb8c64c4d57a57e734073f3cc94620adb24a0acb929bd80ba9f14017e3a975b" \
  node -e \
  'const fs=require("fs"); const yaml=require("yaml"); console.log(yaml.parse(fs.readFileSync(process.argv[1],"utf8")).services.grafana.image);' \
  "$ROOT_DIR/demo/docker-compose.yml"

expect_output \
  "Compose shares the host network while Grafana and q resolve only through IPv4 loopback" \
  '{"networkMode":"host","hasPorts":false,"address":"127.0.0.1","port":"3000","qPort":"${ASYNCQ_DEMO_Q_PORT:-5000}","qPortLabel":"${ASYNCQ_DEMO_Q_PORT:-5000}","datasourceMount":"./runtime/docker-datasource-${ASYNCQ_DEMO_Q_PORT:-5000}.yml:/etc/grafana/provisioning/datasources/asyncq.yml:ro","ownership":"${ASYNCQ_DEMO_COMPOSE_OWNERSHIP_TOKEN:-manual}"}' \
  node -e \
  'const fs=require("fs"); const yaml=require("yaml"); const s=yaml.parse(fs.readFileSync(process.argv[1],"utf8")).services.grafana; console.log(JSON.stringify({networkMode:s.network_mode,hasPorts:Object.hasOwn(s,"ports"),address:s.environment.GF_SERVER_HTTP_ADDR,port:s.environment.GF_SERVER_HTTP_PORT,qPort:s.environment.ASYNCQ_DEMO_Q_PORT,qPortLabel:s.labels["io.asyncq.demo.q-port"],datasourceMount:s.volumes.at(-1),ownership:s.labels["io.asyncq.demo.ownership-token"]}));' \
  "$ROOT_DIR/demo/docker-compose.yml"

launcher_capture_failure_keeps_owned_child_for_cleanup() {
  local launcher

  for launcher in \
    "$ROOT_DIR/scripts/start-demo-q.sh" \
    "$ROOT_DIR/scripts/start-demo-grafana-local.sh"; do
    grep -Fq 'STARTED_PID="$!"' "$launcher" &&
      grep -Fq 'if ! STARTED_START="$(demo_pid_start_time "$STARTED_PID")"; then' "$launcher" &&
      grep -Fq 'demo_terminate_owned_child "$STARTED_PID" 1 || true' "$launcher" ||
      return 1
  done
}

expect_success \
  "q and Grafana launchers retain and reap their exact child after identity-capture failure" \
  launcher_capture_failure_keeps_owned_child_for_cleanup

pinned_grafana_defaults_are_present() {
  grep -Fq 'demo_grafana_pinned_version' "$ROOT_DIR/scripts/start-demo-grafana-local.sh" &&
    grep -Fq 'grafana_13.1.1_29761037902_linux_amd64.tar.gz' "$ROOT_DIR/scripts/demo-launch-common.sh" &&
    grep -Fq 'e47443214da0de041ffb29633d0977ce31ba7c8c569f09974ef5294a8ce32f08' "$ROOT_DIR/scripts/demo-launch-common.sh" &&
    grep -Fq '28ef74a3bd01fec42fc78b1b9583809f35035654adcbf333c5bba440f418c07d' "$ROOT_DIR/scripts/demo-launch-common.sh" &&
    grep -Fq 'c4553a3aaafb1519f9af4af01a9ae6fa51ce1e0fbec63b672c1e974d36ef4298' "$ROOT_DIR/scripts/demo-launch-common.sh" &&
    grep -Fq 'demo_resolve_grafana_artifact' "$ROOT_DIR/scripts/install-demo-business-plugins.sh"
}

expect_success \
  "local Grafana defaults use the pinned 13.1.1 artifacts and checksums" \
  pinned_grafana_defaults_are_present

printf 'ok: %d demo launcher security checks passed\n' "$PASSED"
