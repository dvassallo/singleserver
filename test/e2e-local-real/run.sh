#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
E2E_DIR="$ROOT_DIR/test/e2e-local-real"
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
: "${GITHUB_TEST_REPO:=dvassallo/singleserver-e2e-app}"

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
CASES="$(printf "%s" "${E2E_CASES:-dockerfile static node}" | tr ',' ' ')"
WORK_ROOT="$E2E_DIR/work/$RUN_ID"
ARTIFACT_DIR="$WORK_ROOT/artifacts"
WWW_DIR="$ARTIFACT_DIR/www"
PORT_FILE="$ARTIFACT_DIR/http-port"
SERVER_LOG="$ARTIFACT_DIR/http.log"
TAILSCALE_STATE_ROOT="${SINGLESERVER_E2E_TAILSCALE_STATE_ROOT:-$E2E_DIR/state/tailscale}"

CONTAINER=""
WORK_DIR=""
REPO_DIR=""
APP_NAME=""
DISTRO_IMAGE=""
TAILSCALE_HOSTNAME=""
TAILSCALE_STATE_DIR=""

mkdir -p "$WWW_DIR/bin"

log() {
  printf "\n==> %s\n" "$*"
}

fail() {
  echo "E2E failed: $*" >&2
  exit 1
}

b64url() {
  openssl base64 -A | tr '+/' '-_' | tr -d '='
}

github_app_jwt() {
  local now exp header payload unsigned signature
  now="$(date +%s)"
  exp="$((now + 540))"
  header="$(printf '{"alg":"RS256","typ":"JWT"}' | b64url)"
  payload="$(printf '{"iat":%s,"exp":%s,"iss":%s}' "$((now - 60))" "$exp" "$GITHUB_APP_ID" | b64url)"
  unsigned="$header.$payload"
  signature="$(printf "%s" "$unsigned" | openssl dgst -sha256 -sign "$GITHUB_APP_PRIVATE_KEY_PATH" -binary | b64url)"
  printf "%s.%s\n" "$unsigned" "$signature"
}

github_app_api() {
  local method="$1"
  local path="$2"
  local jwt
  shift 2
  jwt="$(github_app_jwt)"
  curl -fsS -X "$method" \
    -H "Authorization: Bearer $jwt" \
    -H "Accept: application/vnd.github+json" \
    -H "Content-Type: application/json" \
    "https://api.github.com$path" \
    "$@"
}

cf_api() {
  local method="$1"
  local path="$2"
  shift 2
  curl -fsS -X "$method" \
    -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
    -H "Content-Type: application/json" \
    "https://api.cloudflare.com/client/v4$path" \
    "$@"
}

cloudflare_zone_nameservers() {
  if [ -n "${CLOUDFLARE_ZONE_NAMESERVERS:-}" ]; then
    printf "%s\n" "$CLOUDFLARE_ZONE_NAMESERVERS"
    return 0
  fi
  CLOUDFLARE_ZONE_NAMESERVERS="$(cf_api GET "/zones?name=$TEST_ZONE" | python3 -c 'import json, sys
data = json.load(sys.stdin)
zones = data.get("result") or []
if not zones:
    raise SystemExit("Cloudflare zone not found")
print("\n".join(zones[0].get("name_servers") or []))')"
  if [ -z "$CLOUDFLARE_ZONE_NAMESERVERS" ]; then
    fail "Cloudflare zone $TEST_ZONE has no nameservers"
  fi
  printf "%s\n" "$CLOUDFLARE_ZONE_NAMESERVERS"
}

cloudflare_edge_ip_once() {
  local host="$1"
  local ns ip
  for ns in $(cloudflare_zone_nameservers); do
    ip="$(dig +short @"$ns" "$host" A | awk '/^[0-9.]+$/ {print; exit}')"
    if [ -z "$ip" ]; then
      ip="$(dig +short @"$ns" "$host" AAAA | awk '/:/ {print "[" $0 "]"; exit}')"
    fi
    if [ -n "$ip" ]; then
      printf "%s\n" "$ip"
      return 0
    fi
  done
  return 1
}

