#!/usr/bin/env bash
# Install-only E2E: builds a distro image, runs install.sh against it, and
# asserts the install succeeds — without connecting Tailscale/Cloudflare/GitHub
# or deploying apps. It needs no credentials and no .env, so it is cheap to run.
#
# Default scenario is `docker-ce`: a host with Docker CE already installed, which
# is the case PR #1 fixed (the installer must skip the conflicting docker.io
# packages). Override the matrix with E2E_INSTALL_DISTROS=a,b.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
E2E_DIR="$ROOT_DIR/test/e2e"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

require_command docker
require_command go
require_command python3
require_command git

docker info >/dev/null

RUN_ID="${RUN_ID:-install-$(date -u +%Y%m%d%H%M%S)-$RANDOM}"
RUN_ID="$(printf "%s" "$RUN_ID" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9-' '-')"
RUN_ID="${RUN_ID%-}"
RUN_ID="${RUN_ID#-}"

DISTROS="$(printf "%s" "${E2E_INSTALL_DISTROS:-docker-ce}" | tr ',' ' ')"
WORK_ROOT="$E2E_DIR/work/$RUN_ID"
ARTIFACT_DIR="$WORK_ROOT/artifacts"
WWW_DIR="$ARTIFACT_DIR/www"
PORT_FILE="$ARTIFACT_DIR/http-port"
SERVER_LOG="$ARTIFACT_DIR/http.log"
TAILSCALE_STATE_ROOT="${SINGLESERVER_E2E_TAILSCALE_STATE_ROOT:-$E2E_DIR/state/tailscale/$RUN_ID}"

# Globals that lib/host.sh reads and its cleanup trap resets.
CONTAINER=""
WORK_DIR=""
DISTRO_IMAGE=""
TAILSCALE_HOSTNAME=""
TAILSCALE_STATE_DIR=""
APP_NAME=""

mkdir -p "$WWW_DIR/bin"

# providers.sh: log/fail/assert_* (function defs only, no credential checks).
# host.sh: image build, host start, install, container_exec, and the EXIT trap
# that tears the container down and stops the artifact server.
source "$E2E_DIR/lib/providers.sh"
source "$E2E_DIR/lib/host.sh"

run_install_only() {
  local distro="$1"

  build_distro_image "$distro"
  start_distro_host "$distro" "$DISTRO_IMAGE"

  # The scenario is only meaningful if Docker is already present before the
  # installer runs; otherwise the image is wrong and the test would pass
  # vacuously without exercising the skip-the-conflicting-packages path.
  if ! container_exec sh -c 'command -v docker >/dev/null 2>&1'; then
    fail "$distro image must have Docker preinstalled for this scenario"
  fi
  assert_contains "$(container_exec dpkg-query -W -f='${Status}' containerd.io 2>&1)" \
    "installed" "$distro must have containerd.io preinstalled (the conflict trigger)"

  # Runs www/install.sh with SKIP_FIRST_RUN; aborts here (via set -e + the trap's
  # diagnostics) if the installer exits non-zero — e.g. the apt conflict PR #1 fixes.
  install_singleserver

  assert_contains "$(container_exec docker buildx version 2>&1)" \
    "buildx" "$distro docker buildx available after install"
  assert_equal "$(container_exec systemctl is-active docker 2>/dev/null || true)" \
    "active" "$distro docker service active after install"

  log "Install-only E2E passed for $distro"
  teardown_host
}

build_local_binaries
start_artifact_server

for distro in $DISTROS; do
  run_install_only "$distro"
done

log "Install-only E2E passed for distros: $DISTROS"
