# SPDX-License-Identifier: Apache-2.0
# shellcheck shell=bash
# Shared port / URL resolution for Nodra scripts.
#
# Resolution order for the control-plane listen port:
#   1. --port N / PORT_FROM_CLI
#   2. NODRA_PORT
#   3. PORT from .deploy-last
#   4. optional default (caller supplies; else empty)
#
# Host resolution:
#   1. NODRA_HOST
#   2. HOST from .deploy-last

nodra_load_last() {
  local root="${1:-.}"
  NODRA_LAST_HOST=""
  NODRA_LAST_PORT=""
  if [ -f "$root/.deploy-last" ]; then
    # shellcheck disable=SC1091
    source "$root/.deploy-last"
    NODRA_LAST_HOST="${HOST:-}"
    NODRA_LAST_PORT="${PORT:-}"
  fi
}

nodra_resolve_port() {
  # args: cli_port [default]
  local cli="${1:-}"
  local def="${2:-}"
  if [ -n "$cli" ]; then
    printf '%s\n' "$cli"
    return
  fi
  if [ -n "${NODRA_PORT:-}" ]; then
    printf '%s\n' "$NODRA_PORT"
    return
  fi
  if [ -n "${NODRA_LAST_PORT:-}" ]; then
    printf '%s\n' "$NODRA_LAST_PORT"
    return
  fi
  printf '%s\n' "$def"
}

nodra_resolve_host() {
  if [ -n "${NODRA_HOST:-}" ]; then
    printf '%s\n' "$NODRA_HOST"
    return
  fi
  printf '%s\n' "${NODRA_LAST_HOST:-}"
}

nodra_resolve_base_url() {
  # args: root [cli_port]
  local root="${1:-.}"
  local cli="${2:-}"
  if [ -n "${NODRA_URL:-}" ]; then
    printf '%s\n' "${NODRA_URL%/}"
    return
  fi
  nodra_load_last "$root"
  local port host
  port="$(nodra_resolve_port "$cli" "")"
  host="$(nodra_resolve_host)"
  if [ -n "$host" ] && [ -n "$port" ]; then
    printf 'http://%s:%s\n' "$host" "$port"
    return
  fi
  return 1
}

nodra_validate_port() {
  local p="$1"
  case "$p" in
    ''|*[!0-9]*) return 1 ;;
  esac
  [ "$p" -ge 1 ] && [ "$p" -le 65535 ]
}
