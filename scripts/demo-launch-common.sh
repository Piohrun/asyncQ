#!/bin/bash

# Shared validation, process-identity, and archive checks for the demo launchers.
# shellcheck shell=bash

demo_error() {
  printf 'Error: %s\n' "$*" >&2
}

demo_validate_safe_path() {
  local value="$1"
  local component
  local -a components=()

  [[ -n "$value" ]] || {
    demo_error "PATH must be non-empty"
    return 1
  }
  if [[ "$value" == :* || "$value" == *: || "$value" == *::* ]]; then
    demo_error "PATH must not contain empty entries"
    return 1
  fi
  [[ ! "$value" =~ [[:cntrl:]] ]] || {
    demo_error "PATH must contain no control characters"
    return 1
  }
  IFS=: read -r -a components <<<"$value"
  ((${#components[@]} > 0)) || return 1
  for component in "${components[@]}"; do
    [[ -n "$component" && "$component" == /* && "$component" != */../* &&
      "$component" != */./* && "$component" != */.. &&
      "$component" != */. ]] || {
      demo_error "PATH entries must be absolute canonical paths"
      return 1
    }
  done
}

demo_node() {
  command -v node >/dev/null 2>&1 || {
    demo_error "node is required"
    return 1
  }
  env -u NODE_OPTIONS -u NODE_PATH node "$@"
}

demo_append_preserved_network_environment() {
  local array_name="$1"
  local name
  # shellcheck disable=SC2178 # destination is a nameref to the caller's array.
  local -n destination="$array_name"

  for name in \
    HTTP_PROXY HTTPS_PROXY NO_PROXY \
    http_proxy https_proxy no_proxy \
    SSL_CERT_FILE SSL_CERT_DIR; do
    if [[ -v "$name" ]]; then
      destination+=("$name=${!name}")
    fi
  done
}

demo_prepare_clean_environment() {
  local array_name="$1"
  local private_home="$2"
  local private_tmp="$3"
  # shellcheck disable=SC2178 # destination is a nameref to the caller's array.
  local -n destination="$array_name"

  demo_validate_safe_path "$PATH" || return 1
  demo_require_real_directory "$private_home" "private child HOME" || return 1
  demo_require_real_directory "$private_tmp" "private child temporary directory" || return 1
  chmod 700 -- "$private_home" "$private_tmp" || return 1
  destination=(
    env -i
    "PATH=$PATH"
    "HOME=$private_home"
    "TMPDIR=$private_tmp"
    LANG=C
    LC_ALL=C
  )
  demo_append_preserved_network_environment "$array_name"
}

demo_write_empty_private_config() {
  local parent="$1"
  local output="$2"
  local label="$3"
  local temporary
  local mode
  local size

  demo_require_real_directory "$parent" "$label parent" || return 1
  [[ "${output%/*}" == "$parent" ]] || {
    demo_error "$label is not an immediate child of its private directory"
    return 1
  }
  if [[ -e "$output" || -L "$output" ]] &&
    ! demo_regular_file_link_count_one "$output"; then
    demo_error "$label must be a regular single-link file: $output"
    return 1
  fi
  demo_require_regular_output "$parent" "$output" "$label" || return 1
  temporary="$(mktemp "$parent/.npm-config.XXXXXX")" || return 1
  if ! chmod 600 -- "$temporary" ||
    ! mv -f -- "$temporary" "$output"; then
    rm -f -- "$temporary"
    return 1
  fi
  demo_regular_file_link_count_one "$output" || return 1
  mode="$(stat -c '%a' -- "$output")" || return 1
  size="$(stat -c '%s' -- "$output")" || return 1
  [[ "$mode" == "600" && "$size" == "0" ]]
}

demo_prepare_clean_build_environment() {
  local array_name="$1"
  local private_home="$2"
  local private_tmp="$3"
  local name
  local npm_global_config="$private_home/.npmrc-global"
  local npm_global_identity
  local npm_user_config="$private_home/.npmrc-user"
  local npm_user_identity
  local original_home="${HOME:-}"
  local value
  local -A cache_environment=()
  # shellcheck disable=SC2178 # destination is a nameref to the caller's array.
  local -n destination="$array_name"

  demo_prepare_clean_environment \
    "$array_name" \
    "$private_home" \
    "$private_tmp" || return 1
  demo_write_empty_private_config \
    "$private_home" \
    "$npm_user_config" \
    "private npm user configuration" || return 1
  demo_write_empty_private_config \
    "$private_home" \
    "$npm_global_config" \
    "private npm global configuration" || return 1
  npm_user_identity="$(stat -c '%d:%i' -- "$npm_user_config")" || return 1
  npm_global_identity="$(stat -c '%d:%i' -- "$npm_global_config")" || return 1
  [[ "$npm_user_config" != "$npm_global_config" &&
    "$npm_user_identity" != "$npm_global_identity" ]] || {
    demo_error "private npm user and global configurations must be distinct files"
    return 1
  }
  destination+=(
    GOENV=off
    GOFLAGS=
    GOWORK=off
    GOTOOLCHAIN=local
    GIT_CONFIG_GLOBAL=/dev/null
    GIT_CONFIG_NOSYSTEM=1
    GIT_TERMINAL_PROMPT=0
    "NPM_CONFIG_USERCONFIG=$npm_user_config"
    "NPM_CONFIG_GLOBALCONFIG=$npm_global_config"
    NPM_CONFIG_NODE_OPTIONS=
    npm_config_node_options=
  )

  # Cache locations are inert data paths and materially reduce rebuild cost.
  # Executable/configuration hooks remain absent from the allowlisted child.
  if [[ "$original_home" == /* && ! "$original_home" =~ [[:cntrl:]] ]]; then
    cache_environment[NPM_CONFIG_CACHE]="$original_home/.npm"
    cache_environment[GOCACHE]="$original_home/.cache/go-build"
    cache_environment[GOPATH]="$original_home/go"
    cache_environment[GOMODCACHE]="$original_home/go/pkg/mod"
  fi
  for name in NPM_CONFIG_CACHE npm_config_cache GOCACHE GOMODCACHE GOPATH; do
    if [[ -v "$name" ]]; then
      value="${!name}"
      if [[ "$value" == /* && ! "$value" =~ [[:cntrl:]] ]]; then
        cache_environment["$name"]="$value"
      fi
    fi
  done
  for name in NPM_CONFIG_CACHE npm_config_cache GOCACHE GOMODCACHE GOPATH; do
    if [[ -v "cache_environment[$name]" ]]; then
      destination+=("$name=${cache_environment[$name]}")
    fi
  done
}

demo_validate_port() {
  local name="$1"
  local value="$2"

  if [[ ! "$value" =~ ^[1-9][0-9]{0,4}$ ]] || ((10#${value} > 65535)); then
    demo_error "$name must be a canonical decimal integer in the range 1..65535 (got: $value)"
    return 1
  fi
}

demo_validate_release_token() {
  local value="$1"

  if ((${#value} > 64)) ||
    [[ ! "$value" =~ ^[0-9]+(\.[0-9]+){2}([+-][0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
    demo_error "GRAFANA_VERSION must be a release token such as 13.1.1 (got: $value)"
    return 1
  fi
}

demo_validate_grafana_arch() {
  case "$1" in
    amd64 | arm64 | armv7)
      return 0
      ;;
    *)
      demo_error "GRAFANA_ARCH must be one of amd64, arm64, or armv7 (got: $1)"
      return 1
      ;;
  esac
}

demo_validate_sha256() {
  local value="$1"

  if [[ ! "$value" =~ ^[0-9A-Fa-f]{64}$ ]]; then
    demo_error "GRAFANA_SHA256 must contain exactly 64 hexadecimal characters"
    return 1
  fi
}

demo_grafana_pinned_version() {
  printf '%s\n' "13.1.1"
}

demo_resolve_grafana_arch() {
  local arch

  if (($# == 1)); then
    arch="$1"
  else
    case "$(uname -m)" in
      x86_64 | amd64)
        arch="amd64"
        ;;
      aarch64 | arm64)
        arch="arm64"
        ;;
      armv7l | armv7)
        arch="armv7"
        ;;
      *)
        demo_error "unsupported architecture: $(uname -m)"
        return 1
        ;;
    esac
  fi
  demo_validate_grafana_arch "$arch" || return 1
  printf '%s\n' "$arch"
}

demo_grafana_release_metadata() {
  local version="$1"
  local arch="$2"

  [[ "$version" == "$(demo_grafana_pinned_version)" ]] || return 1
  demo_validate_grafana_arch "$arch" || return 1
  case "$arch" in
    amd64)
      printf '%s\t%s\n' \
        "e47443214da0de041ffb29633d0977ce31ba7c8c569f09974ef5294a8ce32f08" \
        "https://dl.grafana.com/grafana/release/13.1.1/grafana_13.1.1_29761037902_linux_amd64.tar.gz"
      ;;
    arm64)
      printf '%s\t%s\n' \
        "28ef74a3bd01fec42fc78b1b9583809f35035654adcbf333c5bba440f418c07d" \
        "https://dl.grafana.com/grafana/release/13.1.1/grafana_13.1.1_29761037902_linux_arm64.tar.gz"
      ;;
    armv7)
      printf '%s\t%s\n' \
        "c4553a3aaafb1519f9af4af01a9ae6fa51ce1e0fbec63b672c1e974d36ef4298" \
        "https://dl.grafana.com/grafana/release/13.1.1/grafana_13.1.1_29761037902_linux_arm-7.tar.gz"
      ;;
  esac
}

demo_resolve_grafana_artifact() {
  local version="$1"
  local arch="$2"
  local caller_sha="${3:-}"
  local official_sha
  local url

  demo_validate_release_token "$version" || return 1
  demo_validate_grafana_arch "$arch" || return 1
  if [[ "$version" == "$(demo_grafana_pinned_version)" ]]; then
    IFS=$'\t' read -r official_sha url < <(
      demo_grafana_release_metadata "$version" "$arch"
    ) || return 1
    if [[ -n "$caller_sha" ]]; then
      demo_validate_sha256 "$caller_sha" || return 1
      if [[ "${caller_sha,,}" != "$official_sha" ]]; then
        demo_error "GRAFANA_SHA256 does not match the official $version linux-$arch artifact"
        return 1
      fi
    fi
    printf '%s\t%s\n' "$official_sha" "$url"
    return 0
  fi

  if [[ -z "$caller_sha" ]]; then
    demo_error "GRAFANA_SHA256 is required when overriding GRAFANA_VERSION"
    return 1
  fi
  demo_validate_sha256 "$caller_sha" || return 1
  printf '%s\t%s\n' \
    "${caller_sha,,}" \
    "https://dl.grafana.com/oss/release/grafana-$version.linux-$arch.tar.gz"
}

demo_regular_file_link_count_one() {
  local path="$1"
  local link_count

  [[ -f "$path" && ! -L "$path" ]] || return 1
  link_count="$(stat -c '%h' -- "$path")" || return 1
  [[ "$link_count" == "1" ]]
}

demo_require_real_directory() {
  local path="$1"
  local label="$2"

  if [[ ! -d "$path" || -L "$path" ]]; then
    demo_error "$label must be a real directory, not a symlink: $path"
    return 1
  fi
}

demo_secure_directory() {
  local path="$1"
  local label="$2"

  demo_require_real_directory "$path" "$label" || return 1
  chmod 700 -- "$path"
}

demo_ensure_child_directory() {
  local parent="$1"
  local path="$2"
  local label="$3"

  demo_require_real_directory "$parent" "${label} parent" || return 1
  [[ "${path%/*}" == "$parent" ]] || {
    demo_error "$label is not an immediate child of its expected parent: $path"
    return 1
  }

  if [[ -e "$path" || -L "$path" ]]; then
    demo_require_real_directory "$path" "$label"
  else
    mkdir -- "$path"
  fi
}

demo_validate_anchored_descendant() {
  local anchor="$1"
  local path="$2"
  local label="$3"
  local component
  local relative
  local -a components=()

  demo_require_real_directory "$anchor" "$label anchor" || return 1
  [[ "$anchor" == /* && "$path" == "$anchor/"* ]] || {
    demo_error "$label is not anchored below its expected root: $path"
    return 1
  }
  relative="${path#"$anchor"/}"
  [[ -n "$relative" && ! "$relative" =~ [[:cntrl:]] ]] || {
    demo_error "$label has an invalid anchored path: $path"
    return 1
  }
  IFS=/ read -r -a components <<<"$relative"
  ((${#components[@]} > 0)) || return 1
  for component in "${components[@]}"; do
    [[ -n "$component" && "$component" != "." && "$component" != ".." ]] || {
      demo_error "$label has an unsafe path component: $path"
      return 1
    }
  done
}

demo_walk_anchored_directory() {
  local anchor="$1"
  local path="$2"
  local label="$3"
  local create_missing="$4"
  local component
  local current="$anchor"
  local relative
  local -a components=()

  demo_validate_anchored_descendant "$anchor" "$path" "$label" || return 1
  [[ "$create_missing" == "0" || "$create_missing" == "1" ]] || return 1
  relative="${path#"$anchor"/}"
  IFS=/ read -r -a components <<<"$relative"
  for component in "${components[@]}"; do
    current="$current/$component"
    if [[ -e "$current" || -L "$current" ]]; then
      demo_require_real_directory "$current" "$label component" || return 1
    elif [[ "$create_missing" == "1" ]]; then
      mkdir -- "$current" || return 1
      demo_require_real_directory "$current" "$label component" || return 1
    else
      demo_error "$label directory is missing: $current"
      return 1
    fi
  done
}

demo_require_anchored_directory() {
  demo_walk_anchored_directory "$1" "$2" "$3" 0
}

demo_ensure_anchored_directory() {
  demo_walk_anchored_directory "$1" "$2" "$3" 1
}

demo_public_tree_operation() {
  local anchor="$1"
  local tree="$2"
  local executable="$3"
  local operation="$4"
  local label="$5"

  demo_require_anchored_directory "$anchor" "$tree" "$label" || return 1
  case "$operation" in
    structure | normalize | modes)
      ;;
    *)
      demo_error "unknown public-tree operation: $operation"
      return 1
      ;;
  esac
  if [[ -n "$executable" ]]; then
    demo_validate_anchored_descendant "$tree" "$executable" "$label executable" ||
      return 1
  fi

  demo_node - "$tree" "$executable" "$operation" "$label" <<'NODE'
const fs = require('fs');
const path = require('path');

const [root, executable, operation, label] = process.argv.slice(2);
const MAX_ENTRIES = 50000;
const expectedDirectoryMode = 0o755;
const expectedFileMode = 0o644;
const expectedExecutableMode = 0o755;

const fail = (message) => {
  process.stderr.write(`Error: ${label} ${message}\n`);
  process.exit(1);
};

const sameIdentity = (left, right) => (
  left.dev === right.dev &&
  left.ino === right.ino &&
  left.isDirectory() === right.isDirectory() &&
  left.isFile() === right.isFile()
);

const collect = () => {
  const entries = [];
  const visit = (entryPath) => {
    let stat;
    try {
      stat = fs.lstatSync(entryPath, {bigint: true});
    } catch {
      fail(`could not inspect ${entryPath}`);
    }
    if (stat.isSymbolicLink()) fail(`contains a symlink: ${entryPath}`);
    if (stat.isDirectory()) {
      entries.push({entryPath, stat, kind: 'directory'});
      if (entries.length > MAX_ENTRIES) fail(`exceeds ${MAX_ENTRIES} entries`);
      let names;
      try {
        names = fs.readdirSync(entryPath).sort();
      } catch {
        fail(`directory is not safely readable: ${entryPath}`);
      }
      for (const name of names) visit(path.join(entryPath, name));
      return;
    }
    if (stat.isFile()) {
      if (stat.nlink !== 1n) {
        fail(`contains a regular file with link count ${stat.nlink}: ${entryPath}`);
      }
      entries.push({entryPath, stat, kind: 'file'});
      if (entries.length > MAX_ENTRIES) fail(`exceeds ${MAX_ENTRIES} entries`);
      return;
    }
    fail(`contains a non-directory, non-regular entry: ${entryPath}`);
  };
  visit(root);
  return entries;
};

const expectedMode = (entry) => (
  entry.kind === 'directory'
    ? expectedDirectoryMode
    : entry.entryPath === executable
      ? expectedExecutableMode
      : expectedFileMode
);

const validateModes = (entries) => {
  let executableFound = executable.length === 0;
  for (const entry of entries) {
    if (entry.entryPath === executable) executableFound = true;
    const mode = Number(entry.stat.mode & 0o7777n);
    if (mode !== expectedMode(entry)) {
      fail(`has mode ${mode.toString(8)} instead of ${expectedMode(entry).toString(8)}: ${entry.entryPath}`);
    }
  }
  if (!executableFound) fail(`is missing its exact backend executable: ${executable}`);
};

let entries = collect();
if (operation === 'structure') process.exit(0);

let executableFound = executable.length === 0;
for (const entry of entries) {
  if (entry.entryPath === executable) executableFound = true;
}
if (!executableFound) fail(`is missing its exact backend executable: ${executable}`);

if (operation === 'normalize') {
  const noFollow = fs.constants.O_NOFOLLOW;
  const directoryOnly = fs.constants.O_DIRECTORY;
  if (typeof noFollow !== 'number' || typeof directoryOnly !== 'number') {
    fail('cannot enforce no-follow mode changes on this platform');
  }
  for (const entry of entries) {
    const flags = fs.constants.O_RDONLY |
      noFollow |
      (entry.kind === 'directory' ? directoryOnly : 0);
    let descriptor;
    try {
      descriptor = fs.openSync(entry.entryPath, flags);
      const opened = fs.fstatSync(descriptor, {bigint: true});
      if (!sameIdentity(entry.stat, opened) ||
        (entry.kind === 'file' && opened.nlink !== 1n)) {
        fail(`changed identity before mode normalization: ${entry.entryPath}`);
      }
      fs.fchmodSync(descriptor, expectedMode(entry));
      const changed = fs.fstatSync(descriptor, {bigint: true});
      if (!sameIdentity(opened, changed) ||
        (entry.kind === 'file' && changed.nlink !== 1n) ||
        Number(changed.mode & 0o7777n) !== expectedMode(entry)) {
        fail(`could not normalize safely: ${entry.entryPath}`);
      }
    } catch {
      fail(`could not normalize without following links: ${entry.entryPath}`);
    } finally {
      if (descriptor !== undefined) fs.closeSync(descriptor);
    }
  }
  entries = collect();
}
validateModes(entries);
NODE
}

demo_resolve_build_goarch() {
  case "$(uname -m)" in
    x86_64 | amd64)
      printf '%s\n' amd64
      ;;
    aarch64 | arm64)
      printf '%s\n' arm64
      ;;
    armv7l | armv7)
      printf '%s\n' arm
      ;;
    *)
      demo_error "unsupported architecture: $(uname -m)"
      return 1
      ;;
  esac
}

demo_validate_build_goarch() {
  case "$1" in
    amd64 | arm64 | arm)
      ;;
    *)
      demo_error "unsupported build GOARCH: $1"
      return 1
      ;;
  esac
}

demo_prepare_build_output_roots() {
  local root_dir="$1"
  local datasource_dir="$root_dir/dist"
  local panel_parent="$root_dir/dist-panel"
  local masterdata_dir="$panel_parent/asyncq-masterdata-panel"
  local excel_dir="$panel_parent/asyncq-excel-report-panel"

  demo_require_real_directory "$root_dir" "repository root" || return 1
  demo_ensure_anchored_directory \
    "$root_dir" "$datasource_dir" "datasource build output" || return 1
  demo_ensure_anchored_directory \
    "$root_dir" "$panel_parent" "panel build output parent" || return 1
  demo_ensure_anchored_directory \
    "$root_dir" "$masterdata_dir" "master-data panel build output" || return 1
  demo_ensure_anchored_directory \
    "$root_dir" "$excel_dir" "Excel-report panel build output" || return 1
  demo_public_tree_operation \
    "$root_dir" "$datasource_dir" "" structure "datasource build output" ||
    return 1
  demo_public_tree_operation \
    "$root_dir" "$masterdata_dir" "" structure "master-data panel build output" ||
    return 1
  demo_public_tree_operation \
    "$root_dir" "$excel_dir" "" structure "Excel-report panel build output"
}

demo_build_output_trees_complete() {
  local root_dir="$1"
  local goarch="$2"

  demo_validate_build_goarch "$goarch" || return 1
  demo_regular_file_link_count_one \
    "$root_dir/dist/gpx_asyncq-kdbbackend-datasource_linux_$goarch" &&
    demo_regular_file_link_count_one "$root_dir/dist/plugin.json" &&
    demo_regular_file_link_count_one \
      "$root_dir/dist-panel/asyncq-masterdata-panel/plugin.json" &&
    demo_regular_file_link_count_one \
      "$root_dir/dist-panel/asyncq-excel-report-panel/plugin.json"
}

demo_validate_build_output_publication() {
  local root_dir="$1"
  local goarch="$2"
  local backend="$root_dir/dist/gpx_asyncq-kdbbackend-datasource_linux_$goarch"

  demo_validate_build_goarch "$goarch" || return 1
  demo_prepare_build_output_roots "$root_dir" || return 1
  demo_build_output_trees_complete "$root_dir" "$goarch" || {
    demo_error "build outputs are incomplete for linux-$goarch"
    return 1
  }
  demo_public_tree_operation \
    "$root_dir" "$root_dir/dist" "$backend" modes "datasource build output" ||
    return 1
  demo_public_tree_operation \
    "$root_dir" \
    "$root_dir/dist-panel/asyncq-masterdata-panel" \
    "" \
    modes \
    "master-data panel build output" || return 1
  demo_public_tree_operation \
    "$root_dir" \
    "$root_dir/dist-panel/asyncq-excel-report-panel" \
    "" \
    modes \
    "Excel-report panel build output"
}

demo_publish_build_output_trees() {
  local root_dir="$1"
  local goarch="$2"
  local backend="$root_dir/dist/gpx_asyncq-kdbbackend-datasource_linux_$goarch"

  demo_validate_build_goarch "$goarch" || return 1
  demo_prepare_build_output_roots "$root_dir" || return 1
  demo_build_output_trees_complete "$root_dir" "$goarch" || {
    demo_error "build outputs are incomplete for linux-$goarch"
    return 1
  }
  demo_public_tree_operation \
    "$root_dir" "$root_dir/dist" "$backend" normalize "datasource build output" ||
    return 1
  demo_public_tree_operation \
    "$root_dir" \
    "$root_dir/dist-panel/asyncq-masterdata-panel" \
    "" \
    normalize \
    "master-data panel build output" || return 1
  demo_public_tree_operation \
    "$root_dir" \
    "$root_dir/dist-panel/asyncq-excel-report-panel" \
    "" \
    normalize \
    "Excel-report panel build output" || return 1
  demo_validate_build_output_publication "$root_dir" "$goarch"
}

demo_validate_docker_static_publication() {
  local root_dir="$1"

  demo_require_anchored_directory \
    "$root_dir" "$root_dir/demo/templates" "Docker template bind tree" ||
    return 1
  demo_require_anchored_directory \
    "$root_dir" \
    "$root_dir/demo/grafana/provisioning" \
    "Docker provisioning bind tree" || return 1
  demo_public_tree_operation \
    "$root_dir" \
    "$root_dir/demo/templates" \
    "" \
    modes \
    "Docker template bind tree" || return 1
  demo_public_tree_operation \
    "$root_dir" \
    "$root_dir/demo/grafana/provisioning" \
    "" \
    modes \
    "Docker provisioning bind tree"
}

demo_publish_docker_static_trees() {
  local root_dir="$1"

  demo_require_anchored_directory \
    "$root_dir" "$root_dir/demo/templates" "Docker template bind tree" ||
    return 1
  demo_require_anchored_directory \
    "$root_dir" \
    "$root_dir/demo/grafana/provisioning" \
    "Docker provisioning bind tree" || return 1
  demo_public_tree_operation \
    "$root_dir" \
    "$root_dir/demo/templates" \
    "" \
    normalize \
    "Docker template bind tree" || return 1
  demo_public_tree_operation \
    "$root_dir" \
    "$root_dir/demo/grafana/provisioning" \
    "" \
    normalize \
    "Docker provisioning bind tree" || return 1
  demo_validate_docker_static_publication "$root_dir"
}

demo_set_public_regular_file_mode() {
  local parent="$1"
  local path="$2"
  local label="$3"

  demo_require_regular_output "$parent" "$path" "$label" || return 1
  demo_regular_file_link_count_one "$path" || {
    demo_error "$label must be a regular single-link file: $path"
    return 1
  }
  demo_node - "$path" "$label" <<'NODE'
const fs = require('fs');

const [path, label] = process.argv.slice(2);
const before = fs.lstatSync(path, {bigint: true});
if (!before.isFile() || before.isSymbolicLink() || before.nlink !== 1n) process.exit(1);
const descriptor = fs.openSync(path, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
try {
  const opened = fs.fstatSync(descriptor, {bigint: true});
  if (
    opened.dev !== before.dev ||
    opened.ino !== before.ino ||
    !opened.isFile() ||
    opened.nlink !== 1n
  ) {
    process.exit(1);
  }
  fs.fchmodSync(descriptor, 0o644);
  const after = fs.fstatSync(descriptor, {bigint: true});
  if (
    after.dev !== opened.dev ||
    after.ino !== opened.ino ||
    !after.isFile() ||
    after.nlink !== 1n ||
    Number(after.mode & 0o7777n) !== 0o644
  ) {
    process.exit(1);
  }
} finally {
  fs.closeSync(descriptor);
}
NODE
  demo_regular_file_link_count_one "$path" || return 1
  [[ "$(stat -c '%a' -- "$path")" == "644" ]]
}

demo_require_regular_output() {
  local parent="$1"
  local path="$2"
  local label="$3"
  local link_count

  demo_require_real_directory "$parent" "${label} parent" || return 1
  [[ "${path%/*}" == "$parent" ]] || {
    demo_error "$label is not an immediate child of its expected parent: $path"
    return 1
  }
  if [[ -e "$path" || -L "$path" ]]; then
    if ! demo_regular_file_link_count_one "$path"; then
      demo_error "$label must be a regular file, not a symlink: $path"
      return 1
    fi
  fi
}

demo_secure_regular_output() {
  local parent="$1"
  local path="$2"
  local label="$3"

  demo_require_regular_output "$parent" "$path" "$label" || return 1
  if [[ -e "$path" ]]; then
    chmod 600 -- "$path"
  fi
}

demo_require_replaceable_symlink() {
  local parent="$1"
  local path="$2"
  local label="$3"

  demo_require_real_directory "$parent" "${label} parent" || return 1
  [[ "${path%/*}" == "$parent" ]] || {
    demo_error "$label is not an immediate child of its expected parent: $path"
    return 1
  }
  if [[ -e "$path" || -L "$path" ]] && [[ ! -L "$path" ]]; then
    demo_error "$label exists but is not a replaceable symlink: $path"
    return 1
  fi
}

demo_validate_private_temp_directory() {
  local parent="$1"
  local path="$2"
  local kind="$3"
  local basename
  local pattern

  demo_require_real_directory "$parent" "$kind parent directory" || return 1
  [[ "${path%/*}" == "$parent" ]] || {
    demo_error "$kind must be an immediate child of $parent: $path"
    return 1
  }
  basename="${path##*/}"
  case "$kind" in
    "Grafana install work directory")
      pattern='^\.grafana-install\.[A-Za-z0-9]{6}$'
      ;;
    "Grafana recovery directory")
      pattern='^\.grafana-recovery\.[A-Za-z0-9]{6}$'
      ;;
    "q session directory")
      pattern='^\.q-session\.[A-Za-z0-9]{6}$'
      ;;
    *)
      demo_error "unknown private temporary-directory kind: $kind"
      return 1
      ;;
  esac
  [[ "$basename" =~ $pattern ]] || {
    demo_error "$kind has an invalid generated basename: $path"
    return 1
  }
  demo_require_real_directory "$path" "$kind"
}

