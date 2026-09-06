#!/usr/bin/env bash
# Copyright 2026 Zyvor
# SPDX-License-Identifier: Apache-2.0
# ============================================================================
# deploy-remote.sh — Deploy Nodra control plane to a remote host (systemd)
# ============================================================================
# nodra-server builds as a static binary (CGO_ENABLED=0):
#   1. Detect remote OS/arch over SSH
#   2. Cross-compile ./cmd/nodra-server (+ nodractl) locally
#   3. Install binary, env, data dir, systemd unit
#   4. Enable/start nodra-server.service, open firewall port
#   5. Verify with scripts/smoke-remote.sh
#
# Usage:
#   ./scripts/deploy-remote.sh <host> [user] [password] [options]
#   ./scripts/deploy-remote.sh 212.8.248.187 sus
#   ./scripts/deploy-remote.sh 212.8.248.187 sus --port 18447
#   NODRA_PORT=18447 ./scripts/deploy-remote.sh 212.8.248.187 sus
#   ./scripts/deploy-remote.sh 212.8.248.187 sus --uninstall
#
# Options:
#   --port N      Listen/health-check TCP port (overrides env and .deploy-last)
#   --uninstall   Stop nodra-server.service and remove binary + unit
#   --dry-run     Print what would happen; make no changes
#   --skip-smoke  Skip smoke-remote.sh
#
# Port resolution (first match wins):
#   1. --port N
#   2. NODRA_PORT env
#   3. PORT from .deploy-last
#   4. random in 18000–28999
#
# Tokens (optional; defaults generated once and reused from remote env):
#   NODRA_ADMIN_TOKEN, NODRA_ENROLLMENT_TOKEN
# ============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=lib/deploy-common.sh
source "$SCRIPT_DIR/lib/deploy-common.sh"

info()  { nodra_info "$@"; }
warn()  { nodra_warn "$@"; }
error() { nodra_error "$@"; }
step()  { LAST_ACTION="$*"; deploy_ui_step_start "$*"; }

REMOTE_BIN=/usr/local/bin/nodra-server
REMOTE_CTL=/usr/local/bin/nodractl
REMOTE_DIR=/etc/nodra
REMOTE_ENV=/etc/nodra/nodra.env
REMOTE_DATA=/var/lib/nodra
REMOTE_UNIT=/etc/systemd/system/nodra-server.service

UNINSTALL_MODE=false
DRY_RUN=false
SKIP_SMOKE=false
PORT_FROM_CLI=""
POSITIONAL=()
while [ $# -gt 0 ]; do
    case "$1" in
        --port)
            [ $# -ge 2 ] || error "--port requires a value"
            PORT_FROM_CLI="$2"
            shift 2
            ;;
        --port=*)
            PORT_FROM_CLI="${1#*=}"
            shift
            ;;
        --uninstall)  UNINSTALL_MODE=true; shift ;;
        --dry-run)    DRY_RUN=true; shift ;;
        --skip-smoke) SKIP_SMOKE=true; shift ;;
        --help|-h)
            sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        --)
            shift
            POSITIONAL+=("$@")
            break
            ;;
        -*)
            error "Unknown option: $1 (see --help)"
            ;;
        *)
            POSITIONAL+=("$1")
            shift
            ;;
    esac
done

HOST="${POSITIONAL[0]:-${DEPLOY_HOST:-}}"
USER="${POSITIONAL[1]:-${DEPLOY_USER:-root}}"
PASS="${POSITIONAL[2]:-${DEPLOY_PASS:-}}"

nodra_parse_target HOST USER
LAST_PORT=""
if [ -z "$HOST" ] && nodra_load_deploy_last "$REPO_DIR"; then
    info "Using .deploy-last → ${USER}@${HOST}"
    LAST_PORT="${PORT:-}"
elif [ -f "$REPO_DIR/.deploy-last" ]; then
    LAST_PORT="$(awk -F= '/^PORT=/ {print $2; exit}' "$REPO_DIR/.deploy-last")"
fi
[ -z "$HOST" ] && error "Usage: $0 <host> [user] [password] [options]  (see --help)"

if [ -n "$PORT_FROM_CLI" ]; then
    NODRA_PORT="$PORT_FROM_CLI"
elif [ -n "${NODRA_PORT:-}" ]; then
    :
elif [ -n "$LAST_PORT" ]; then
    NODRA_PORT="$LAST_PORT"
    info "Reusing port ${NODRA_PORT} from .deploy-last"
else
    NODRA_PORT=$((18000 + RANDOM % 11000))
    info "Selected random port ${NODRA_PORT}"
fi
case "$NODRA_PORT" in
    ''|*[!0-9]*) error "Invalid port: ${NODRA_PORT}" ;;