public_dns_a_once() {
  local host="$1"
  dig +short @1.1.1.1 "$host" A | awk '/^[0-9.]+$/ {print; exit}'
}

tailscale_oauth_token() {
  curl -fsS -X POST \
    -d "client_id=$TAILSCALE_OAUTH_CLIENT_ID" \
    -d "client_secret=$TAILSCALE_OAUTH_CLIENT_SECRET" \
    -d "scope=auth_keys" \
    -d "tags=$TAILSCALE_TAG" \
    "https://api.tailscale.com/api/v2/oauth/token" | json_field access_token
}

tailscale_e2e_authkey() {
  if [ -n "${TAILSCALE_AUTHKEY:-}" ]; then
    printf "%s" "$TAILSCALE_AUTHKEY"
    return
  fi

  local token payload key
  token="$(tailscale_oauth_token)"
  if [ -z "$token" ]; then
    fail "Tailscale OAuth did not return an access token"
  fi

  payload="$(python3 - "$TAILSCALE_TAG" "$RUN_ID" <<'PY'
import json
import sys

tag, run_id = sys.argv[1:3]
print(json.dumps({
    "capabilities": {
        "devices": {
            "create": {
                "reusable": False,
                "ephemeral": False,
                "preauthorized": True,
                "tags": [tag],
            },
        },
    },
    "expirySeconds": 3600,
    "description": f"Single Server E2E {run_id}",
}))
PY
)"

  key="$(curl -fsS -X POST \
    -H "Authorization: Bearer $token" \
    -H "Content-Type: application/json" \
    "https://api.tailscale.com/api/v2/tailnet/-/keys" \
    --data "$payload" | json_field key)"
  if [ -z "$key" ]; then
    fail "Tailscale API did not return an auth key"
  fi
  printf "%s" "$key"
}

json_field() {
  python3 -c 'import json,sys; data=json.load(sys.stdin); path=sys.argv[1].split("."); value=data
for key in path:
    if key.isdigit():
        if not isinstance(value, list) or int(key) >= len(value):
            value = ""
            break
        value=value[int(key)]
    else:
        if not isinstance(value, dict):
            value = ""
            break
        value=value.get(key, "")
print(value if value is not None else "")' "$1"
}

container_exec() {
  docker exec "$CONTAINER" "$@"
}

container_bash() {
  docker exec "$CONTAINER" bash -lc "$*"
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
      docker exec "$CONTAINER" singleserver remove "$APP_NAME" --yes >/dev/null 2>&1 || true
      APP_NAME=""
    fi

    local state tunnel_id account_id
    state="$(docker exec "$CONTAINER" cat /etc/singleserver/cloudflare.json 2>/dev/null || true)"
    tunnel_id="$(printf "%s" "$state" | json_field tunnel_id 2>/dev/null || true)"
    account_id="$(printf "%s" "$state" | json_field account_id 2>/dev/null || true)"
    if [ -n "$tunnel_id" ] && [ -n "$account_id" ]; then
      cf_api DELETE "/accounts/$account_id/cfd_tunnel/$tunnel_id" >/dev/null 2>&1 || true
    fi

    if [ "${SINGLESERVER_E2E_RESET_TAILSCALE_STATE:-0}" = "1" ]; then
      docker exec "$CONTAINER" tailscale logout >/dev/null 2>&1 || true
      if [ -n "$TAILSCALE_STATE_DIR" ]; then
        rm -rf "$TAILSCALE_STATE_DIR"
      fi
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
  TAILSCALE_HOSTNAME="${SINGLESERVER_E2E_TAILSCALE_HOSTNAME_PREFIX:-singleserver-e2e}-$distro"
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
  docker exec \
    -e SINGLESERVER_DOWNLOAD_BASE_URL="$ARTIFACT_BASE_URL" \
    -e SINGLESERVER_INSTALL_SKIP_FIRST_RUN=1 \
    -e SINGLESERVER_DOCKER_STORAGE_DRIVER="${SINGLESERVER_E2E_DOCKER_STORAGE_DRIVER:-vfs}" \
    "$CONTAINER" bash /workspace/www/install.sh

  container_exec singleserver version
}

