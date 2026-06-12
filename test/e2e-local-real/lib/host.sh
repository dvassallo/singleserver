container_exec() {
  docker exec "$CONTAINER" "$@"
}

container_bash() {
  docker exec "$CONTAINER" bash -lc "$*"
}

container_file_sha() {
  local path="$1"
  docker exec "$CONTAINER" sh -c 'if [ -f "$1" ]; then sha256sum "$1" | awk "{print \$1}"; else printf missing; fi' sh "$path"
}

container_json_field() {
  local path="$1"
  local field="$2"
  local body
  body="$(docker exec "$CONTAINER" cat "$path" 2>/dev/null || true)"
  if [ -z "$body" ]; then
    return 0
  fi
  printf "%s" "$body" | json_field "$field" 2>/dev/null || true
}

teardown_host() {
  local old_opts="$-"
  set +e
  if [ -n "$CONTAINER" ] && docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER"; then
    log "Collecting $CONTAINER diagnostics"
    mkdir -p "$WORK_DIR"
    docker exec "$CONTAINER" bash -lc '
      systemctl --no-pager --failed || true
      journalctl -u singleserver.service -n 200 --no-pager || true
      journalctl -u tailscaled.service -n 200 --no-pager || true
      journalctl -u cloudflared-singleserver.service -n 200 --no-pager || true
    ' >"$WORK_DIR/container-diagnostics.log" 2>&1

    log "Best-effort $CONTAINER cleanup"
    if [ -n "$APP_NAME" ]; then
      docker exec "$CONTAINER" singleserver remove "$APP_NAME" --delete-storage --non-interactive >/dev/null 2>&1 || true
      APP_NAME=""
    fi

    local state tunnel_id account_id
    state="$(docker exec "$CONTAINER" cat /etc/singleserver/cloudflare.json 2>/dev/null || true)"
    tunnel_id="$(printf "%s" "$state" | json_field tunnel_id 2>/dev/null || true)"
    account_id="$(printf "%s" "$state" | json_field account_id 2>/dev/null || true)"
    if [ -n "$tunnel_id" ] && [ -n "$account_id" ]; then
      cf_api DELETE "/accounts/$account_id/cfd_tunnel/$tunnel_id" >/dev/null 2>&1 || true
    fi

    docker exec "$CONTAINER" tailscale logout >/dev/null 2>&1 || true
    if [ -n "$TAILSCALE_STATE_DIR" ]; then
      rm -rf "$TAILSCALE_STATE_DIR"
    fi

    if [ "${SINGLESERVER_E2E_KEEP_CONTAINER:-0}" != "1" ]; then
      docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    fi
  fi
  CONTAINER=""
  TAILSCALE_HOSTNAME=""
  TAILSCALE_STATE_DIR=""
  case "$old_opts" in
    *e*) set -e ;;
  esac
}

cleanup() {
  local status=$?
  set +e
  teardown_host
  if [ -n "${HTTP_SERVER_PID:-}" ]; then
    kill "$HTTP_SERVER_PID" >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT

build_local_binaries() {
  log "Building local Linux binaries"
  local commit build_date ldflags
  commit="$(git -C "$ROOT_DIR" rev-parse HEAD)"
  build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  ldflags="-X github.com/dvassallo/singleserver/internal/singleserver.Commit=$commit -X github.com/dvassallo/singleserver/internal/singleserver.BuildDate=$build_date"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$ldflags" -o "$WWW_DIR/bin/singleserver-linux-amd64" "$ROOT_DIR/cmd/singleserverd"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$ldflags" -o "$WWW_DIR/bin/singleserver-linux-arm64" "$ROOT_DIR/cmd/singleserverd"
}

start_artifact_server() {
  log "Starting local artifact server"
  python3 - "$WWW_DIR" "$PORT_FILE" >"$SERVER_LOG" 2>&1 <<'PY' &
import functools
import http.server
import pathlib
import socketserver
import sys

root = pathlib.Path(sys.argv[1])
port_file = pathlib.Path(sys.argv[2])
handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=str(root))
with socketserver.TCPServer(("", 0), handler) as httpd:
    port_file.write_text(str(httpd.server_address[1]))
    httpd.serve_forever()
PY
  HTTP_SERVER_PID=$!
  for _ in $(seq 1 50); do
    if [ -f "$PORT_FILE" ]; then
      break
    fi
    sleep 0.1
  done
  ARTIFACT_PORT="$(cat "$PORT_FILE")"
  ARTIFACT_BASE_URL="http://host.docker.internal:$ARTIFACT_PORT"
}