esac
if [ "$NODRA_PORT" -lt 1 ] || [ "$NODRA_PORT" -gt 65535 ]; then
    error "Port out of range: ${NODRA_PORT}"
fi

[ -f "$REPO_DIR/go.mod" ] || error "Not in the nodra repo: $REPO_DIR"
[ -d "$REPO_DIR/cmd/nodra-server" ] || error "cmd/nodra-server missing"
nodra_build_metadata "$REPO_DIR"
DEPLOY_UI_PORT="$NODRA_PORT"

SUDO=""
[ "$USER" != "root" ] && SUDO="sudo"

DEPLOY_SSH_OPTS=(
    -o StrictHostKeyChecking=no
    -o ConnectTimeout=15
    -o ServerAliveInterval=15
    -o ServerAliveCountMax=8
)
DEPLOY_SSH_TTY_OPTS=()
[ "$USER" != "root" ] && DEPLOY_SSH_TTY_OPTS=(-tt)

if [ -n "$PASS" ] && ! command -v sshpass &>/dev/null; then
    error "sshpass required for password auth"
fi

_ssh() {
    local -a ssh_args=("${DEPLOY_SSH_OPTS[@]}" "${DEPLOY_SSH_TTY_OPTS[@]}")
    if [ -n "$PASS" ]; then
        SSHPASS="$PASS" sshpass -e ssh "${ssh_args[@]}" "${USER}@${HOST}" "$@"
    else
        ssh "${ssh_args[@]}" "${USER}@${HOST}" "$@"
    fi
}

_ssh_batch() {
    local -a ssh_args=("${DEPLOY_SSH_OPTS[@]}")
    if [ -n "$PASS" ]; then
        SSHPASS="$PASS" sshpass -e ssh "${ssh_args[@]}" "${USER}@${HOST}" "$@"
    else
        ssh "${ssh_args[@]}" "${USER}@${HOST}" "$@"
    fi
}

_scp() {
    local -a scp_args=("${DEPLOY_SSH_OPTS[@]}")
    if [ -n "$PASS" ]; then
        SSHPASS="$PASS" sshpass -e scp "${scp_args[@]}" "$@"
    else
        scp "${scp_args[@]}" "$@"
    fi
}

if $DRY_RUN; then
    deploy_ui_banner "${DEPLOY_UI_ICON_MAGIC} Dry run" "no changes will be made"
    deploy_ui_kv "🎯" "Target" "${USER}@${HOST}"
    deploy_ui_kv "📦" "Binary" "$REMOTE_BIN"
    deploy_ui_kv "🌐" "Port" "$NODRA_PORT"
    echo ""
    deploy_ui_note "Would: detect arch → cross-compile → install → enable/start → smoke"
    echo ""
    exit 0
fi

deploy_ui_banner "Remote Deploy" "${NODRA_GIT_VERSION} (${NODRA_GIT_COMMIT}) → ${USER}@${HOST}"
deploy_ui_kv "🎯" "Target" "${USER}@${HOST}"
deploy_ui_kv "🌐" "Port" "$NODRA_PORT"
echo ""

if $UNINSTALL_MODE; then
    deploy_ui_uninstall_banner
    step "Uninstalling nodra-server from ${HOST}"
    _ssh "
        $SUDO systemctl disable --now nodra-server.service 2>/dev/null || true
        $SUDO rm -f $REMOTE_BIN $REMOTE_CTL $REMOTE_UNIT
        $SUDO rm -rf $REMOTE_DIR
        $SUDO systemctl daemon-reload 2>/dev/null || true
    "
    info "nodra-server removed from ${HOST} (data at ${REMOTE_DATA} left in place)"
    exit 0
fi

step "Detecting remote architecture"
REMOTE_ARCH_RAW=$(_ssh_batch "uname -m" | tr -d '\r')
case "$REMOTE_ARCH_RAW" in
    x86_64)         GOARCH=amd64 ;;
    aarch64|arm64)  GOARCH=arm64 ;;
    *) error "Unsupported remote architecture: $REMOTE_ARCH_RAW" ;;
esac
info "Remote: linux/${GOARCH}"

step "Cross-compiling nodra-server for linux/${GOARCH}"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
(
    cd "$REPO_DIR"
    CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
        go build -trimpath -ldflags="-s -w" -o "$BUILD_DIR/nodra-server" ./cmd/nodra-server
    CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
        go build -trimpath -ldflags="-s -w" -o "$BUILD_DIR/nodractl" ./cmd/nodractl
)
info "Built nodra-server + nodractl"