connect_tailscale() {
  log "Connecting Tailscale for $CONTAINER"
  if [ -z "${TAILSCALE_AUTHKEY:-}" ]; then
    log "Generating Tailscale auth key"
  fi
  TAILSCALE_E2E_AUTHKEY="$(tailscale_e2e_authkey)"
  docker exec \
    -e TAILSCALE_AUTHKEY="$TAILSCALE_E2E_AUTHKEY" \
    "$CONTAINER" singleserver tailscale connect --hostname "$TAILSCALE_HOSTNAME"
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
  local host ip last
  host="${FUNNEL_URL#https://}"
  host="${host%%/*}"
  for _ in $(seq 1 120); do
    ip="$(public_dns_a_once "$host")"
    if [ -z "$ip" ]; then
      last="no public A record for $host"
      sleep 2
      continue
    fi
    if curl -fsS --max-time 5 --resolve "$host:443:$ip" "$url" >/dev/null 2>&1; then
      return 0
    fi
    last="GET $url via $ip failed"
    sleep 2
  done
  fail "Funnel health endpoint did not become ready at $url: $last"
}

connect_cloudflare() {
  local cloudflare_args
  log "Connecting Cloudflare for $CONTAINER"
  cloudflare_args=(singleserver cloudflare connect)
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
  docker cp "$GITHUB_APP_PRIVATE_KEY_PATH" "$CONTAINER:/etc/singleserver/github-app.private-key.pem"
  container_bash "chmod 600 /etc/singleserver/github-app.private-key.pem"
  python3 - "$GITHUB_APP_ID" "$GITHUB_APP_SLUG" "$GITHUB_WEBHOOK_SECRET" <<'PY' | docker exec -i "$CONTAINER" tee /etc/singleserver/github-app.json >/dev/null
import json
import sys

app_id, slug, secret = sys.argv[1:4]
print(json.dumps({"app_id": int(app_id), "slug": slug, "webhook_secret": secret}, indent=2))
PY
  container_bash "chmod 600 /etc/singleserver/github-app.json && systemctl restart singleserver.service"
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

reset_case_repo() {
  (
    cd "$REPO_DIR"
    git rm -r --ignore-unmatch . >/dev/null 2>&1 || true
    git clean -fdx >/dev/null
  )
}

prepare_dockerfile_case() {
  local marker="$1"
  reset_case_repo
  (
    cd "$REPO_DIR"
    cat > Dockerfile <<EOF
FROM nginx:alpine
COPY index.html /usr/share/nginx/html/index.html
COPY up /usr/share/nginx/html/up
EOF
    printf '<!doctype html><title>Single Server E2E</title><h1>%s</h1>\n' "$marker" > index.html
    printf '%s\n' "$marker" > up
  )
}

prepare_static_case() {
  local marker="$1"
  reset_case_repo
  (
    cd "$REPO_DIR"
    mkdir -p public
    printf '<!doctype html><title>Single Server E2E Static</title><h1>%s</h1>\n' "$marker" > public/index.html
    printf '%s\n' "$marker" > public/case.txt
  )
}

prepare_node_case() {
  local marker="$1"
  reset_case_repo
  (
    cd "$REPO_DIR"
    cat > server.mjs <<EOF
import http from "node:http";

const marker = "$marker";
const port = Number(process.env.PORT || 3000);

const server = http.createServer((req, res) => {
  if (req.url === "/readyz") {
    res.writeHead(200, { "content-type": "text/plain" });
    res.end("ready\\n");
    return;
  }
  res.writeHead(200, { "content-type": "text/plain" });
  res.end(marker + "\\n");
});

server.listen(port, "0.0.0.0");
EOF
    cat > package.json <<'EOF'
{"type":"module","scripts":{"start":"node server.mjs"}}
EOF
  )
}

prepare_case_repo() {
  local case_name="$1"
  local marker="$2"
  case "$case_name" in
    dockerfile) prepare_dockerfile_case "$marker" ;;
    static) prepare_static_case "$marker" ;;
    node) prepare_node_case "$marker" ;;
    *) fail "Unknown E2E app case '$case_name'" ;;
  esac
}