distro_dockerfile() {
  local distro="$1"
  local dockerfile="$E2E_DIR/images/$distro.Dockerfile"
  if [ ! -f "$dockerfile" ]; then
    fail "No E2E Dockerfile for distro '$distro' at $dockerfile"
  fi
  printf "%s" "$dockerfile"
}

build_distro_image() {
  local distro="$1"
  local dockerfile
  dockerfile="$(distro_dockerfile "$distro")"
  DISTRO_IMAGE="${SINGLESERVER_E2E_IMAGE_PREFIX:-singleserver-e2e-server}:$distro-local"
  log "Building $distro E2E server image"
  docker build -t "$DISTRO_IMAGE" -f "$dockerfile" "$ROOT_DIR"
}

start_distro_host() {
  local distro="$1"
  local image="$2"
  CONTAINER="singleserver-e2e-$RUN_ID-$distro"
  WORK_DIR="$WORK_ROOT/$distro"
  REPO_DIR="$WORK_DIR/repo"
  local hostname_run
  hostname_run="${RUN_ID%%-*}-${RUN_ID##*-}"
  TAILSCALE_HOSTNAME="${SINGLESERVER_E2E_TAILSCALE_HOSTNAME_PREFIX:-singleserver-e2e}-$hostname_run-$distro"
  TAILSCALE_STATE_DIR="$TAILSCALE_STATE_ROOT/$distro"
  mkdir -p "$REPO_DIR" "$TAILSCALE_STATE_DIR"

  log "Starting $CONTAINER"
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  docker run -d \
    --name "$CONTAINER" \
    --hostname "$CONTAINER" \
    --privileged \
    --cgroupns=host \
    -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
    -v "$ROOT_DIR:/workspace:ro" \
    -v "$TAILSCALE_STATE_DIR:/var/lib/tailscale" \
    "$image" >/dev/null

  log "Waiting for $CONTAINER systemd"
  for _ in $(seq 1 60); do
    if docker exec "$CONTAINER" systemctl is-system-running >/dev/null 2>&1; then
      break
    fi
    state="$(docker exec "$CONTAINER" systemctl is-system-running 2>/dev/null || true)"
    if [ "$state" = "degraded" ]; then
      break
    fi
    sleep 1
  done
}

install_singleserver() {
  log "Installing Single Server in $CONTAINER"
  run_install_script
  container_exec singleserver version
}

run_install_script() {
  docker exec \
    -e SINGLESERVER_DOWNLOAD_BASE_URL="$ARTIFACT_BASE_URL" \
    -e SINGLESERVER_INSTALL_SKIP_FIRST_RUN=1 \
    -e SINGLESERVER_DOCKER_STORAGE_DRIVER="${SINGLESERVER_E2E_DOCKER_STORAGE_DRIVER:-vfs}" \
    "$CONTAINER" bash /workspace/www/install.sh
}

verify_initial_installer_idempotency() {
  local distro="$1"
  local apps_sha env_sha service_sha root_key_sha deploy_auth_sha

  log "Checking installer idempotency before provider setup on $distro"
  apps_sha="$(container_file_sha /etc/singleserver/apps.yml)"
  env_sha="$(container_file_sha /etc/singleserver/singleserver.env)"
  service_sha="$(container_file_sha /etc/systemd/system/singleserver.service)"
  root_key_sha="$(container_file_sha /root/.ssh/id_ed25519.pub)"
  deploy_auth_sha="$(container_file_sha /home/deploy/.ssh/authorized_keys)"

  run_install_script

  assert_equal "$(container_file_sha /etc/singleserver/apps.yml)" "$apps_sha" "$distro installer apps.yml before provider setup"
  assert_equal "$(container_file_sha /etc/singleserver/singleserver.env)" "$env_sha" "$distro installer env before provider setup"
  assert_equal "$(container_file_sha /etc/systemd/system/singleserver.service)" "$service_sha" "$distro installer systemd service before provider setup"
  assert_equal "$(container_file_sha /root/.ssh/id_ed25519.pub)" "$root_key_sha" "$distro installer root SSH key before provider setup"
  assert_equal "$(container_file_sha /home/deploy/.ssh/authorized_keys)" "$deploy_auth_sha" "$distro installer deploy authorized_keys before provider setup"
  container_exec singleserver doctor
}

