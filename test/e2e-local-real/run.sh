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

CONTAINER="singleserver-e2e-$RUN_ID"
IMAGE="${SINGLESERVER_E2E_IMAGE:-singleserver-e2e-server:local}"
APP_NAME="e2e-$RUN_ID"
DOMAIN="run-$RUN_ID.$TEST_ZONE"
WORK_DIR="$E2E_DIR/work/$RUN_ID"
WWW_DIR="$WORK_DIR/www"
REPO_DIR="$WORK_DIR/repo"
PORT_FILE="$WORK_DIR/http-port"
SERVER_LOG="$WORK_DIR/http.log"

mkdir -p "$WWW_DIR/bin" "$REPO_DIR"

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
                "ephemeral": True,
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

cleanup() {
  local status=$?
  set +e
  if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER"; then
    log "Collecting container diagnostics"
    docker exec "$CONTAINER" bash -lc 'systemctl --no-pager --failed || true; journalctl -u singleserver.service -n 200 --no-pager || true' >"$WORK_DIR/container-diagnostics.log" 2>&1

    log "Best-effort cleanup"
    docker exec "$CONTAINER" singleserver remove "$APP_NAME" --yes >/dev/null 2>&1 || true

    local state tunnel_id account_id
    state="$(docker exec "$CONTAINER" cat /etc/singleserver/cloudflare.json 2>/dev/null || true)"
    tunnel_id="$(printf "%s" "$state" | json_field tunnel_id 2>/dev/null || true)"
    account_id="$(printf "%s" "$state" | json_field account_id 2>/dev/null || true)"
    if [ -n "$tunnel_id" ] && [ -n "$account_id" ]; then
      cf_api DELETE "/accounts/$account_id/cfd_tunnel/$tunnel_id" >/dev/null 2>&1 || true
    fi

    docker exec "$CONTAINER" tailscale logout >/dev/null 2>&1 || true

    if [ "${SINGLESERVER_E2E_KEEP_CONTAINER:-0}" != "1" ]; then
      docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    fi
  fi
  if [ -n "${HTTP_SERVER_PID:-}" ]; then
    kill "$HTTP_SERVER_PID" >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT

log "Building local Linux binaries"
commit="$(git -C "$ROOT_DIR" rev-parse HEAD)"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags="-X github.com/dvassallo/singleserver/internal/singleserver.Commit=$commit -X github.com/dvassallo/singleserver/internal/singleserver.BuildDate=$build_date"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$ldflags" -o "$WWW_DIR/bin/singleserver-linux-amd64" "$ROOT_DIR/cmd/singleserverd"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$ldflags" -o "$WWW_DIR/bin/singleserver-linux-arm64" "$ROOT_DIR/cmd/singleserverd"

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

log "Building E2E server image"
docker build -t "$IMAGE" -f "$E2E_DIR/Dockerfile.server" "$ROOT_DIR"

log "Starting $CONTAINER"
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d \
  --name "$CONTAINER" \
  --hostname "$CONTAINER" \
  --privileged \
  --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  -v "$ROOT_DIR:/workspace:ro" \
  "$IMAGE" >/dev/null

log "Waiting for systemd"
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

log "Installing Single Server in container"
docker exec \
  -e SINGLESERVER_DOWNLOAD_BASE_URL="$ARTIFACT_BASE_URL" \
  -e SINGLESERVER_INSTALL_SKIP_FIRST_RUN=1 \
  -e SINGLESERVER_DOCKER_STORAGE_DRIVER="${SINGLESERVER_E2E_DOCKER_STORAGE_DRIVER:-vfs}" \
  "$CONTAINER" bash /workspace/www/install.sh

container_exec singleserver version

log "Connecting Tailscale"
if [ -z "${TAILSCALE_AUTHKEY:-}" ]; then
  log "Generating ephemeral Tailscale auth key"
fi
TAILSCALE_E2E_AUTHKEY="$(tailscale_e2e_authkey)"
docker exec \
  -e TAILSCALE_AUTHKEY="$TAILSCALE_E2E_AUTHKEY" \
  "$CONTAINER" singleserver tailscale connect --hostname "$CONTAINER"
TAILSCALE_E2E_AUTHKEY=""

FUNNEL_URL="$(container_bash ". /etc/singleserver/singleserver.env; printf '%s' \"\$SINGLESERVER_PUBLIC_URL\"")"
if [ -z "$FUNNEL_URL" ]; then
  fail "Tailscale did not produce SINGLESERVER_PUBLIC_URL"
