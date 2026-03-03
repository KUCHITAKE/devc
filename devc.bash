#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# devc - Launch a devcontainer with Neovim, Claude Code, and ripgrep
#
# Features are injected via --additional-features so existing devcontainer.json
# configs work as-is. Ports from forwardPorts/appPort (which don't work in CLI)
# are auto-converted to runArgs -p.
# ==============================================================================

DOTFILES_DIR="/opt/devc-dotfiles"

ADDITIONAL_FEATURES='{
  "ghcr.io/duduribeiro/devcontainer-features/neovim:1": { "version": "nightly" },
  "ghcr.io/anthropics/devcontainer-features/claude-code:1": {},
  "ghcr.io/jungaretti/features/ripgrep:1": {},
  "ghcr.io/devcontainers/features/github-cli:1": {}
}'

HOST_MOUNTS=(
  "${HOME}/.config/nvim:${DOTFILES_DIR}/config-nvim"
  "${HOME}/.claude:${DOTFILES_DIR}/claude"
  "${HOME}/.claude.json:${DOTFILES_DIR}/claude.json"
  "${HOME}/.ssh:${DOTFILES_DIR}/ssh"
  "/tmp/devc-credentials:/tmp/devc-credentials"
  # Obsidian vault - MCP config references this absolute path
  "${HOME}/work:${HOME}/work"
)

# --- Shared helpers -----------------------------------------------------------

usage() {
  cat << 'USAGE'
Usage: devc <command> [options] [workspace-dir]

Launch a devcontainer with Neovim (nightly), Claude Code, and ripgrep.

Commands:
  up [opts] [dir]   Start the devcontainer and enter it (default)
  down [dir]        Stop the devcontainer (volumes are preserved)
  clean [dir]       Remove containers and volumes (fresh DB, etc.)
  rebuild [dir]     Rebuild and enter (alias for up --rebuild)
  help              Show this help

Options (up):
  -p PORT           Publish port (e.g. -p 3000:3000). Repeatable.
  --rebuild         Rebuild the container from scratch

Examples:
  devc ~/project
  devc up -p 3000:3000 -p 5173:5173 ~/project
  devc rebuild .
  devc down ~/project
  devc clean ~/project

Ports defined in forwardPorts/appPort are auto-converted to runArgs.
USAGE
  exit 0
}

resolve_workspace() {
  local dir="${1:-.}"
  WORKSPACE="$(cd "$dir" && pwd)"
  PROJECT_NAME="$(basename "$WORKSPACE")"
}

compose_files() {
  local dc_json="$WORKSPACE/.devcontainer/devcontainer.json"
  local files=()
  if [[ -f "$dc_json" ]]; then
    while IFS= read -r f; do
      files+=(-f "$WORKSPACE/.devcontainer/$f")
    done < <(jq -r '.dockerComposeFile // [] | if type == "array" then .[] else . end' "$dc_json")
  fi
  printf '%s\n' "${files[@]}"
}

compose_project() {
  echo "${PROJECT_NAME}_devcontainer"
}

read_compose_files() {
  COMPOSE_FILES=()
  while IFS= read -r arg; do
    [[ -n "$arg" ]] && COMPOSE_FILES+=("$arg")
  done < <(compose_files)
}

extract_credentials() {
  mkdir -p /tmp/devc-credentials
  git config --global user.name  > /tmp/devc-credentials/git-user-name  2>/dev/null || true
  git config --global user.email > /tmp/devc-credentials/git-user-email 2>/dev/null || true
  gh auth token                  > /tmp/devc-credentials/gh-token        2>/dev/null || true
}

ensure_devcontainer_json() {
  local devcontainer_dir="$WORKSPACE/.devcontainer"
  DEVCONTAINER_JSON="$devcontainer_dir/devcontainer.json"

  if [ ! -f "$DEVCONTAINER_JSON" ]; then
    mkdir -p "$devcontainer_dir"
    cat > "$DEVCONTAINER_JSON" << EOF
{
  "name": "${PROJECT_NAME}",
  "image": "mcr.microsoft.com/devcontainers/base:ubuntu"
}
EOF
    echo "==> Generated: $DEVCONTAINER_JSON"
  fi
}

collect_ports() {
  local json_ports
  json_ports=$(jq -r \
    '[(.forwardPorts // [])[], (.appPort // [])[]] | map(tostring)[]' \
    "$DEVCONTAINER_JSON" 2>/dev/null || true)

  while IFS= read -r p; do
    [[ -n "$p" ]] && PORTS+=("$p")
  done <<< "$json_ports"
}