demo_plugin_manifest_matches() {
  local manifest="$1"
  local expected_id="$2"
  local expected_version="$3"

  demo_validate_file_size_cap "$manifest" 1048576 "Grafana plugin manifest" || return 1
  demo_node - "$manifest" "$expected_id" "$expected_version" <<'NODE'
const fs = require('fs');

const [manifestPath, expectedId, expectedVersion] = process.argv.slice(2);
let plugin;
try {
  plugin = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
} catch {
  process.exit(1);
}
if (
  plugin === null ||
  typeof plugin !== 'object' ||
  plugin.id !== expectedId ||
  plugin.info === null ||
  typeof plugin.info !== 'object' ||
  plugin.info.version !== expectedVersion
) {
  process.exit(1);
}
NODE
}

demo_valid_signal_pid() {
  local value="$1"

  [[ "$value" =~ ^[1-9][0-9]{0,9}$ ]] || return 1
  ((10#${value} > 1 && 10#${value} <= 2147483647)) || return 1
  ((10#${value} != $$ && 10#${value} != PPID)) || return 1
}

demo_read_pid_file() {
  local pid_file="$1"
  local value

  demo_regular_file_link_count_one "$pid_file" || return 1
  [[ -r "$pid_file" ]] || return 1
  value="$(<"$pid_file")"
  demo_valid_signal_pid "$value" || return 1
  printf '%s\n' "$value"
}

demo_proc_stat_start_time() {
  local stat_line="$1"
  local fields_text
  local -a fields=()
  local value

  [[ "$stat_line" == *") "* ]] || return 1
  fields_text="${stat_line##*) }"
  read -r -a fields <<<"$fields_text"
  ((${#fields[@]} >= 20)) || return 1
  value="${fields[19]}"
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || return 1
  printf '%s\n' "$value"
}

demo_proc_stat_parent_pid() {
  local stat_line="$1"
  local fields_text
  local -a fields=()
  local value

  [[ "$stat_line" == *") "* ]] || return 1
  fields_text="${stat_line##*) }"
  read -r -a fields <<<"$fields_text"
  ((${#fields[@]} >= 2)) || return 1
  value="${fields[1]}"
  [[ "$value" =~ ^[0-9]+$ ]] || return 1
  printf '%s\n' "$value"
}

demo_proc_stat_state() {
  local stat_line="$1"
  local fields_text
  local -a fields=()
  local value

  [[ "$stat_line" == *") "* ]] || return 1
  fields_text="${stat_line##*) }"
  read -r -a fields <<<"$fields_text"
  ((${#fields[@]} >= 1)) || return 1
  value="${fields[0]}"
  [[ "$value" =~ ^[A-Z]$ ]] || return 1
  printf '%s\n' "$value"
}

demo_proc_stat_process_group() {
  local stat_line="$1"
  local fields_text
  local -a fields=()
  local value

  [[ "$stat_line" == *") "* ]] || return 1
  fields_text="${stat_line##*) }"
  read -r -a fields <<<"$fields_text"
  ((${#fields[@]} >= 3)) || return 1
  value="${fields[2]}"
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || return 1
  printf '%s\n' "$value"
}

demo_pid_start_time() {
  local pid="$1"
  local stat_line

  demo_valid_signal_pid "$pid" || return 1
  [[ -r "/proc/$pid/stat" ]] || return 1
  stat_line="$(<"/proc/$pid/stat")"
  demo_proc_stat_start_time "$stat_line"
}

demo_observe_process_start_time() {
  local pid="$1"
  local stat_line

  [[ "$pid" =~ ^[1-9][0-9]{0,9}$ ]] || return 1
  ((10#${pid} > 1 && 10#${pid} <= 2147483647)) || return 1
  [[ -r "/proc/$pid/stat" ]] || return 1
  stat_line="$(<"/proc/$pid/stat")"
  demo_proc_stat_start_time "$stat_line"
}

demo_pid_process_group() {
  local pid="$1"
  local stat_line

  [[ "$pid" =~ ^[1-9][0-9]{0,9}$ ]] || return 1
  ((10#${pid} <= 2147483647)) || return 1
  [[ -r "/proc/$pid/stat" ]] || return 1
  stat_line="$(<"/proc/$pid/stat")"
  demo_proc_stat_process_group "$stat_line"
}

demo_process_instance_matches() {
  local pid="$1"
  local expected_start="$2"
  local expected_parent="${3:-}"
  local stat_line
  local current_start
  local current_parent

  demo_valid_signal_pid "$pid" || return 1
  [[ "$expected_start" =~ ^[1-9][0-9]*$ ]] || return 1
  [[ -r "/proc/$pid/stat" ]] || return 1
  stat_line="$(<"/proc/$pid/stat")"
  current_start="$(demo_proc_stat_start_time "$stat_line")" || return 1
  [[ "$current_start" == "$expected_start" ]] || return 1
  if [[ -n "$expected_parent" ]]; then
    [[ "$expected_parent" =~ ^[1-9][0-9]*$ ]] || return 1
    current_parent="$(demo_proc_stat_parent_pid "$stat_line")" || return 1
    [[ "$current_parent" == "$expected_parent" ]] || return 1
  fi
}

demo_process_instance_is_running() {
  local pid="$1"
  local expected_start="$2"
  local expected_parent="${3:-}"
  local stat_line
  local state

  demo_process_instance_matches "$pid" "$expected_start" "$expected_parent" || return 1
  [[ -r "/proc/$pid/stat" ]] || return 1
  stat_line="$(<"/proc/$pid/stat")"
  state="$(demo_proc_stat_state "$stat_line")" || return 1
  [[ "$state" != "Z" && "$state" != "X" ]]
}

demo_state_target_replaceable() {
  local path="$1"

  if [[ ! -e "$path" && ! -L "$path" ]]; then
    return 0
  fi
  demo_regular_file_link_count_one "$path"
}

demo_read_start_file() {
  local start_file="$1"
  local value

  demo_regular_file_link_count_one "$start_file" || return 1
  [[ -r "$start_file" ]] || return 1
  value="$(<"$start_file")"
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || return 1
  printf '%s\n' "$value"
}

demo_pid_state_matches() {
  local pid="$1"
  local start_file="$2"
  local recorded_start
  local current_start

  recorded_start="$(demo_read_start_file "$start_file")" || return 1
  current_start="$(demo_pid_start_time "$pid")" || return 1
  [[ "$recorded_start" == "$current_start" ]]
}

demo_write_pid_state() {
  local pid_file="$1"
  local start_file="$2"
  local pid="$3"
  local state_dir="${pid_file%/*}"
  local start
  local pid_tmp
  local start_tmp

  demo_valid_signal_pid "$pid" || return 1
  start="$(demo_pid_start_time "$pid")" || return 1
  demo_state_target_replaceable "$pid_file" || {
    demo_error "refusing to replace unsafe or hard-linked PID state: $pid_file"
    return 1
  }
  demo_state_target_replaceable "$start_file" || {
    demo_error "refusing to replace unsafe or hard-linked start state: $start_file"
    return 1
  }
  pid_tmp="$(mktemp "$state_dir/.demo-pid.XXXXXX")" || return 1
  start_tmp="$(mktemp "$state_dir/.demo-start.XXXXXX")" || {
    rm -f -- "$pid_tmp"
    return 1
  }

  if ! printf '%s\n' "$pid" >"$pid_tmp" ||
    ! printf '%s\n' "$start" >"$start_tmp" ||
    ! chmod 600 "$pid_tmp" "$start_tmp" ||
    ! mv -f -- "$start_tmp" "$start_file" ||
    ! mv -f -- "$pid_tmp" "$pid_file"; then
    rm -f -- "$pid_tmp" "$start_tmp"
    return 1
  fi
  demo_regular_file_link_count_one "$pid_file" &&
    demo_regular_file_link_count_one "$start_file"
}

demo_clear_pid_state() {
  local pid_file="$1"
  local start_file="$2"
  local path
  local status=0

  for path in "$pid_file" "$start_file"; do
    if [[ ! -e "$path" && ! -L "$path" ]]; then
      continue
    fi
    if ! demo_regular_file_link_count_one "$path"; then
      demo_error "refusing to remove unsafe or hard-linked process state: $path"
      return 1
    fi
  done
  for path in "$pid_file" "$start_file"; do
    if [[ ! -e "$path" && ! -L "$path" ]]; then
      continue
    fi
    if ! demo_regular_file_link_count_one "$path"; then
      demo_error "refusing to remove process state that changed during validation: $path"
      status=1
      continue
    fi
    rm -- "$path" || status=1
  done
  return "$status"
}

demo_process_group_is_safe() {
  local pid="$1"
  local pgid
  local current_pgid

  demo_valid_signal_pid "$pid" || return 1
  pgid="$(demo_pid_process_group "$pid")" || return 1
  [[ "$pgid" == "$pid" ]] || return 1

  current_pgid="$(demo_pid_process_group "$$")" || return 1
  [[ -n "$current_pgid" && "$pgid" != "$current_pgid" ]]
}

demo_capture_process_table() {
  command -v timeout >/dev/null 2>&1 || return 1
  LC_ALL=C timeout --foreground --kill-after=1s 2s \
    ps -e -o pid=,pgid=,stat=
}

demo_process_group_has_live_members() {
  local pgid="$1"
  local process_table
  local row_pgid
  local state

  demo_valid_signal_pid "$pgid" || return 1
  process_table="$(demo_capture_process_table)" || return 2
  while read -r _ row_pgid state; do
    [[ "$row_pgid" == "$pgid" ]] || continue
    [[ "$state" == Z* || "$state" == X* ]] || return 0
  done <<<"$process_table"
  return 1
}

demo_capture_process_group_witnesses() {
  local pgid="$1"
  local process_table
  local pid
  local row_pgid
  local state
  local start
  local found=0

  demo_valid_signal_pid "$pgid" || return 1
  process_table="$(demo_capture_process_table)" || return 1
  while read -r pid row_pgid state; do
    [[ "$row_pgid" == "$pgid" ]] || continue
    [[ "$state" == Z* || "$state" == X* ]] && continue
    start="$(demo_pid_start_time "$pid" 2>/dev/null || true)"
    [[ -n "$start" ]] || continue
    [[ "$(demo_pid_process_group "$pid" 2>/dev/null || true)" == "$pgid" ]] || continue
    printf '%s:%s\n' "$pid" "$start"
    found=1
  done <<<"$process_table"
  ((found == 1))
}

demo_process_group_has_matching_witness() {
  local pgid="$1"
  local witnesses="$2"
  local witness
  local pid
  local start

  demo_valid_signal_pid "$pgid" || return 1
  while IFS= read -r witness; do
    [[ "$witness" =~ ^([1-9][0-9]{0,9}):([1-9][0-9]*)$ ]] || continue
    pid="${BASH_REMATCH[1]}"
    start="${BASH_REMATCH[2]}"
    demo_process_instance_matches "$pid" "$start" || continue
    [[ "$(demo_pid_process_group "$pid" 2>/dev/null || true)" == "$pgid" ]] || continue
    return 0
  done <<<"$witnesses"
  return 1
}

demo_process_group_signal_gate() {
  local pid="$1"
  local expected_start="${2:-}"
  local expected_parent="${3:-}"
  local verifier="${4:-}"
  shift 4 || true

  demo_process_group_is_safe "$pid" || return 1
  if [[ -n "$expected_start" ]]; then
    demo_process_instance_matches "$pid" "$expected_start" "$expected_parent" || return 1
  elif [[ -n "$expected_parent" ]]; then
    return 1
  fi
  if [[ -n "$verifier" ]]; then
    declare -F "$verifier" >/dev/null || return 1
    "$verifier" "$pid" "$@" >/dev/null || return 1
  fi
}

demo_process_cwd_is() {
  local pid="$1"
  local expected="$2"
  local actual
  local resolved_expected

  [[ -L "/proc/$pid/cwd" ]] || return 1
  actual="$(readlink -f -- "/proc/$pid/cwd")" || return 1
  resolved_expected="$(readlink -f -- "$expected")" || return 1
  [[ "$actual" == "$resolved_expected" ]]
}

demo_grafana_process_home() {
  local pid="$1"
  local root_dir="$2"
  local runtime_dir="$3"
  local -a args=()
  local arg
  local home
  local version
  local config
  local actual_exe
  local expected_exe

  [[ -r "/proc/$pid/cmdline" && -L "/proc/$pid/exe" ]] || return 1
  while IFS= read -r -d '' arg; do
    args+=("$arg")
  done <"/proc/$pid/cmdline"

  ((${#args[@]} == 6)) || return 1
  [[ "${args[1]}" == "server" &&
    "${args[2]}" == "--homepath" &&
    "${args[4]}" == "--config" ]] || return 1
  home="${args[3]}"
  config="${args[5]}"
  case "$home" in
    "$runtime_dir"/grafana-*)
      ;;
    *)
      return 1
      ;;
  esac
  version="${home#"$runtime_dir"/grafana-}"
  demo_validate_release_token "$version" >/dev/null 2>&1 || return 1
  [[ "$home" == "$runtime_dir/grafana-$version" ]] || return 1
  [[ "$config" == "$runtime_dir/grafana.ini" ]] || return 1
  [[ -d "$home" && ! -L "$home" && -d "$home/bin" && ! -L "$home/bin" ]] || return 1
  [[ -f "$home/bin/grafana" && ! -L "$home/bin/grafana" ]] || return 1
  demo_regular_file_link_count_one "$config" || return 1

  actual_exe="$(readlink -f -- "/proc/$pid/exe")" || return 1
  expected_exe="$(readlink -f -- "$home/bin/grafana")" || return 1
  [[ "$actual_exe" == "$expected_exe" ]] || return 1
  demo_process_cwd_is "$pid" "$root_dir" || return 1
  printf '%s\n' "$home"
}

demo_process_environment_value() {
  local pid="$1"
  local key="$2"
  local item

  demo_valid_signal_pid "$pid" || return 1
  [[ "$key" =~ ^[A-Z][A-Z0-9_]*$ && -r "/proc/$pid/environ" ]] || return 1
  while IFS= read -r -d '' item; do
    if [[ "$item" == "$key="* ]]; then
      printf '%s\n' "${item#*=}"
      return 0
    fi
  done <"/proc/$pid/environ"
  return 1
}

demo_q_runner_process_matches() {
  local pid="$1"
  local root_dir="$2"
  local runner="$3"
  local q_script="$4"
  local expected_port="${5:-}"
  local expected_q_binary="${6:-}"
  local -a args=()
  local arg
  local actual_runner_exe
  local expected_runner_exe

  [[ -r "/proc/$pid/cmdline" && -L "/proc/$pid/exe" ]] || return 1
  while IFS= read -r -d '' arg; do
    args+=("$arg")
  done <"/proc/$pid/cmdline"

  ((${#args[@]} == 6)) || return 1
  [[ "$runner" == /* && "$q_script" == /* &&
    "${args[1]}" == "$runner" &&
    "${args[2]}" == /* &&
    "${args[3]}" == "$q_script" &&
    "${args[5]}" == "$root_dir/demo/logs" ]] || return 1
  demo_validate_port "recorded q port" "${args[4]}" >/dev/null 2>&1 || return 1
  if [[ -n "$expected_port" && "${args[4]}" != "$expected_port" ]]; then
    return 1
  fi
  if [[ -n "$expected_q_binary" && "${args[2]}" != "$expected_q_binary" ]]; then
    return 1
  fi
  actual_runner_exe="$(readlink -f -- "/proc/$pid/exe")" || return 1
  expected_runner_exe="$(readlink -f -- "/proc/$$/exe")" || return 1
  [[ "$actual_runner_exe" == "$expected_runner_exe" ]] || return 1
  demo_process_cwd_is "$pid" "$root_dir"
}

demo_bash_script_process_matches() {
  local pid="$1"
  local expected_script="$2"
  local -a args=()
  local arg
  local actual_exe
  local expected_exe

  [[ "$expected_script" == /* &&
    -f "$expected_script" &&
    ! -L "$expected_script" &&
    -r "/proc/$pid/cmdline" &&
    -L "/proc/$pid/exe" ]] || return 1
  while IFS= read -r -d '' arg; do
    args+=("$arg")
  done <"/proc/$pid/cmdline"

  ((${#args[@]} == 2)) || return 1
  [[ "${args[1]}" == "$expected_script" ]] || return 1
  actual_exe="$(readlink -f -- "/proc/$pid/exe")" || return 1
  expected_exe="$(readlink -f -- "/proc/$$/exe")" || return 1
  [[ "$actual_exe" == "$expected_exe" ]]
}

demo_q_runner_port() {
  local pid="$1"
  local -a args=()
  local arg

  [[ -r "/proc/$pid/cmdline" ]] || return 1
  while IFS= read -r -d '' arg; do
    args+=("$arg")
  done <"/proc/$pid/cmdline"
  ((${#args[@]} == 6)) || return 1
  demo_validate_port "recorded q port" "${args[4]}" >/dev/null 2>&1 || return 1
  printf '%s\n' "${args[4]}"
}

demo_verified_q_runner_pid() {
  local pid_file="$1"
  local start_file="$2"
  local root_dir="$3"
  local runner="$4"
  local q_script="$5"
  local pid
  local port
  local child_pid

  pid="$(demo_read_pid_file "$pid_file" 2>/dev/null)" || return 1
  demo_pid_state_matches "$pid" "$start_file" || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  demo_q_runner_process_matches "$pid" "$root_dir" "$runner" "$q_script" || return 1
  demo_process_group_is_safe "$pid" || return 1
  port="$(demo_q_runner_port "$pid")" || return 1
  child_pid="$(demo_q_runner_child_pid "$pid" "$q_script" "$port")" || return 1
  demo_process_owns_ipv4_loopback_listener "$child_pid" "$port" || return 1
  printf '%s\n' "$pid"
}

demo_verified_q_runner_on_port() {
  local pid_file="$1"
  local start_file="$2"
  local root_dir="$3"
  local runner="$4"
  local q_script="$5"
  local expected_port="$6"
  local pid
  local child_pid

  demo_validate_port "expected q port" "$expected_port" >/dev/null 2>&1 || return 1
  pid="$(
    demo_verified_q_runner_pid \
      "$pid_file" \
      "$start_file" \
      "$root_dir" \
      "$runner" \
      "$q_script"
  )" || return 1
  [[ "$(demo_q_runner_port "$pid")" == "$expected_port" ]] || return 1
  child_pid="$(demo_q_runner_child_pid "$pid" "$q_script" "$expected_port")" || return 1
  demo_process_owns_ipv4_loopback_listener "$child_pid" "$expected_port" || return 1
  printf '%s\n' "$pid"
}

demo_should_rollback_reported_process() {
  local status="$1"
  local reported_pid="$2"
  local current_pid="$3"

  [[ "$status" == "started" ]] || return 1
  demo_valid_signal_pid "$reported_pid" || return 1
  demo_valid_signal_pid "$current_pid" || return 1
  [[ "$reported_pid" == "$current_pid" ]]
}

demo_q_runner_child_pid() {
  local runner_pid="$1"
  local q_script="$2"
  local expected_port="$3"
  local children_text
  local -a children=()
  local -a args=()
  local arg
  local child_pid
  local actual_exe
  local tail
  local options_match=0

  demo_valid_signal_pid "$runner_pid" || return 1
  [[ "$q_script" == /* ]] || return 1
  demo_validate_port "recorded q port" "$expected_port" >/dev/null 2>&1 || return 1
  [[ -r "/proc/$runner_pid/task/$runner_pid/children" ]] || return 1
  children_text="$(<"/proc/$runner_pid/task/$runner_pid/children")"
  read -r -a children <<<"$children_text"
  ((${#children[@]} == 1)) || return 1
  child_pid="${children[0]}"
  demo_valid_signal_pid "$child_pid" || return 1
  [[ -r "/proc/$child_pid/cmdline" && -L "/proc/$child_pid/exe" ]] || return 1
  [[ "$(
    demo_proc_stat_parent_pid "$(<"/proc/$child_pid/stat")" 2>/dev/null || true
  )" == "$runner_pid" ]] || return 1
  while IFS= read -r -d '' arg; do
    args+=("$arg")
  done <"/proc/$child_pid/cmdline"
  if ((${#args[@]} >= 10)); then
    tail=$((${#args[@]} - 9))
    if [[ "${args[$tail]}" == "$q_script" &&
      "${args[$((tail + 1))]}" == "-p" &&
      "${args[$((tail + 2))]}" == "127.0.0.1:$expected_port" &&
      "${args[$((tail + 3))]}" == "-T" &&
      "${args[$((tail + 4))]}" == "30" &&
      "${args[$((tail + 5))]}" == "-w" &&
      "${args[$((tail + 6))]}" == "1024" &&
      "${args[$((tail + 7))]}" == "-u" &&
      "${args[$((tail + 8))]}" == "1" ]]; then
      options_match=1
    fi
  fi
  # q normalizes host-qualified -p in its live argv by splitting
  # "127.0.0.1:PORT" into the two exact values "127.0.0.1" and "PORT".
  if ((options_match == 0 && ${#args[@]} >= 11)); then
    tail=$((${#args[@]} - 10))
    if [[ "${args[$tail]}" == "$q_script" &&
      "${args[$((tail + 1))]}" == "-p" &&
      "${args[$((tail + 2))]}" == "127.0.0.1" &&
      "${args[$((tail + 3))]}" == "$expected_port" &&
      "${args[$((tail + 4))]}" == "-T" &&
      "${args[$((tail + 5))]}" == "30" &&
      "${args[$((tail + 6))]}" == "-w" &&
      "${args[$((tail + 7))]}" == "1024" &&
      "${args[$((tail + 8))]}" == "-u" &&
      "${args[$((tail + 9))]}" == "1" ]]; then
      options_match=1
    fi
  fi
  ((options_match == 1)) || return 1
  actual_exe="$(readlink -f -- "/proc/$child_pid/exe")" || return 1
  [[ "$actual_exe" == /* && -f "$actual_exe" && -x "$actual_exe" ]] || return 1
  printf '%s\n' "$child_pid"
}

demo_q_demo_process_matches() {
  local pid="$1"
  local root_dir="$2"
  local runner="$3"
  local q_script="$4"
  local expected_port="${5:-}"
  local expected_q_binary="${6:-}"
  local port
  local child_pid

  demo_q_runner_process_matches \
    "$pid" \
    "$root_dir" \
    "$runner" \
    "$q_script" \
    "$expected_port" \
    "$expected_q_binary" || return 1
  port="$(demo_q_runner_port "$pid")" || return 1
  child_pid="$(demo_q_runner_child_pid "$pid" "$q_script" "$port")" || return 1
  demo_process_owns_ipv4_loopback_listener "$child_pid" "$port"
}

demo_process_owns_listen_port() {
  local pid="$1"
  local port="$2"
  local port_hex
  local fd
  local link
  local socket_table
  local line
  local -a columns=()
  local -A socket_inodes=()

  demo_valid_signal_pid "$pid" || return 1
  demo_validate_port "listener port" "$port" >/dev/null 2>&1 || return 1
  port_hex="$(printf '%04X' "$((10#$port))")"

  for fd in "/proc/$pid/fd/"*; do
    link="$(readlink -- "$fd" 2>/dev/null || true)"
    if [[ "$link" =~ ^socket:\[([0-9]+)\]$ ]]; then
      socket_inodes["${BASH_REMATCH[1]}"]=1
    fi
  done
  ((${#socket_inodes[@]} > 0)) || return 1

  # shellcheck disable=SC2094 # $table is only read; the diagnostic is a variable-name false positive.
  for table in "/proc/$pid/net/tcp" "/proc/$pid/net/tcp6"; do
    [[ -r "$table" ]] || continue
    while IFS= read -r line; do
      read -r -a columns <<<"$line"
      ((${#columns[@]} >= 10)) || continue
      [[ "${columns[1]##*:}" == "$port_hex" && "${columns[3]}" == "0A" ]] || continue
      if [[ -n "${socket_inodes[${columns[9]}]:-}" ]]; then
        return 0
      fi
    done <"$table"
  done
  return 1
}

demo_process_owns_ipv4_loopback_listener() {
  local pid="$1"
  local port="$2"
  local port_hex
  local fd
  local link
  local table
  local line
  local local_address
  local socket_fd
  local -a columns=()
  local -A socket_inodes=()
  local found_loopback=0
  local unsafe_listener=0

  demo_valid_signal_pid "$pid" || return 1
  demo_validate_port "listener port" "$port" >/dev/null 2>&1 || return 1
  port_hex="$(printf '%04X' "$((10#$port))")"
  for fd in "/proc/$pid/fd/"*; do
    link="$(readlink -- "$fd" 2>/dev/null || true)"
    if [[ "$link" =~ ^socket:\[([0-9]+)\]$ ]]; then
      socket_inodes["${BASH_REMATCH[1]}"]=1
    fi
  done
  ((${#socket_inodes[@]} > 0)) || return 1

  for socket_table in "/proc/$pid/net/tcp" "/proc/$pid/net/tcp6"; do
    [[ -r "$socket_table" ]] || return 1
    exec {socket_fd}<"$socket_table" || return 1
    while IFS= read -r line; do
      read -r -a columns <<<"$line"
      ((${#columns[@]} >= 10)) || continue
      [[ "${columns[1]##*:}" == "$port_hex" && "${columns[3]}" == "0A" ]] || continue
      [[ -n "${socket_inodes[${columns[9]}]:-}" ]] || continue
      local_address="${columns[1]%:*}"
      if [[ "$socket_table" == "/proc/$pid/net/tcp" && "$local_address" == "0100007F" ]]; then
        found_loopback=1
      else
        unsafe_listener=1
        break
      fi
    done <&"$socket_fd"
    exec {socket_fd}<&-
    ((unsafe_listener == 0)) || return 1
  done
  ((found_loopback == 1))
}

demo_ipv4_loopback_port_is_reachable() {
  local port="$1"

  demo_validate_port "loopback reachability port" "$port" >/dev/null 2>&1 || return 1
  command -v node >/dev/null 2>&1 || return 1
  command -v timeout >/dev/null 2>&1 || return 1
  timeout --foreground --kill-after=1s 2s \
    env -u NODE_OPTIONS -u NODE_PATH node -e '
    const net = require("net");
    const port = Number(process.argv[1]);
    const socket = net.createConnection({host: "127.0.0.1", port});
    let finished = false;
    const finish = (status) => {
      if (finished) return;
      finished = true;
      socket.destroy();
      process.exit(status);
    };
    socket.setTimeout(1000);
    socket.once("connect", () => finish(0));
    socket.once("error", () => finish(1));
    socket.once("timeout", () => finish(1));
  ' "$port"
}

demo_validate_grafana_health_json() {
  local body_file="$1"
  local expected_version="$2"

  demo_validate_release_token "$expected_version" >/dev/null 2>&1 || return 1
  demo_validate_file_size_cap "$body_file" 4096 "Grafana health response" || return 1
  demo_node - "$body_file" "$expected_version" <<'NODE'
const fs = require('fs');

const [bodyPath, expectedVersion] = process.argv.slice(2);
let body;
try {
  body = JSON.parse(fs.readFileSync(bodyPath, 'utf8'));
} catch {
  process.exit(1);
}
if (
  body === null ||
  typeof body !== 'object' ||
  Array.isArray(body) ||
  body.database !== 'ok' ||
  body.version !== expectedVersion
) {
  process.exit(1);
}
NODE
}

demo_fetch_grafana_health() {
  local port="$1"
  local expected_version="$2"
  local work_dir="$3"
  local health_file
  local status=0

  demo_validate_port "Grafana health port" "$port" >/dev/null 2>&1 || return 1
  demo_validate_release_token "$expected_version" >/dev/null 2>&1 || return 1
  demo_require_real_directory "$work_dir" "Grafana health work directory" || return 1
  command -v curl >/dev/null 2>&1 || return 1
  command -v timeout >/dev/null 2>&1 || return 1
  health_file="$(mktemp "$work_dir/.grafana-health.XXXXXX")" || return 1
  if ! timeout --foreground --kill-after=1s 3s curl --disable \
    --fail \
    --silent \
    --show-error \
    --noproxy '*' \
    --connect-timeout 1 \
    --max-time 2 \
    --max-filesize 4096 \
    --output "$health_file" \
    "http://127.0.0.1:$port/api/health"; then
    status=1
  elif ! demo_validate_grafana_health_json "$health_file" "$expected_version"; then
    status=1
  fi
  rm -- "$health_file" || status=1
  return "$status"
}

demo_fetch_grafana_demo_identity() {
  local port="$1"
  local work_dir="$2"
  local datasource_file
  local plugins_file
  local status=0

  demo_validate_port "Grafana API port" "$port" >/dev/null 2>&1 || return 1
  demo_require_real_directory "$work_dir" "Grafana API work directory" || return 1
  command -v curl >/dev/null 2>&1 || return 1
  command -v timeout >/dev/null 2>&1 || return 1
  datasource_file="$(mktemp "$work_dir/.grafana-datasource.XXXXXX")" || return 1
  plugins_file="$(mktemp "$work_dir/.grafana-plugins.XXXXXX")" || {
    rm -- "$datasource_file"
    return 1
  }

  # Anonymous access is intentionally configured with the Admin role for this
  # loopback-only demo, so no credential is placed in argv or temporary files.
  if ! timeout --foreground --kill-after=1s 3s curl --disable \
    --fail \
    --silent \
    --show-error \
    --noproxy '*' \
    --connect-timeout 1 \
    --max-time 2 \
    --max-filesize 65536 \
    --header 'Accept: application/json' \
    --output "$datasource_file" \
    "http://127.0.0.1:$port/api/datasources/uid/asyncq-demo"; then
    status=1
  elif ! timeout --foreground --kill-after=1s 3s curl --disable \
    --fail \
    --silent \
    --show-error \
    --noproxy '*' \
    --connect-timeout 1 \
    --max-time 2 \
    --max-filesize 1048576 \
    --header 'Accept: application/json' \
    --output "$plugins_file" \
    "http://127.0.0.1:$port/api/plugins"; then
    status=1
  elif ! demo_validate_file_size_cap \
    "$datasource_file" 65536 "Grafana datasource API response" ||
    ! demo_validate_file_size_cap \
      "$plugins_file" 1048576 "Grafana plugins API response" ||
    ! demo_node - "$datasource_file" "$plugins_file" <<'NODE'
const fs = require('fs');

const [datasourcePath, pluginsPath] = process.argv.slice(2);
let datasource;
let plugins;
try {
  datasource = JSON.parse(fs.readFileSync(datasourcePath, 'utf8'));
  plugins = JSON.parse(fs.readFileSync(pluginsPath, 'utf8'));
} catch {
  process.exit(1);
}
if (
  datasource === null ||
  typeof datasource !== 'object' ||
  Array.isArray(datasource) ||
  datasource.uid !== 'asyncq-demo' ||
  datasource.type !== 'asyncq-kdbbackend-datasource'
) {
  process.exit(1);
}
if (!Array.isArray(plugins) || plugins.length > 10000) process.exit(1);
const expected = new Map([
  ['asyncq-kdbbackend-datasource', 'datasource'],
  ['asyncq-masterdata-panel', 'panel'],
  ['asyncq-excel-report-panel', 'panel'],
]);
const seen = new Set();
for (const plugin of plugins) {
  if (
    plugin === null ||
    typeof plugin !== 'object' ||
    Array.isArray(plugin) ||
    typeof plugin.id !== 'string'
  ) {
    process.exit(1);
  }
  if (!expected.has(plugin.id)) continue;
  if (seen.has(plugin.id) || plugin.type !== expected.get(plugin.id)) process.exit(1);
  seen.add(plugin.id);
}
if (seen.size !== expected.size) process.exit(1);
NODE
  then
    status=1
  fi
  rm -- "$datasource_file" "$plugins_file" || status=1
  return "$status"
}

demo_grafana_process_environment_is_exact() {
  local pid="$1"
  local port="$2"
  local root_dir="$3"
  local runtime_dir="$4"
  local log_dir="$5"
  local q_port="$6"
  local item
  local key
  local value
  local -A expected=(
    [GF_AUTH_ANONYMOUS_ENABLED]=true
    [GF_AUTH_ANONYMOUS_ORG_ROLE]=Admin
    [GF_DATABASE_TYPE]=sqlite3
    [GF_LOG_LEVEL]=info
    [GF_PATHS_DATA]="$runtime_dir/data"
    [GF_PATHS_LOGS]="$log_dir"
    [GF_PATHS_PLUGINS]="$runtime_dir/plugins"
    [GF_PATHS_PROVISIONING]="$runtime_dir/provisioning"
    [GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS]="asyncq-kdbbackend-datasource,asyncq-masterdata-panel,asyncq-excel-report-panel"
    [GF_SECURITY_ADMIN_PASSWORD]=admin
    [GF_SECURITY_ADMIN_USER]=admin
    [GF_SERVER_HTTP_ADDR]=127.0.0.1
    [GF_SERVER_HTTP_PORT]="$port"
    [GF_SERVER_ROOT_URL]="http://127.0.0.1:$port"
    [GF_USERS_DEFAULT_THEME]=light
    [ASYNCQ_DEMO_Q_PORT]="$q_port"
    [ASYNCQ_DEMO_TEMPLATE_DIR]="$root_dir/demo/templates"
  )

  demo_valid_signal_pid "$pid" || return 1
  demo_validate_port "Grafana environment port" "$port" >/dev/null 2>&1 || return 1
  demo_validate_port "Grafana environment q port" "$q_port" >/dev/null 2>&1 || return 1
  [[ -r "/proc/$pid/environ" ]] || return 1
  while IFS= read -r -d '' item; do
    key="${item%%=*}"
    case "$key" in
      GF_* | ASYNCQ_DEMO_*)
        [[ -v "expected[$key]" ]] || return 1
        value="${item#*=}"
        [[ "${expected[$key]}" == "$value" ]] || return 1
        unset 'expected[$key]'
        ;;
    esac
  done <"/proc/$pid/environ"
  ((${#expected[@]} == 0))
}

demo_grafana_process_is_ready() {
  local pid="$1"
  local port="$2"
  local root_dir="$3"
  local runtime_dir="$4"
  local expected_home="$5"
  local expected_version="$6"
  local health_work_dir="$7"
  local expected_q_port="${8:-}"
  local actual_home
  local actual_port
  local actual_address
  local actual_q_port

  actual_home="$(demo_grafana_process_home "$pid" "$root_dir" "$runtime_dir")" || return 1
  [[ "$actual_home" == "$expected_home" ]] || return 1
  actual_port="$(demo_process_environment_value "$pid" GF_SERVER_HTTP_PORT)" || return 1
  actual_address="$(demo_process_environment_value "$pid" GF_SERVER_HTTP_ADDR)" || return 1
  [[ "$actual_port" == "$port" && "$actual_address" == "127.0.0.1" ]] || return 1
  if [[ -n "$expected_q_port" ]]; then
    demo_validate_port "expected q port" "$expected_q_port" >/dev/null 2>&1 || return 1
    actual_q_port="$(
      demo_process_environment_value "$pid" ASYNCQ_DEMO_Q_PORT
    )" || return 1
    [[ "$actual_q_port" == "$expected_q_port" ]] || return 1
    demo_grafana_process_environment_is_exact \
      "$pid" \
      "$port" \
      "$root_dir" \
      "$runtime_dir" \
      "$health_work_dir" \
      "$expected_q_port" || return 1
  fi
  demo_process_owns_ipv4_loopback_listener "$pid" "$port" || return 1
  demo_fetch_grafana_health "$port" "$expected_version" "$health_work_dir"
}

demo_render_loopback_datasource() {
  local source_file="$1"
  local output_file="$2"
  local q_port="$3"

  demo_regular_file_link_count_one "$source_file" || return 1
  [[ -r "$source_file" ]] || return 1
  demo_regular_file_link_count_one "$output_file" || return 1
  demo_validate_port "datasource q port" "$q_port" >/dev/null 2>&1 || return 1
  demo_node - "$source_file" "$output_file" "$q_port" <<'NODE'
const fs = require('fs');

const [sourcePath, outputPath, port] = process.argv.slice(2);
let text = fs.readFileSync(sourcePath, 'utf8');
const hostPattern = /^      host: host\.docker\.internal$/gm;
const portPattern = /^      port: 5000$/gm;
const hostMatches = text.match(hostPattern) ?? [];
const portMatches = text.match(portPattern) ?? [];
if (hostMatches.length !== 1 || portMatches.length !== 1) {
  process.exit(1);
}
text = text
  .replace(hostPattern, '      host: 127.0.0.1')
  .replace(portPattern, `      port: ${port}`);
fs.writeFileSync(outputPath, text, {encoding: 'utf8', flag: 'w'});
NODE
}

demo_stop_process_group() {
  local pid="$1"
  local expected_start="${2:-}"
  local expected_parent="${3:-}"
  local verifier="${4:-}"
  local -a verifier_args=()
  local attempt
  local group_status
  local witnesses

  if (($# > 4)); then
    verifier_args=("${@:5}")
  fi
  demo_process_group_signal_gate \
    "$pid" \
    "$expected_start" \
    "$expected_parent" \
    "$verifier" \
    "${verifier_args[@]}" || return 1
  witnesses="$(demo_capture_process_group_witnesses "$pid")" || return 1
  kill -TERM -- "-$pid" || return 1
  kill -CONT -- "-$pid" 2>/dev/null || true

  for ((attempt = 0; attempt < 50; attempt++)); do
    if demo_process_group_has_live_members "$pid"; then
      group_status=0
    else
      group_status=$?
    fi
    case "$group_status" in
      0)
        ;;
      1)
        return 0
        ;;
      *)
        return 1
        ;;
    esac
    sleep 0.1
  done

  demo_process_group_has_matching_witness "$pid" "$witnesses" || return 1
  kill -KILL -- "-$pid" || return 1
  for ((attempt = 0; attempt < 20; attempt++)); do
    if demo_process_group_has_live_members "$pid"; then
      group_status=0
    else
      group_status=$?
    fi
    case "$group_status" in
      0)
        ;;
      1)
        return 0
        ;;
      *)
        return 1
        ;;
    esac
    sleep 0.1
  done
  return 1
}

demo_stop_owned_process_group() {
  local pid="$1"
  local witnesses
  local attempt
  local group_status

  demo_process_group_is_safe "$pid" || return 1
  witnesses="$(demo_capture_process_group_witnesses "$pid")" || return 1
  kill -TERM -- "-$pid" 2>/dev/null || true
  for ((attempt = 0; attempt < 20; attempt++)); do
    if demo_process_group_has_live_members "$pid"; then
      group_status=0
    else
      group_status=$?
    fi
    case "$group_status" in
      0)
        ;;
      1)
        return 0
        ;;
      *)
        return 1
        ;;
    esac
    sleep 0.1
  done
  demo_process_group_has_matching_witness "$pid" "$witnesses" || return 1
  kill -KILL -- "-$pid" 2>/dev/null || return 1
  for ((attempt = 0; attempt < 20; attempt++)); do
    if demo_process_group_has_live_members "$pid"; then
      group_status=0
    else
      group_status=$?
    fi
    case "$group_status" in
      0)
        ;;
      1)
        return 0
        ;;
      *)
        return 1
        ;;
    esac
    sleep 0.1
  done
  return 1
}

demo_terminate_owned_child() {
  local pid="$1"
  local owns_process_group="${2:-0}"
  local attempt
  local stat_line
  local state

  demo_valid_signal_pid "$pid" || return 1
  if [[ "$owns_process_group" == "1" ]] && demo_process_group_is_safe "$pid"; then
    if demo_stop_owned_process_group "$pid"; then
      wait "$pid" 2>/dev/null || true
      return 0
    fi
  fi
  if [[ -r "/proc/$pid/stat" ]]; then
    kill -TERM "$pid" 2>/dev/null || true
    for ((attempt = 0; attempt < 20; attempt++)); do
      [[ -r "/proc/$pid/stat" ]] || break
      stat_line="$(<"/proc/$pid/stat")"
      state="$(demo_proc_stat_state "$stat_line" 2>/dev/null || true)"
      [[ "$state" == "Z" || "$state" == "X" ]] && break
      sleep 0.1
    done
    if [[ -r "/proc/$pid/stat" ]]; then
      stat_line="$(<"/proc/$pid/stat")"
      state="$(demo_proc_stat_state "$stat_line" 2>/dev/null || true)"
      if [[ "$state" != "Z" && "$state" != "X" ]]; then
        kill -KILL "$pid" 2>/dev/null || true
      fi
    fi
  fi
  wait "$pid" 2>/dev/null || true
}

demo_file_sha256() {
  local file="$1"
  local output

  if command -v sha256sum >/dev/null 2>&1; then
    output="$(sha256sum -- "$file")" || return 1
  elif command -v shasum >/dev/null 2>&1; then
    output="$(shasum -a 256 -- "$file")" || return 1
  else
    demo_error "sha256sum or shasum is required to verify Grafana"
    return 1
  fi
  printf '%s\n' "${output%% *}"
}

demo_verify_sha256() {
  local file="$1"
  local expected="${2,,}"
  local actual

  demo_validate_sha256 "$expected" >/dev/null 2>&1 || return 1
  demo_regular_file_link_count_one "$file" || return 1
  actual="$(demo_file_sha256 "$file")" || return 1
  [[ "$actual" == "$expected" ]]
}

demo_decimal_leq() {
  local value="$1"
  local limit="$2"
  local LC_ALL=C

  [[ "$value" =~ ^[0-9]+$ && "$limit" =~ ^[0-9]+$ ]] || return 1
  while [[ ${#value} -gt 1 && "$value" == 0* ]]; do
    value="${value#0}"
  done
  while [[ ${#limit} -gt 1 && "$limit" == 0* ]]; do
    limit="${limit#0}"
  done
  ((${#value} < ${#limit})) ||
    { ((${#value} == ${#limit})) && [[ "$value" < "$limit" || "$value" == "$limit" ]]; }
}

demo_decimal_normalize() {
  local value="$1"

  [[ "$value" =~ ^[0-9]+$ ]] || return 1
  while [[ ${#value} -gt 1 && "$value" == 0* ]]; do
    value="${value#0}"
  done
  printf '%s\n' "$value"
}

demo_validate_file_size_cap() {
  local file="$1"
  local max_bytes="$2"
  local label="$3"
  local actual_bytes

  demo_regular_file_link_count_one "$file" || {
    demo_error "$label must be a regular file with exactly one link: $file"
    return 1
  }
  [[ "$max_bytes" =~ ^[1-9][0-9]*$ ]] || return 1
  actual_bytes="$(stat -c '%s' -- "$file")" || return 1
  [[ "$actual_bytes" =~ ^[0-9]+$ ]] || return 1
  if ! demo_decimal_leq "$actual_bytes" "$max_bytes"; then
    demo_error "$label exceeds the $max_bytes byte limit: $file"
    return 1
  fi
}

demo_capture_bounded_command_output() {
  local output="$1"
  local max_bytes="$2"
  local label="$3"
  local parent="${output%/*}"
  local probe_bytes
  local -a pipeline_status=()
  shift 3

  (($# > 0)) || return 1
  max_bytes="$(demo_decimal_normalize "$max_bytes")" || return 1
  [[ "$max_bytes" != "0" ]] || return 1
  demo_decimal_leq "$max_bytes" 9223372036854775807 || return 1
  demo_require_regular_output "$parent" "$output" "$label" || return 1

  if "$@" | {
    head -c "$max_bytes" >"$output" || exit 91
    probe_bytes="$(head -c 1 | wc -c)" || exit 92
    [[ "$probe_bytes" == "0" ]] || exit 90
  }; then
    pipeline_status=("${PIPESTATUS[@]}")
  else
    pipeline_status=("${PIPESTATUS[@]}")
  fi

  demo_validate_file_size_cap "$output" "$max_bytes" "$label" || return 1
  case "${pipeline_status[1]:-93}" in
    0)
      ;;
    90)
      demo_error "$label exceeds the $max_bytes byte limit"
      return 1
      ;;
    *)
      demo_error "could not capture $label safely"
      return 1
      ;;
  esac
  if [[ "${pipeline_status[0]:-1}" != "0" ]]; then
    demo_error "$label command failed"
    return 1
  fi
}

demo_validate_archive_metadata_file() {
  local metadata_file="$1"
  local max_entries="${2:-25000}"
  local max_member_bytes="${3:-629145600}"
  local max_total_bytes="${4:-2147483648}"
  local mode
  local owner
  local size
  local size_value
  local max_entries_value
  local max_member_value
  local max_total_value
  local remaining
  local rest
  local count=0
  local total=0

  demo_regular_file_link_count_one "$metadata_file" || return 1
  max_entries="$(demo_decimal_normalize "$max_entries")" || return 1
  max_member_bytes="$(demo_decimal_normalize "$max_member_bytes")" || return 1
  max_total_bytes="$(demo_decimal_normalize "$max_total_bytes")" || return 1
  [[ "$max_entries" != "0" && "$max_member_bytes" != "0" && "$max_total_bytes" != "0" ]] || return 1
  demo_decimal_leq "$max_entries" 9223372036854775807 || return 1
  demo_decimal_leq "$max_member_bytes" 9223372036854775807 || return 1
  demo_decimal_leq "$max_total_bytes" 9223372036854775807 || return 1
  max_entries_value=$((10#$max_entries))
  max_member_value=$((10#$max_member_bytes))
  max_total_value=$((10#$max_total_bytes))

  while read -r mode owner size rest; do
    [[ -n "$mode" ]] || continue
    count=$((count + 1))
    if ((count > max_entries_value)); then
      demo_error "Grafana archive contains more than $max_entries members"
      return 1
    fi
    case "$mode" in
      d* | -*)
        ;;
      *)
        demo_error "Grafana archive contains links or special files; refusing extraction"
        return 1
        ;;
    esac
    [[ "$owner" == */* && "$size" =~ ^[0-9]+$ ]] || {
      demo_error "Grafana archive metadata has an unexpected format"
      return 1
    }
    size="$(demo_decimal_normalize "$size")" || return 1
    if ! demo_decimal_leq "$size" "$max_member_value"; then
      demo_error "Grafana archive member exceeds the $max_member_bytes byte limit"
      return 1
    fi
    remaining=$((max_total_value - total))
    if ! demo_decimal_leq "$size" "$remaining"; then
      demo_error "Grafana archive expands beyond the $max_total_bytes byte aggregate limit"
      return 1
    fi
    size_value=$((10#$size))
    total=$((total + size_value))
  done <"$metadata_file"

  ((count > 0)) || {
    demo_error "Grafana archive metadata is empty"
    return 1
  }
  printf '%s\n' "$count"
}

demo_validate_grafana_archive() {
  local archive="$1"
  local expected_top="$2"
  local work_dir="$3"
  local entries_file="$work_dir/archive-entries.txt"
  local verbose_file="$work_dir/archive-verbose.txt"
  local entry
  local normalized
  local found=0
  local entry_count=0
  local metadata_count
  local listing_cap=67108864

  demo_validate_file_size_cap "$archive" 536870912 "Grafana archive" || return 1
  [[ "$expected_top" =~ ^grafana-[0-9A-Za-z.+-]+$ ]] || {
    demo_error "Invalid expected Grafana archive directory: $expected_top"
    return 1
  }
  command -v timeout >/dev/null 2>&1 || {
    demo_error "timeout is required to validate the Grafana archive"
    return 1
  }
  demo_require_real_directory "$work_dir" "Grafana archive validation work directory" || return 1
  if ! demo_capture_bounded_command_output \
    "$entries_file" \
    "$listing_cap" \
    "Grafana archive entry listing" \
    env -u TAR_OPTIONS -u GZIP LC_ALL=C \
    timeout --foreground --kill-after=5s 60s \
    tar --quoting-style=escape -tzf "$archive"; then
    demo_error "Grafana archive cannot be listed"
    return 1
  fi
  if ! demo_capture_bounded_command_output \
    "$verbose_file" \
    "$listing_cap" \
    "Grafana archive metadata listing" \
    env -u TAR_OPTIONS -u GZIP LC_ALL=C \
    timeout --foreground --kill-after=5s 60s \
    tar --numeric-owner --full-time --quoting-style=escape -tvzf "$archive"; then
    demo_error "Grafana archive metadata cannot be listed"
    return 1
  fi
  metadata_count="$(
    demo_validate_archive_metadata_file "$verbose_file" 25000 629145600 2147483648
  )" || return 1

  while IFS= read -r entry; do
    [[ -n "$entry" ]] || continue
    entry_count=$((entry_count + 1))
    if ((entry_count > 25000)); then
      demo_error "Grafana archive contains more than 25000 members"
      return 1
    fi
    normalized="${entry%/}"
    if [[ "$normalized" == *\\* ||
      "$normalized" == /* ||
      "$normalized" == *//* ||
      "$normalized" =~ (^|/)\.\.?($|/) ||
      "$normalized" =~ [[:cntrl:]] ]]; then
      demo_error "Grafana archive contains an escaped, control, or unsafe path: $entry"
      return 1
    fi
    case "$normalized" in
      "$expected_top" | "$expected_top"/*)
        found=1
        ;;
      *)
        demo_error "Grafana archive has an unexpected top-level entry: $entry"
        return 1
        ;;
    esac
  done <"$entries_file"

  if ((found == 0)); then
    demo_error "Grafana archive is empty"
    return 1
  fi
  if [[ "$metadata_count" != "$entry_count" ]]; then
    demo_error "Grafana archive member listings are inconsistent"
    return 1
  fi
}

demo_extract_verified_grafana_archive() {
  local archive="$1"
  local expected_top="$2"
  local work_dir="$3"
  local extract_dir="$work_dir/extract"
  local candidate="$extract_dir/$expected_top"

  demo_validate_grafana_archive "$archive" "$expected_top" "$work_dir" || return 1
  if [[ -e "$extract_dir" || -L "$extract_dir" ]]; then
    demo_error "Grafana extraction directory already exists: $extract_dir"
    return 1
  fi
  mkdir -- "$extract_dir" || return 1
  chmod 700 -- "$extract_dir" || return 1
  if ! timeout --foreground --kill-after=10s 120s \
    env -u TAR_OPTIONS -u GZIP \
    tar \
    --extract \
    --gzip \
    --file="$archive" \
    --directory="$extract_dir" \
    --no-same-owner \
    --no-same-permissions; then
    demo_error "verified Grafana archive extraction failed"
    return 1
  fi
  if [[ ! -d "$candidate" || -L "$candidate" ||
    ! -d "$candidate/bin" || -L "$candidate/bin" ||
    ! -x "$candidate/bin/grafana" ]] ||
    ! demo_regular_file_link_count_one "$candidate/bin/grafana"; then
    demo_error "verified Grafana archive does not contain the expected executable"
    return 1
  fi
  printf '%s\n' "$candidate"
}

demo_require_grafana_install_shape() {
  local install_dir="$1"

  demo_require_real_directory "$install_dir" "Grafana installation" || return 1
  demo_require_real_directory "$install_dir/bin" "Grafana binary directory" || return 1
  if [[ ! -x "$install_dir/bin/grafana" ]] ||
    ! demo_regular_file_link_count_one "$install_dir/bin/grafana"; then
    demo_error "Grafana installation does not contain a safe executable: $install_dir/bin/grafana"
    return 1
  fi
}

demo_grafana_tree_fingerprint() {
  local install_dir="$1"

  demo_require_grafana_install_shape "$install_dir" || return 1
  command -v timeout >/dev/null 2>&1 || {
    demo_error "timeout is required to fingerprint the Grafana installation"
    return 1
  }
  command -v node >/dev/null 2>&1 || {
    demo_error "node is required to fingerprint the Grafana installation"
    return 1
  }
  env -u NODE_OPTIONS -u NODE_PATH \
    timeout --foreground --kill-after=10s 120s \
    node - "$install_dir" <<'NODE'
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

const [root] = process.argv.slice(2);
const MAX_ENTRIES = 25000;
const MAX_MEMBER_BYTES = 629145600;
const MAX_TOTAL_BYTES = 2147483648;
const STATE_FILE = '.asyncq-demo-install-state';

function fingerprintTree(root) {
  const entries = [];
  let totalBytes = 0;
  const pending = [''];
  while (pending.length !== 0) {
    const relativeDirectory = pending.pop();
    const absoluteDirectory = path.join(root, relativeDirectory);
    const names = fs.readdirSync(absoluteDirectory).sort();
    for (const name of names) {
      const relative = relativeDirectory === ''
        ? name
        : `${relativeDirectory}/${name}`;
      if (relative === STATE_FILE) continue;
      const absolute = path.join(root, relative);
      const stat = fs.lstatSync(absolute);
      if (stat.isSymbolicLink() || (!stat.isDirectory() && !stat.isFile())) {
        throw new Error('unsupported cached-install entry');
      }
      entries.push(stat.isDirectory() ? `d\0${relative}` : `f\0${relative}`);
      if (entries.length > MAX_ENTRIES) throw new Error('too many entries');
      if (stat.isDirectory()) {
        pending.push(relative);
      } else {
        if (stat.nlink !== 1) throw new Error('linked cached-install file');
        if (stat.size > MAX_MEMBER_BYTES) {
          throw new Error('cached-install member exceeds byte cap');
        }
        totalBytes += stat.size;
        if (!Number.isSafeInteger(totalBytes) || totalBytes > MAX_TOTAL_BYTES) {
          throw new Error('cached install exceeds byte cap');
        }
        const digestState = crypto.createHash('sha256');
        const descriptor = fs.openSync(absolute, 'r');
        const buffer = Buffer.allocUnsafe(1024 * 1024);
        try {
          for (;;) {
            const bytesRead = fs.readSync(
              descriptor,
              buffer,
              0,
              buffer.length,
              null
            );
            if (bytesRead === 0) break;
            digestState.update(buffer.subarray(0, bytesRead));
          }
        } finally {
          fs.closeSync(descriptor);
        }
        const digest = digestState.digest('hex');
        entries[entries.length - 1] += `\0${stat.size}\0${digest}`;
      }
    }
  }
  const treeDigest = crypto.createHash('sha256');
  for (const entry of entries.sort()) {
    const encoded = Buffer.from(entry);
    const length = Buffer.allocUnsafe(8);
    length.writeBigUInt64BE(BigInt(encoded.length));
    treeDigest.update(length);
    treeDigest.update(encoded);
  }
  return treeDigest.digest('hex');
}

try {
  process.stdout.write(`${fingerprintTree(root)}\n`);
} catch {
  process.exit(1);
}
NODE
}

demo_read_grafana_install_state() {
  local install_dir="$1"
  local expected_archive_sha="${2,,}"
  local state_file="$install_dir/.asyncq-demo-install-state"
  local prefix
  local contents
  local expected_size
  local mode
  local tree_sha

  demo_validate_sha256 "$expected_archive_sha" >/dev/null 2>&1 || return 1
  demo_require_grafana_install_shape "$install_dir" || return 1
  demo_regular_file_link_count_one "$state_file" || return 1
  mode="$(stat -c '%a' -- "$state_file")" || return 1
  [[ "$mode" == "600" ]] || {
    demo_error "Grafana install state must have mode 0600: $state_file"
    return 1
  }
  prefix=$'asyncq-demo-install-v1\narchive-sha256='"$expected_archive_sha"$'\ntree-sha256='
  expected_size=$((${#prefix} + 64 + 1))
  [[ "$(stat -c '%s' -- "$state_file")" == "$expected_size" ]] || {
    demo_error "Grafana install state is malformed or does not match the trusted archive"
    return 1
  }
  contents="$(<"$state_file")" || return 1
  [[ "$contents" == "$prefix"* ]] || {
    demo_error "Grafana install state is malformed or does not match the trusted archive"
    return 1
  }
  tree_sha="${contents#"$prefix"}"
  demo_validate_sha256 "$tree_sha" >/dev/null 2>&1 || {
    demo_error "Grafana install state contains an invalid tree fingerprint"
    return 1
  }
  printf '%s\n' "${tree_sha,,}"
}

demo_validate_grafana_install_state() {
  local install_dir="$1"
  local expected_archive_sha="$2"
  local recorded_tree_sha
  local actual_tree_sha

  recorded_tree_sha="$(
    demo_read_grafana_install_state "$install_dir" "$expected_archive_sha"
  )" || return 1
  actual_tree_sha="$(demo_grafana_tree_fingerprint "$install_dir")" || {
    demo_error "could not fingerprint the cached Grafana installation"
    return 1
  }
  if [[ "$actual_tree_sha" != "$recorded_tree_sha" ]]; then
    demo_error "cached Grafana installation differs from its verified install state"
    return 1
  fi
  printf '%s\n' "$actual_tree_sha"
}

demo_write_grafana_install_state() {
  local install_dir="$1"
  local expected_archive_sha="${2,,}"
  local state_file="$install_dir/.asyncq-demo-install-state"
  local temporary=""
  local tree_sha

  demo_validate_sha256 "$expected_archive_sha" >/dev/null 2>&1 || return 1
  demo_require_grafana_install_shape "$install_dir" || return 1
  if [[ -e "$state_file" || -L "$state_file" ]]; then
    demo_error "verified Grafana archive uses the reserved install-state path"
    return 1
  fi
  tree_sha="$(demo_grafana_tree_fingerprint "$install_dir")" || {
    demo_error "could not fingerprint the verified Grafana candidate"
    return 1
  }
  temporary="$(mktemp "$install_dir/.asyncq-demo-install-state.XXXXXX")" || return 1
  if ! printf '%s\n' \
    "asyncq-demo-install-v1" \
    "archive-sha256=$expected_archive_sha" \
    "tree-sha256=$tree_sha" >"$temporary" ||
    ! chmod 600 -- "$temporary" ||
    ! demo_regular_file_link_count_one "$temporary" ||
    ! ln -- "$temporary" "$state_file" ||
    ! rm -- "$temporary"; then
    [[ -n "$temporary" && -e "$temporary" && ! -L "$temporary" ]] &&
      rm -f -- "$temporary"
    return 1
  fi
  if [[ "$(demo_read_grafana_install_state "$install_dir" "$expected_archive_sha")" != "$tree_sha" ]]; then
    demo_error "could not create a safe Grafana install state"
    return 1
  fi
  printf '%s\n' "$tree_sha"
}

demo_write_trusted_grafana_config() {
  local runtime_dir="$1"
  local config_file="$2"
  local temporary

  demo_require_regular_output \
    "$runtime_dir" \
    "$config_file" \
    "trusted Grafana configuration" || return 1
  temporary="$(mktemp "$runtime_dir/.grafana-config.XXXXXX")" || return 1
  if ! printf '%s\n' \
    '# AsyncQ local demo configuration anchor.' \
    '# Runtime values are supplied by the launchers through a clean environment.' \
    >"$temporary" ||
    ! chmod 600 -- "$temporary" ||
    ! mv -f -- "$temporary" "$config_file"; then
    rm -f -- "$temporary"
    return 1
  fi
  demo_regular_file_link_count_one "$config_file"
}

demo_validate_local_docker_host() {
  local value="$1"
  local socket_path
  local normalized

  [[ "$value" == unix:///* && ! "$value" =~ [[:cntrl:]] ]] || {
    demo_error "Docker Engine must use a local unix:/// endpoint"
    return 1
  }
  socket_path="${value#unix://}"
  [[ "$socket_path" == /* && "$socket_path" != *//* &&
    "$socket_path" != */../* && "$socket_path" != */./* &&
    "$socket_path" != */.. && "$socket_path" != */. ]] || {
    demo_error "Docker Engine Unix socket path must be canonical"
    return 1
  }
  normalized="$(readlink -m -- "$socket_path")" || return 1
  [[ "$normalized" == /* && ! "$normalized" =~ [[:cntrl:]] ]] || return 1
  printf 'unix://%s\n' "$normalized"
}

demo_canonical_environment_directory() {
  local name="$1"
  local value="$2"
  local resolved

  [[ "$name" == "QHOME" || "$name" == "QLIC" ]] || return 1
  [[ "$value" == /* && ! "$value" =~ [[:cntrl:]] ]] || {
    demo_error "$name must be an absolute directory path"
    return 1
  }
  resolved="$(readlink -f -- "$value")" || {
    demo_error "$name does not resolve to an existing directory"
    return 1
  }
  demo_require_real_directory "$resolved" "$name directory" || return 1
  printf '%s\n' "$resolved"
}

demo_safe_remove_install_workdir() {
  local runtime_dir="$1"
  local work_dir="$2"

  [[ -n "$work_dir" ]] || return 0
  demo_validate_private_temp_directory \
    "$runtime_dir" \
    "$work_dir" \
    "Grafana install work directory" || return 1
  rm -rf -- "$work_dir"
}

demo_safe_remove_recovery_directory() {
  local runtime_dir="$1"
  local recovery_dir="$2"

  demo_validate_private_temp_directory \
    "$runtime_dir" \
    "$recovery_dir" \
    "Grafana recovery directory" || return 1
  rm -rf -- "$recovery_dir"
}

demo_safe_remove_q_session_directory() {
  local state_dir="$1"
  local session_dir="$2"

  demo_validate_private_temp_directory \
    "$state_dir" \
    "$session_dir" \
    "q session directory" || return 1
  rmdir -- "$session_dir"
}

demo_move_path() {
  mv -- "$1" "$2"
}

demo_replace_install_directory() {
  local runtime_dir="$1"
  local install_dir="$2"
  local candidate_dir="$3"
  local recovery_dir=""

  demo_require_real_directory "$runtime_dir" "Grafana runtime directory" || return 1
  [[ "${install_dir%/*}" == "$runtime_dir" ]] || return 1
  demo_require_real_directory "$candidate_dir" "verified Grafana install candidate" || return 1

  if [[ -e "$install_dir" || -L "$install_dir" ]]; then
    demo_require_real_directory "$install_dir" "previous Grafana installation" || return 1
    recovery_dir="$(mktemp -d "$runtime_dir/.grafana-recovery.XXXXXX")" || return 1
    demo_validate_private_temp_directory \
      "$runtime_dir" \
      "$recovery_dir" \
      "Grafana recovery directory" || return 1
    rmdir -- "$recovery_dir" || return 1
    if ! demo_move_path "$install_dir" "$recovery_dir"; then
      demo_error "could not preserve the previous Grafana installation"
      return 1
    fi
  fi

  if demo_move_path "$candidate_dir" "$install_dir"; then
    if [[ -n "$recovery_dir" ]]; then
      demo_safe_remove_recovery_directory "$runtime_dir" "$recovery_dir" || return 1
    fi
    return 0
  fi

  if [[ -n "$recovery_dir" ]]; then
    if demo_move_path "$recovery_dir" "$install_dir"; then
      demo_error "could not install the new Grafana candidate; the previous installation was restored"
    else
      demo_error "could not install the new Grafana candidate or restore the previous installation; recovery preserved at $recovery_dir"
    fi
  else
    demo_error "could not install the new Grafana candidate"
  fi
  return 1
}

demo_write_dashboard_provider_config() {
  local parent="$1"
  local output="$2"
  local root_dir="$3"
  local temporary

  demo_require_regular_output "$parent" "$output" "generated dashboard provisioning" || return 1
  temporary="$(mktemp "$parent/.dashboards-config.XXXXXX")" || return 1
  if ! demo_node - "$root_dir" >"$temporary" <<'NODE'
const rootDir = process.argv[2];
if (/[\u0000-\u001f\u007f]/u.test(rootDir)) {
  process.exit(1);
}
const config = {
  apiVersion: 1,
  providers: [{
    name: 'AsyncQ Demo',
    orgId: 1,
    folder: 'AsyncQ',
    type: 'file',
    disableDeletion: false,
    allowUiUpdates: true,
    updateIntervalSeconds: 5,
    options: {
      path: `${rootDir}/demo/grafana/provisioning/dashboards/json`,
    },
  }],
};
process.stdout.write(`${JSON.stringify(config, null, 2)}\n`);
NODE
  then
    rm -f -- "$temporary"
    return 1
  fi
  if ! chmod 600 -- "$temporary" || ! mv -f -- "$temporary" "$output"; then
    rm -f -- "$temporary"
    return 1
  fi
  demo_regular_file_link_count_one "$output"
}