fi
WEBHOOK_URL="${FUNNEL_URL%/}/github/webhook"
log "Funnel URL: $FUNNEL_URL"

log "Connecting Cloudflare"
cloudflare_args=(singleserver cloudflare connect)
if [ -n "${CLOUDFLARE_ACCOUNT_ID:-}" ]; then
  cloudflare_args+=(--account "$CLOUDFLARE_ACCOUNT_ID")
fi
docker exec \
  -e CLOUDFLARE_API_TOKEN="$CLOUDFLARE_API_TOKEN" \
  "$CONTAINER" "${cloudflare_args[@]}"

log "Writing GitHub App credentials"
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

log "Preparing test app repository"
rm -rf "$REPO_DIR"
gh repo clone "$GITHUB_TEST_REPO" "$REPO_DIR" >/dev/null
(
  cd "$REPO_DIR"
  git config user.name "Single Server E2E"
  git config user.email "singleserver-e2e@example.com"
  if git rev-parse --verify main >/dev/null 2>&1; then
    git switch main >/dev/null
  else
    git switch -c main >/dev/null
  fi
  marker="initial-$RUN_ID"
  cat > Dockerfile <<EOF
FROM nginx:alpine
COPY index.html /usr/share/nginx/html/index.html
COPY up /usr/share/nginx/html/up
EOF
  printf '<!doctype html><title>Single Server E2E</title><h1>%s</h1>\n' "$marker" > index.html
  printf '%s\n' "$marker" > up
  git add Dockerfile index.html up
  git commit -m "E2E initial $RUN_ID" >/dev/null || true
  if [ -n "${GITHUB_PUSH_TOKEN:-}" ]; then
    git push "https://x-access-token:${GITHUB_PUSH_TOKEN}@github.com/${GITHUB_TEST_REPO}.git" HEAD:main >/dev/null
  else
    git push origin HEAD:main >/dev/null
  fi
)

log "Adding and deploying app"
container_exec singleserver add "https://github.com/$GITHUB_TEST_REPO" \
  --name "$APP_NAME" \
  --branch main \
  --domain "$DOMAIN" \
  --healthcheck-path /up \
  --healthcheck "https://$DOMAIN/up" \
  --yes

log "Waiting for initial public app"
initial_marker="initial-$RUN_ID"
for _ in $(seq 1 90); do
  body="$(curl -fsS "https://$DOMAIN/up" 2>/dev/null || true)"
  if [ "$body" = "$initial_marker" ]; then
    break
  fi
  sleep 2
done
body="$(curl -fsS "https://$DOMAIN/up" 2>/dev/null || true)"
if [ "$body" != "$initial_marker" ]; then
  fail "Initial app did not become live at https://$DOMAIN/up; got '$body'"
fi

log "Pushing a change to trigger real GitHub webhook"
changed_marker="changed-$RUN_ID"
(
  cd "$REPO_DIR"
  printf '<!doctype html><title>Single Server E2E</title><h1>%s</h1>\n' "$changed_marker" > index.html
  printf '%s\n' "$changed_marker" > up
  git add index.html up
  git commit -m "E2E change $RUN_ID" >/dev/null
  if [ -n "${GITHUB_PUSH_TOKEN:-}" ]; then
    git push "https://x-access-token:${GITHUB_PUSH_TOKEN}@github.com/${GITHUB_TEST_REPO}.git" HEAD:main >/dev/null
  else
    git push origin HEAD:main >/dev/null
  fi
)

for _ in $(seq 1 120); do
  body="$(curl -fsS "https://$DOMAIN/up" 2>/dev/null || true)"
  if [ "$body" = "$changed_marker" ]; then
    break
  fi
  sleep 2
done
body="$(curl -fsS "https://$DOMAIN/up" 2>/dev/null || true)"
if [ "$body" != "$changed_marker" ]; then
  container_bash "journalctl -u singleserver.service -n 300 --no-pager" || true
  fail "Webhook deploy did not publish changed marker; got '$body'"
fi

log "Running doctor"
container_exec singleserver doctor "$APP_NAME"

log "Removing app"
container_exec singleserver remove "$APP_NAME" --yes

log "Verifying Cloudflare DNS cleanup"
zone_id="$(cf_api GET "/zones?name=$TEST_ZONE" | json_field result.0.id)"
records="$(cf_api GET "/zones/$zone_id/dns_records?type=CNAME&name=$DOMAIN" | json_field result.0.id || true)"
if [ -n "$records" ]; then
  fail "Cloudflare DNS record still exists for $DOMAIN"
fi

log "E2E passed for $DOMAIN"
