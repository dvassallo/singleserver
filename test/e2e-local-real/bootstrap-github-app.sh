#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
E2E_DIR="$ROOT_DIR/test/e2e-local-real"
ENV_FILE="${E2E_ENV_FILE:-$E2E_DIR/.env}"

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing $ENV_FILE. Copy .env.example to .env and fill in Tailscale credentials first." >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

if [ -z "${TAILSCALE_AUTHKEY:-}" ]; then
  : "${TAILSCALE_OAUTH_CLIENT_ID:?TAILSCALE_OAUTH_CLIENT_ID or TAILSCALE_AUTHKEY is required}"
  : "${TAILSCALE_OAUTH_CLIENT_SECRET:?TAILSCALE_OAUTH_CLIENT_SECRET or TAILSCALE_AUTHKEY is required}"
  : "${TAILSCALE_TAG:?TAILSCALE_TAG is required when using Tailscale OAuth}"
fi

RUN_ID="${RUN_ID:-bootstrap-$(date -u +%Y%m%d%H%M%S)}"
RUN_ID="$(printf "%s" "$RUN_ID" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9-' '-')"
short_id="$(printf "%s" "$RUN_ID" | sed 's/^bootstrap-//' | cut -c1-12)"
GITHUB_APP_NAME="${SINGLESERVER_E2E_GITHUB_APP_NAME:-Single Server E2E $short_id}"
CONTAINER="singleserver-e2e-$RUN_ID"
IMAGE="${SINGLESERVER_E2E_IMAGE:-singleserver-e2e-server:local}"
WORK_DIR="$E2E_DIR/work/$RUN_ID"
WWW_DIR="$WORK_DIR/www"
PORT_FILE="$WORK_DIR/http-port"
CREDS_DIR="$E2E_DIR/work/github-app"

mkdir -p "$WWW_DIR/bin" "$CREDS_DIR"

log() {
  printf "\n==> %s\n" "$*"
}

fail() {
  echo "GitHub App bootstrap failed: $*" >&2
  exit 1
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

cleanup() {
  local status=$?
  set +e
  if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER"; then
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
python3 - "$WWW_DIR" "$PORT_FILE" >/dev/null 2>&1 <<'PY' &
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
  [ -f "$PORT_FILE" ] && break
  sleep 0.1
done
ARTIFACT_BASE_URL="http://host.docker.internal:$(cat "$PORT_FILE")"

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

for _ in $(seq 1 60); do
  state="$(docker exec "$CONTAINER" systemctl is-system-running 2>/dev/null || true)"
  [ "$state" = "running" ] || [ "$state" = "degraded" ] && break
  sleep 1
done

log "Installing Single Server"
docker exec \
  -e SINGLESERVER_DOWNLOAD_BASE_URL="$ARTIFACT_BASE_URL" \
  -e SINGLESERVER_INSTALL_SKIP_FIRST_RUN=1 \
  -e SINGLESERVER_DOCKER_STORAGE_DRIVER="${SINGLESERVER_E2E_DOCKER_STORAGE_DRIVER:-vfs}" \
  "$CONTAINER" bash /workspace/www/install.sh

docker exec "$CONTAINER" bash -lc "cat >> /etc/singleserver/singleserver.env <<'EOF'
SINGLESERVER_GITHUB_APP_NAME='$GITHUB_APP_NAME'
SINGLESERVER_GITHUB_APP_PUBLIC='false'
EOF
systemctl restart singleserver.service"

log "Connecting Tailscale"
if [ -z "${TAILSCALE_AUTHKEY:-}" ]; then
  log "Generating ephemeral Tailscale auth key"
fi
TAILSCALE_E2E_AUTHKEY="$(tailscale_e2e_authkey)"
docker exec \
  -e TAILSCALE_AUTHKEY="$TAILSCALE_E2E_AUTHKEY" \
  "$CONTAINER" singleserver tailscale connect --hostname "$CONTAINER"
TAILSCALE_E2E_AUTHKEY=""

log "Creating private GitHub App through manifest flow"
setup_output="$(docker exec "$CONTAINER" singleserver github connect)"
printf "%s\n" "$setup_output"
setup_url="$(printf "%s\n" "$setup_output" | awk '/github[[:space:]]+connect/ { print $4; exit }')"
if [ -z "$setup_url" ]; then
  echo "Could not find setup URL in output." >&2
  exit 1
fi

echo
echo "Open this URL, click Create GitHub App, approve it, then install it on the test repo owner:"
echo "$setup_url"
if command -v open >/dev/null 2>&1; then
  open "$setup_url" || true
fi

echo
echo "Waiting for GitHub App credentials to be written inside the container..."
for _ in $(seq 1 600); do
  if docker exec "$CONTAINER" test -s /etc/singleserver/github-app.json \
    && docker exec "$CONTAINER" test -s /etc/singleserver/github-app.private-key.pem; then
    break
  fi
  sleep 2
done

docker exec "$CONTAINER" test -s /etc/singleserver/github-app.json
docker exec "$CONTAINER" test -s /etc/singleserver/github-app.private-key.pem

docker cp "$CONTAINER:/etc/singleserver/github-app.json" "$CREDS_DIR/github-app.json"
docker cp "$CONTAINER:/etc/singleserver/github-app.private-key.pem" "$CREDS_DIR/github-app.private-key.pem"
chmod 600 "$CREDS_DIR/github-app.private-key.pem"

app_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["app_id"])' "$CREDS_DIR/github-app.json")"
app_slug="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("slug",""))' "$CREDS_DIR/github-app.json")"
webhook_secret="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["webhook_secret"])' "$CREDS_DIR/github-app.json")"

cat <<EOF

Add these lines to $ENV_FILE:

GITHUB_APP_ID=$app_id
GITHUB_APP_SLUG=$app_slug
GITHUB_WEBHOOK_SECRET=$webhook_secret
GITHUB_APP_PRIVATE_KEY_PATH=$CREDS_DIR/github-app.private-key.pem

If the app is not installed yet, install it here:
https://github.com/apps/$app_slug/installations/new
EOF