verify_live_app_installer_idempotency() {
  local distro="$1"
  local case_name="$2"
  local app_name="$3"
  local public_url="$4"
  local marker="$5"
  local apps_sha env_sha cloudflare_sha cloudflared_config_sha cloudflared_credentials_sha github_sha github_key_sha
  local tunnel_id public_funnel_url
  local current_public_funnel_url

  if [ "$INSTALLER_POST_APP_IDEMPOTENCY_CHECKED" = "1" ]; then
    return 0
  fi

  log "Checking installer idempotency after live app deploy on $distro"
  apps_sha="$(container_file_sha /etc/singleserver/apps.yml)"
  env_sha="$(container_file_sha /etc/singleserver/singleserver.env)"
  cloudflare_sha="$(container_file_sha /etc/singleserver/cloudflare.json)"
  cloudflared_config_sha="$(container_file_sha /etc/cloudflared/singleserver.yml)"
  cloudflared_credentials_sha="$(container_file_sha /etc/cloudflared/singleserver.json)"
  github_sha="$(container_file_sha /etc/singleserver/github.json)"
  github_key_sha="$(container_file_sha /etc/singleserver/github.private-key.pem)"
  tunnel_id="$(container_json_field /etc/singleserver/cloudflare.json tunnel_id)"
  public_funnel_url="$(container_bash ". /etc/singleserver/singleserver.env; printf '%s' \"\${SINGLESERVER_PUBLIC_URL:-}\"")"

  if [ "$cloudflare_sha" = "missing" ] || [ "$github_sha" = "missing" ] || [ "$tunnel_id" = "" ] || [ "$public_funnel_url" = "" ]; then
    fail "$distro installer idempotency needs connected Cloudflare, GitHub, and Tailscale state"
  fi

  run_install_script

  assert_equal "$(container_file_sha /etc/singleserver/apps.yml)" "$apps_sha" "$distro installer apps.yml after live app"
  assert_equal "$(container_file_sha /etc/singleserver/singleserver.env)" "$env_sha" "$distro installer env after live app"
  assert_equal "$(container_file_sha /etc/singleserver/cloudflare.json)" "$cloudflare_sha" "$distro installer Cloudflare state after live app"
  assert_equal "$(container_file_sha /etc/cloudflared/singleserver.yml)" "$cloudflared_config_sha" "$distro installer cloudflared config after live app"
  assert_equal "$(container_file_sha /etc/cloudflared/singleserver.json)" "$cloudflared_credentials_sha" "$distro installer cloudflared credentials after live app"
  assert_equal "$(container_file_sha /etc/singleserver/github.json)" "$github_sha" "$distro installer GitHub state after live app"
  assert_equal "$(container_file_sha /etc/singleserver/github.private-key.pem)" "$github_key_sha" "$distro installer GitHub key after live app"
  assert_equal "$(container_json_field /etc/singleserver/cloudflare.json tunnel_id)" "$tunnel_id" "$distro installer Cloudflare tunnel ID after live app"
  current_public_funnel_url="$(container_bash ". /etc/singleserver/singleserver.env; printf '%s' \"\${SINGLESERVER_PUBLIC_URL:-}\"")"
  assert_equal "$current_public_funnel_url" "$public_funnel_url" "$distro installer Tailscale Funnel URL after live app"

  container_exec singleserver doctor "$app_name"
  wait_for_app_marker "$public_url" "$marker" "$distro/$case_name installer idempotency live app"
  INSTALLER_POST_APP_IDEMPOTENCY_CHECKED=1
}

connect_tailscale() {
  log "Connecting Tailscale for $CONTAINER"
  if [ -z "${TAILSCALE_AUTHKEY:-}" ]; then
    log "Generating Tailscale auth key"
  fi
  TAILSCALE_E2E_AUTHKEY="$(tailscale_e2e_authkey)"
  docker exec \
    -e TAILSCALE_AUTHKEY="$TAILSCALE_E2E_AUTHKEY" \
    "$CONTAINER" singleserver connect tailscale --hostname "$TAILSCALE_HOSTNAME"
  TAILSCALE_E2E_AUTHKEY=""

  FUNNEL_URL="$(container_bash ". /etc/singleserver/singleserver.env; printf '%s' \"\$SINGLESERVER_PUBLIC_URL\"")"
  if [ -z "$FUNNEL_URL" ]; then
    fail "Tailscale did not produce SINGLESERVER_PUBLIC_URL"
  fi
  WEBHOOK_URL="${FUNNEL_URL%/}/github/webhook"
  log "Funnel URL: $FUNNEL_URL"
}

