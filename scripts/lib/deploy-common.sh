# SPDX-License-Identifier: Apache-2.0
# shellcheck shell=bash
# Nodra deploy library (self-contained under scripts/lib/).

_DEPLOY_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

DEPLOY_UI_PROJECT="nodra"
DEPLOY_UI_ICON="📡"
DEPLOY_UI_ICON_UNINSTALL="🗑️"
DEPLOY_UI_ICON_MAGIC="✨"
DEPLOY_UI_PORT="${NODRA_PORT:-0}"
DEPLOY_UI_SCHEME="http"
DEPLOY_UI_DASH_PATH="/"
DEPLOY_UI_HEALTH_PATH="/healthz"

# shellcheck source=deploy-ui.sh
source "$_DEPLOY_LIB_DIR/deploy-ui.sh"

nodra_build_metadata() {
    local repo_dir="$1"
    NODRA_GIT_VERSION=$(git -C "$repo_dir" describe --tags --always --dirty 2>/dev/null || echo 'dev')
    NODRA_GIT_COMMIT=$(git -C "$repo_dir" rev-parse --short HEAD 2>/dev/null || echo 'unknown')
    export NODRA_GIT_VERSION NODRA_GIT_COMMIT
}

nodra_parse_target() { deploy_ui_parse_target "$@"; }
nodra_save_deploy_last() {
    deploy_ui_save_deploy_last "$1" "$2" "$3" "$4" "${NODRA_GIT_VERSION:-}" "${NODRA_GIT_COMMIT:-}"
}
nodra_load_deploy_last() { deploy_ui_load_deploy_last "$1"; }
nodra_print_success() {
    deploy_ui_success "$1" "$2" "./scripts/deploy-remote.sh $1 --uninstall"
}

nodra_info()  { deploy_ui_info "$@"; }
nodra_warn()  { deploy_ui_warn "$@"; }
nodra_error() { deploy_ui_error "$@"; }
