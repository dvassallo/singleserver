#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
E2E_DIR="$ROOT_DIR/test/e2e"
ENV_FILE="${E2E_ENV_FILE:-$E2E_DIR/.env}"

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing $ENV_FILE. Copy .env.example to .env and fill in the real test credentials." >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

: "${CLOUDFLARE_API_TOKEN:?CLOUDFLARE_API_TOKEN is required}"
: "${TEST_ZONE:=singleserver.xyz}"
: "${GITHUB_APP_ID:?GITHUB_APP_ID is required}"
: "${GITHUB_WEBHOOK_SECRET:?GITHUB_WEBHOOK_SECRET is required}"
: "${GITHUB_APP_PRIVATE_KEY_PATH:?GITHUB_APP_PRIVATE_KEY_PATH is required}"
: "${GITHUB_TEST_REPO:?GITHUB_TEST_REPO is required}"

if [ -z "${TAILSCALE_AUTHKEY:-}" ]; then
  : "${TAILSCALE_OAUTH_CLIENT_ID:?TAILSCALE_OAUTH_CLIENT_ID or TAILSCALE_AUTHKEY is required}"
  : "${TAILSCALE_OAUTH_CLIENT_SECRET:?TAILSCALE_OAUTH_CLIENT_SECRET or TAILSCALE_AUTHKEY is required}"
  : "${TAILSCALE_TAG:?TAILSCALE_TAG is required when using Tailscale OAuth}"
fi

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

require_command curl
require_command docker
require_command gh
require_command git
require_command go
require_command dig
require_command openssl
require_command python3

docker info >/dev/null
gh auth status >/dev/null

RUN_ID="${RUN_ID:-$(date -u +%Y%m%d%H%M%S)-$RANDOM}"
RUN_ID="$(printf "%s" "$RUN_ID" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9-' '-')"
RUN_ID="${RUN_ID%-}"
RUN_ID="${RUN_ID#-}"
if [ -z "$RUN_ID" ]; then
  RUN_ID="$(date -u +%Y%m%d%H%M%S)"
fi

DISTROS="$(printf "%s" "${E2E_DISTROS:-ubuntu debian amazonlinux rocky}" | tr ',' ' ')"
CASES="$(printf "%s" "${E2E_CASES:-dockerfile static static-build node}" | tr ',' ' ')"
COMMAND_COVERAGE="${E2E_COMMAND_COVERAGE:-1}"
CLOUDFLARE_E2E_TUNNEL_PREFIX="${SINGLESERVER_E2E_CLOUDFLARE_TUNNEL_PREFIX:-singleserver-singleserver-e2e-}"
CLOUDFLARE_E2E_TUNNEL_CLEANUP_MIN_AGE_SECONDS="${SINGLESERVER_E2E_CLOUDFLARE_TUNNEL_CLEANUP_MIN_AGE_SECONDS:-21600}"
WORK_ROOT="$E2E_DIR/work/$RUN_ID"
ARTIFACT_DIR="$WORK_ROOT/artifacts"
WWW_DIR="$ARTIFACT_DIR/www"
PORT_FILE="$ARTIFACT_DIR/http-port"
SERVER_LOG="$ARTIFACT_DIR/http.log"
TAILSCALE_STATE_ROOT="${SINGLESERVER_E2E_TAILSCALE_STATE_ROOT:-$E2E_DIR/state/tailscale/$RUN_ID}"

CONTAINER=""
WORK_DIR=""
REPO_DIR=""
APP_NAME=""
DISTRO_IMAGE=""
TAILSCALE_HOSTNAME=""
TAILSCALE_STATE_DIR=""
INSTALLER_POST_APP_IDEMPOTENCY_CHECKED=""

mkdir -p "$WWW_DIR/bin"

source "$E2E_DIR/lib/providers.sh"
source "$E2E_DIR/lib/host.sh"
source "$E2E_DIR/lib/cases.sh"

run_distro() {
  local distro="$1"
  local case_name

  build_distro_image "$distro"
  start_distro_host "$distro" "$DISTRO_IMAGE"
  INSTALLER_POST_APP_IDEMPOTENCY_CHECKED=""
  install_singleserver
  verify_initial_installer_idempotency "$distro"
  connect_tailscale
  connect_cloudflare
  connect_github_app
  ensure_test_repo
  clone_test_repo

  for case_name in $CASES; do
    run_app_case "$distro" "$case_name"
  done

  if [ "$COMMAND_COVERAGE" != "0" ]; then
    run_ops_scenario "$distro"
  fi

  log "E2E passed for $distro cases: $CASES command_coverage=$COMMAND_COVERAGE"
  teardown_host
}

sweep_stale_cloudflare_e2e_tunnels
build_local_binaries
start_artifact_server

for distro in $DISTROS; do
  run_distro "$distro"
done

log "E2E passed for distros: $DISTROS"