wait_for_funnel_health() {
  local url="${FUNNEL_URL%/}/health"
  local host ip last attempts i
  host="${FUNNEL_URL#https://}"
  host="${host%%/*}"
  attempts="${SINGLESERVER_E2E_FUNNEL_HEALTH_ATTEMPTS:-300}"
  for i in $(seq 1 "$attempts"); do
    ip="$(public_dns_ip_once "$host")"
    if [ -z "$ip" ]; then
      last="no public A/AAAA record for $host"
      if [ "$i" = 1 ] || [ $((i % 15)) = 0 ]; then
        log "Waiting for public Funnel DNS ($i/$attempts): $last"
      fi
      sleep 2
      continue
    fi
    if curl -fsS --max-time 5 --resolve "$host:443:$ip" "$url" >/dev/null 2>&1; then
      return 0
    fi
    last="GET $url via $ip failed"
    if [ "$i" = 1 ] || [ $((i % 15)) = 0 ]; then
      log "Waiting for public Funnel health ($i/$attempts): $last"
    fi
    sleep 2
  done
  if curl -fsS --max-time 5 "$url" >/dev/null 2>&1; then
    last="$last; local resolver can reach the Funnel, but public DNS is not ready"
  fi
  fail "Funnel health endpoint did not become ready at $url: $last"
}

connect_cloudflare() {
  local cloudflare_args
  log "Connecting Cloudflare for $CONTAINER"
  cloudflare_args=(singleserver connect cloudflare)
  if [ -n "${CLOUDFLARE_ACCOUNT_ID:-}" ]; then
    cloudflare_args+=(--account "$CLOUDFLARE_ACCOUNT_ID")
  fi
  docker exec \
    -e CLOUDFLARE_API_TOKEN="$CLOUDFLARE_API_TOKEN" \
    "$CONTAINER" "${cloudflare_args[@]}"
}

connect_github_app() {
  log "Writing GitHub App credentials for $CONTAINER"
  container_exec mkdir -p /etc/singleserver
  docker cp "$GITHUB_APP_PRIVATE_KEY_PATH" "$CONTAINER:/etc/singleserver/github.private-key.pem"
  container_bash "chmod 600 /etc/singleserver/github.private-key.pem"
  python3 - "$GITHUB_APP_ID" "$GITHUB_APP_SLUG" "$GITHUB_WEBHOOK_SECRET" <<'PY' | docker exec -i "$CONTAINER" tee /etc/singleserver/github.json >/dev/null
import json
import sys

app_id, slug, secret = sys.argv[1:4]
print(json.dumps({"app_id": int(app_id), "slug": slug, "webhook_secret": secret}, indent=2))
PY
  container_bash "chmod 600 /etc/singleserver/github.json && systemctl restart singleserver.service"
  wait_for_funnel_health

  log "Updating GitHub App webhook URL"
  webhook_payload="$(python3 - "$WEBHOOK_URL" "$GITHUB_WEBHOOK_SECRET" <<'PY'
import json
import sys

url, secret = sys.argv[1:3]
print(json.dumps({
    "url": url,
    "content_type": "json",
    "insecure_ssl": "0",
    "secret": secret,
}))
PY
)"
  github_app_api PATCH /app/hook/config --data "$webhook_payload" >/dev/null
}

ensure_test_repo() {
  log "Ensuring test repo exists: $GITHUB_TEST_REPO"
  if ! gh repo view "$GITHUB_TEST_REPO" >/dev/null 2>&1; then
    gh repo create "$GITHUB_TEST_REPO" --private --confirm >/dev/null
  fi

  if ! github_app_api GET "/repos/$GITHUB_TEST_REPO/installation" >/dev/null 2>&1; then
    echo "GitHub App is not installed on $GITHUB_TEST_REPO." >&2
    if [ -n "${GITHUB_APP_SLUG:-}" ]; then
      echo "Install it here, then rerun:" >&2
      echo "https://github.com/apps/$GITHUB_APP_SLUG/installations/new" >&2
    fi
    exit 1
  fi
}

clone_test_repo() {
  local repo_url
  log "Cloning test app repository"
  rm -rf "$REPO_DIR"
  if [ -n "${GITHUB_PUSH_TOKEN:-}" ]; then
    repo_url="https://x-access-token:${GITHUB_PUSH_TOKEN}@github.com/${GITHUB_TEST_REPO}.git"
  else
    repo_url="https://github.com/${GITHUB_TEST_REPO}.git"
  fi
  git clone "$repo_url" "$REPO_DIR" >/dev/null
  (
    cd "$REPO_DIR"
    git config user.name "Single Server E2E"
    git config user.email "singleserver-e2e@example.com"
    if git rev-parse --verify main >/dev/null 2>&1; then
      git switch main >/dev/null
    else
      git switch -c main >/dev/null
    fi
  )
}