commit_and_push_case() {
  local message="$1"
  (
    cd "$REPO_DIR"
    git add .
    git commit -m "$message" >/dev/null
    if [ -n "${GITHUB_PUSH_TOKEN:-}" ]; then
      git push "https://x-access-token:${GITHUB_PUSH_TOKEN}@github.com/${GITHUB_TEST_REPO}.git" HEAD:main >/dev/null
    else
      git push origin HEAD:main >/dev/null
    fi
    git rev-parse HEAD
  )
}

wait_for_github_push_delivery() {
  local sha="$1"
  local label="$2"
  local deliveries ids id detail delivered_sha status redelivered_ids last_status
  redelivered_ids=""
  last_status=""

  log "Waiting for GitHub webhook delivery for $label"
  for _ in $(seq 1 60); do
    deliveries="$(github_app_api GET "/app/hook/deliveries?per_page=10")"
    ids="$(printf "%s" "$deliveries" | python3 -c 'import json,sys
for delivery in json.load(sys.stdin):
    if delivery.get("event") == "push":
        print(delivery.get("id", ""))
')"
    for id in $ids; do
      [ -n "$id" ] || continue
      detail="$(github_app_api GET "/app/hook/deliveries/$id" 2>/dev/null || true)"
      [ -n "$detail" ] || continue
      delivered_sha="$(printf "%s" "$detail" | json_field request.payload.after 2>/dev/null || true)"
      if [ "$delivered_sha" = "$sha" ]; then
        status="$(printf "%s" "$detail" | json_field status)"
        if [ "$status" = "OK" ]; then
          return 0
        fi
        last_status="$status"
        case " $redelivered_ids " in
          *" $id "*) ;;
          *)
            log "Redelivering GitHub webhook for $label after status '$status'"
            github_app_api POST "/app/hook/deliveries/$id/attempts" >/dev/null 2>&1 || true
            redelivered_ids="$redelivered_ids $id"
            ;;
        esac
      fi
    done
    sleep 2
  done
  if [ -n "${last_status:-}" ]; then
    fail "GitHub webhook delivery for $label did not become OK; last status '$last_status'"
  fi
  fail "GitHub webhook delivery for $label did not arrive for $sha"
}

case_public_path() {
  case "$1" in
    dockerfile) printf "/up" ;;
    static) printf "/case.txt" ;;
    node) printf "/" ;;
    *) fail "Unknown E2E app case '$1'" ;;
  esac
}

add_case_app() {
  local case_name="$1"
  local domain="$2"
  case "$case_name" in
    dockerfile)
      container_exec singleserver add "https://github.com/$GITHUB_TEST_REPO" \
        --name "$APP_NAME" \
        --branch main \
        --domain "$domain" \
        --healthcheck-path /up \
        --healthcheck "https://$domain/up" \
        --yes
      ;;
    static)
      container_exec singleserver add "https://github.com/$GITHUB_TEST_REPO" \
        --name "$APP_NAME" \
        --branch main \
        --domain "$domain" \
        --runtime static \
        --static-dir public \
        --healthcheck-path /ready \
        --yes
      ;;
    node)
      container_exec singleserver add "https://github.com/$GITHUB_TEST_REPO" \
        --name "$APP_NAME" \
        --branch main \
        --domain "$domain" \
        --runtime node \
        --start "node server.mjs" \
        --app-port 3000 \
        --healthcheck-path /readyz \
        --yes
      ;;
    *) fail "Unknown E2E app case '$case_name'" ;;
  esac
}