step "Installing nodra-server on ${HOST}"
# Reuse existing tokens from remote env when present
EXISTING_ADMIN="$(_ssh_batch "grep -E '^NODRA_ADMIN_TOKEN=' $REMOTE_ENV 2>/dev/null | cut -d= -f2- || true" | tr -d '\r' || true)"
EXISTING_ENROLL="$(_ssh_batch "grep -E '^NODRA_ENROLLMENT_TOKEN=' $REMOTE_ENV 2>/dev/null | cut -d= -f2- || true" | tr -d '\r' || true)"
ADMIN_TOKEN="${NODRA_ADMIN_TOKEN:-${EXISTING_ADMIN:-nodra-lab-admin}}"
ENROLL_TOKEN="${NODRA_ENROLLMENT_TOKEN:-${EXISTING_ENROLL:-nodra-lab-enroll}}"

_scp "$BUILD_DIR/nodra-server" "${USER}@${HOST}:/tmp/nodra-server.new"
_scp "$BUILD_DIR/nodractl" "${USER}@${HOST}:/tmp/nodractl.new"
_scp "$REPO_DIR/deployments/systemd/nodra-server.service" "${USER}@${HOST}:/tmp/nodra-server.service.new"

cat > "$BUILD_DIR/nodra.env" <<ENVEOF
NODRA_LISTEN=0.0.0.0:${NODRA_PORT}
NODRA_DATA_DIR=${REMOTE_DATA}
NODRA_ADMIN_TOKEN=${ADMIN_TOKEN}
NODRA_ENROLLMENT_TOKEN=${ENROLL_TOKEN}
ENVEOF
_scp "$BUILD_DIR/nodra.env" "${USER}@${HOST}:/tmp/nodra.env.new"

_ssh "
    set -euo pipefail
    $SUDO mkdir -p $REMOTE_DIR $REMOTE_DATA
    $SUDO install -m 755 /tmp/nodra-server.new $REMOTE_BIN
    $SUDO install -m 755 /tmp/nodractl.new $REMOTE_CTL
    $SUDO install -m 640 /tmp/nodra.env.new $REMOTE_ENV
    $SUDO install -m 644 /tmp/nodra-server.service.new $REMOTE_UNIT
    rm -f /tmp/nodra-server.new /tmp/nodractl.new /tmp/nodra.env.new /tmp/nodra-server.service.new
    # Binary reads NODRA_LISTEN / NODRA_DATA_DIR from env; pass via EnvironmentFile.
"
info "Binary + config + systemd unit installed"

step "Starting nodra-server.service"
_ssh "
    set -euo pipefail
    $SUDO systemctl daemon-reload
    $SUDO systemctl enable --now nodra-server.service
    $SUDO systemctl restart nodra-server.service
    if command -v firewall-cmd &>/dev/null; then
        $SUDO firewall-cmd --permanent --add-port=${NODRA_PORT}/tcp 2>/dev/null || true
        $SUDO firewall-cmd --reload 2>/dev/null || true
    elif command -v ufw &>/dev/null; then
        $SUDO ufw allow ${NODRA_PORT}/tcp 2>/dev/null || true
    fi
    sleep 1
    if $SUDO systemctl is-active nodra-server.service &>/dev/null; then
        echo 'nodra-server.service: running'
    else
        echo 'nodra-server.service: FAILED TO START'
        $SUDO journalctl -u nodra-server.service --no-pager -n 30
        exit 1
    fi
"
info "nodra-server.service active"

step "Verifying deployment"
BASE_URL="http://${HOST}:${NODRA_PORT}"
DEPLOY_UI_SCHEME="http"
_ssh "curl -fsS http://127.0.0.1:${NODRA_PORT}/healthz >/dev/null" \
    && info "Health check OK (http://127.0.0.1:${NODRA_PORT}/healthz, on-host)"

nodra_save_deploy_last "$REPO_DIR" "$HOST" "$USER" "full"

deploy_ui_highlight "📋 Final checklist"
deploy_ui_checklist "service" "$(_ssh_batch "$SUDO systemctl is-active nodra-server.service" | tr -d '\r')"
deploy_ui_checklist "health"  "$(_ssh_batch "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:${NODRA_PORT}/healthz" | tr -d '\r')"

nodra_print_success "$HOST" 0
info "Admin token (Connect): ${ADMIN_TOKEN}"

if $SKIP_SMOKE; then
    info "Skipped smoke-remote.sh (--skip-smoke)"
else
    step "Running scripts/smoke-remote.sh against ${BASE_URL}"
    ( cd "$REPO_DIR" && NODRA_URL="$BASE_URL" NODRA_ADMIN_TOKEN="$ADMIN_TOKEN" ./scripts/smoke-remote.sh )
fi