build_config_arg() {
  CONFIG_ARG=""
  if [[ ${#PORTS[@]} -gt 0 ]]; then
    local devcontainer_dir
    devcontainer_dir="$(dirname "$DEVCONTAINER_JSON")"
    MERGED_CONFIG="$devcontainer_dir/.devcontainer.json"
    trap 'rm -f "$MERGED_CONFIG"' EXIT

    local port_args
    port_args=$(printf '%s\n' "${PORTS[@]}" | jq -R . | jq -s '[.[] as $p | "-p", $p]')
    jq --argjson portArgs "$port_args" \
      '.runArgs = (.runArgs // []) + $portArgs' \
      "$DEVCONTAINER_JSON" > "$MERGED_CONFIG"

    CONFIG_ARG="--config $MERGED_CONFIG"
    echo "==> Ports: ${PORTS[*]}"
  fi
}

build_mount_args() {
  MOUNT_ARGS=()
  for m in "${HOST_MOUNTS[@]}"; do
    src="${m%%:*}"
    rest="${m#*:}"
    tgt="${rest%%:*}"
    [[ -e "$src" ]] && MOUNT_ARGS+=(--mount "type=bind,source=${src},target=${tgt}")
  done
}

setup_container() {
  local container_id="$1"
  local remote_user="$2"

  _exec() { docker exec -u "$remote_user" "$container_id" "$@"; }

  local remote_home
  remote_home=$(_exec sh -c 'echo $HOME')
  echo "==> Remote home: ${remote_home}"

  _exec mkdir -p "${remote_home}/.config"
  _exec ln -sfn "${DOTFILES_DIR}/config-nvim" "${remote_home}/.config/nvim"
  _exec ln -sfn "${DOTFILES_DIR}/claude"       "${remote_home}/.claude"
  _exec ln -sfn "${DOTFILES_DIR}/claude.json"  "${remote_home}/.claude.json"
  _exec ln -sfn "${DOTFILES_DIR}/ssh"          "${remote_home}/.ssh"

  if [[ -f /tmp/devc-credentials/git-user-name ]]; then
    _exec git config --global user.name "$(cat /tmp/devc-credentials/git-user-name)" 2>/dev/null || true
  fi
  if [[ -f /tmp/devc-credentials/git-user-email ]]; then
    _exec git config --global user.email "$(cat /tmp/devc-credentials/git-user-email)" 2>/dev/null || true
  fi
  if [[ -f /tmp/devc-credentials/gh-token ]]; then
    _exec bash -c 'gh auth login --with-token < /tmp/devc-credentials/gh-token && gh auth setup-git' 2>/dev/null || true
  fi
}

# --- Subcommand functions -----------------------------------------------------

cmd_up() {
  local rebuild=""
  PORTS=()
  local workspace_arg="."

  while [[ $# -gt 0 ]]; do
    case $1 in
      -p) PORTS+=("$2"); shift 2 ;;
      --rebuild) rebuild="--remove-existing-container"; shift ;;
      -*) echo "Unknown option: $1" >&2; exit 1 ;;
      *) workspace_arg="$1"; shift ;;
    esac
  done

  resolve_workspace "$workspace_arg"
  extract_credentials
  ensure_devcontainer_json
  collect_ports
  build_config_arg
  build_mount_args

  echo "==> Starting devcontainer for ${PROJECT_NAME}..."
  local up_output
  up_output=$(devcontainer up \
    --workspace-folder "$WORKSPACE" \
    --additional-features "$ADDITIONAL_FEATURES" \
    "${MOUNT_ARGS[@]}" \
    $rebuild $CONFIG_ARG)

  local container_id
  container_id=$(echo "$up_output" | jq -r '.containerId // empty')
  if [[ -z "$container_id" ]]; then
    echo "$up_output" >&2
    echo "Error: Failed to start devcontainer" >&2
    exit 1
  fi

  local remote_user remote_workspace
  remote_user=$(echo "$up_output" | jq -r '.remoteUser // "vscode"')
  remote_workspace=$(echo "$up_output" | jq -r '.remoteWorkspaceFolder // "/workspaces/'"${PROJECT_NAME}"'"')
  echo "==> Entering container ${container_id:0:12} as ${remote_user}..."

  setup_container "$container_id" "$remote_user"

  exec docker exec -it -u "$remote_user" -w "$remote_workspace" "$container_id" bash -l
}

cmd_down() {
  resolve_workspace "${1:-.}"
  read_compose_files
  local project
  project="$(compose_project)"

  if [[ ${#COMPOSE_FILES[@]} -gt 0 ]]; then
    echo "==> Stopping containers for ${project}..."
    docker compose -p "$project" "${COMPOSE_FILES[@]}" down
  else
    echo "==> Stopping container ${project}..."
    docker stop "$project" 2>/dev/null || true
  fi

  echo "==> Down complete."
}

cmd_clean() {
  resolve_workspace "${1:-.}"
  read_compose_files
  local project
  project="$(compose_project)"

  if [[ ${#COMPOSE_FILES[@]} -gt 0 ]]; then
    echo "==> Removing containers and volumes for ${project}..."
    docker compose -p "$project" "${COMPOSE_FILES[@]}" down -v
  else
    echo "==> No docker-compose config found; removing container only..."
    docker rm -f "$project" 2>/dev/null || true
  fi

  echo "==> Clean complete."
}

cmd_rebuild() {
  cmd_up --rebuild "$@"
}

cmd_help() {
  usage
}

# --- Dispatch -----------------------------------------------------------------

case "${1:-}" in
  up|down|clean|rebuild|help) SUBCOMMAND="$1"; shift ;;
  --clean)                    SUBCOMMAND="clean"; shift ;;
  -h|--help)                  SUBCOMMAND="help" ;;
  *)                          SUBCOMMAND="up" ;;  # don't shift — arg is for up
esac

"cmd_${SUBCOMMAND}" "$@"