wait_for_app_marker() {
  local url="$1"
  local marker="$2"
  local label="$3"
  local body host path edge_ip last_public last_local
  host="${url#https://}"
  path="/${host#*/}"
  if [ "$path" = "/$host" ]; then
    path="/"
  fi
  host="${host%%/*}"
  for _ in $(seq 1 120); do
    edge_ip="$(cloudflare_edge_ip_once "$host" || true)"
    if [ -n "$edge_ip" ]; then
      body="$(curl -fsS --resolve "$host:443:$edge_ip" "$url" 2>/dev/null || true)"
      if [ "$body" = "$marker" ]; then
        return 0
      fi
      last_public="$body"
    else
      last_public="dns-unpublished"
    fi
    body="$(docker exec "$CONTAINER" curl -fsS --max-time 5 -H "Host: $host" "http://127.0.0.1$path" 2>/dev/null || true)"
    if [ "$body" = "$marker" ]; then
      return 0
    fi
    last_local="$body"
    sleep 2
  done
  edge_ip="$(cloudflare_edge_ip_once "$host" || true)"
  body=""
  if [ -n "$edge_ip" ]; then
    body="$(curl -fsS --resolve "$host:443:$edge_ip" "$url" 2>/dev/null || true)"
  fi
  fail "$label did not publish expected marker at $url; public='$body' last_public='${last_public:-}' last_local='${last_local:-}'"
}

verify_dns_removed() {
  local domain="$1"
  local zone_id records
  zone_id="$(cf_api GET "/zones?name=$TEST_ZONE" | json_field result.0.id)"
  records="$(cf_api GET "/zones/$zone_id/dns_records?type=CNAME&name=$domain" | json_field result.0.id || true)"
  if [ -n "$records" ]; then
    fail "Cloudflare DNS record still exists for $domain"
  fi
}

run_app_case() {
  local distro="$1"
  local case_name="$2"
  local domain public_path initial_marker changed_marker public_url initial_sha changed_sha

  APP_NAME="e2e-$RUN_ID-$distro-$case_name"
  domain="run-$RUN_ID-$distro-$case_name.$TEST_ZONE"
  public_path="$(case_public_path "$case_name")"
  public_url="https://$domain$public_path"

  log "Preparing $case_name app repository for $distro"
  initial_marker="initial-$RUN_ID-$distro-$case_name"
  prepare_case_repo "$case_name" "$initial_marker"
  initial_sha="$(commit_and_push_case "E2E $distro $case_name initial $RUN_ID")"
  wait_for_github_push_delivery "$initial_sha" "$distro/$case_name initial push"

  log "Adding and deploying $case_name app on $distro"
  add_case_app "$case_name" "$domain"

  log "Waiting for initial $case_name app on $distro"
  wait_for_app_marker "$public_url" "$initial_marker" "$distro/$case_name initial deploy"

  log "Pushing $case_name change to trigger real GitHub webhook"
  changed_marker="changed-$RUN_ID-$distro-$case_name"
  prepare_case_repo "$case_name" "$changed_marker"
  changed_sha="$(commit_and_push_case "E2E $distro $case_name change $RUN_ID")"
  wait_for_github_push_delivery "$changed_sha" "$distro/$case_name change push"
  wait_for_app_marker "$public_url" "$changed_marker" "$distro/$case_name webhook deploy"

  log "Running doctor for $case_name on $distro"
  container_exec singleserver doctor "$APP_NAME"

  log "Removing $case_name app from $distro"
  container_exec singleserver remove "$APP_NAME" --yes
  APP_NAME=""

  log "Verifying Cloudflare DNS cleanup for $domain"
  verify_dns_removed "$domain"
}

run_distro() {
  local distro="$1"
  local case_name

  build_distro_image "$distro"
  start_distro_host "$distro" "$DISTRO_IMAGE"
  install_singleserver
  connect_tailscale
  connect_cloudflare
  connect_github_app
  ensure_test_repo
  clone_test_repo

  for case_name in $CASES; do
    run_app_case "$distro" "$case_name"
  done

  log "E2E passed for $distro cases: $CASES"
  teardown_host
}

build_local_binaries
start_artifact_server

for distro in $DISTROS; do
  run_distro "$distro"
done

log "E2E passed for distros: $DISTROS"
