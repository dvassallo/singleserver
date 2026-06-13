log() {
  printf "\n==> %s\n" "$*"
}

fail() {
  echo "E2E failed: $*" >&2
  exit 1
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"
  if ! grep -Fq -- "$needle" <<<"$haystack"; then
    printf 'Expected %s to contain %q. Output:\n%s\n' "$label" "$needle" "$haystack" >&2
    fail "$label did not contain expected text"
  fi
}

assert_not_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"
  if grep -Fq -- "$needle" <<<"$haystack"; then
    printf 'Expected %s not to contain %q. Output:\n%s\n' "$label" "$needle" "$haystack" >&2
    fail "$label contained unexpected text"
  fi
}

assert_equal() {
  local got="$1"
  local want="$2"
  local label="$3"
  if [ "$got" != "$want" ]; then
    printf 'Expected %s to be %q, got %q\n' "$label" "$want" "$got" >&2
    fail "$label did not match"
  fi
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
    --connect-timeout 10 \
    --max-time 30 \
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

cloudflare_account_id() {
  if [ -n "${CLOUDFLARE_ACCOUNT_ID:-}" ]; then
    printf "%s\n" "$CLOUDFLARE_ACCOUNT_ID"
    return 0
  fi

  cf_api GET "/zones?name=$TEST_ZONE" | json_field result.0.account.id
}

sweep_stale_cloudflare_e2e_tunnels() {
  if [ "${SINGLESERVER_E2E_SKIP_CLOUDFLARE_TUNNEL_SWEEP:-0}" = "1" ]; then
    log "Skipping stale Cloudflare E2E tunnel sweep"
    return 0
  fi

  local account_id page response_file candidates count total_pages tunnel_id tunnel_name tunnel_status
  account_id="$(cloudflare_account_id)"
  if [ -z "$account_id" ]; then
    fail "Could not determine Cloudflare account ID for stale tunnel sweep"
  fi

  log "Sweeping stale Cloudflare E2E tunnels"
  response_file="$(mktemp)"
  count=0
  page=1
  while :; do
    cf_api GET "/accounts/$account_id/cfd_tunnel?is_deleted=false&per_page=100&page=$page" >"$response_file"
    candidates="$(python3 - "$response_file" "$CLOUDFLARE_E2E_TUNNEL_PREFIX" "$CLOUDFLARE_E2E_TUNNEL_CLEANUP_MIN_AGE_SECONDS" <<'PY'
import datetime as dt
import json
import sys

path, prefix, min_age_seconds = sys.argv[1], sys.argv[2], int(sys.argv[3])
now = dt.datetime.now(dt.timezone.utc)

with open(path, "r", encoding="utf-8") as f:
    data = json.load(f)

for tunnel in data.get("result") or []:
    tunnel_id = str(tunnel.get("id") or "")
    name = str(tunnel.get("name") or "")
    if not tunnel_id or not name.startswith(prefix):
        continue
    if tunnel.get("deleted_at"):
        continue
    status = str(tunnel.get("status") or "").lower()
    if status == "healthy":
        continue
    if tunnel.get("connections"):
        continue
    created_at = tunnel.get("created_at")
    if not created_at:
        continue
    try:
        created = dt.datetime.fromisoformat(str(created_at).replace("Z", "+00:00"))
    except ValueError:
        continue
    if (now - created).total_seconds() < min_age_seconds:
        continue
    print(f"{tunnel_id}\t{name}\t{status or 'unknown'}")
PY
)"
    while IFS=$'\t' read -r tunnel_id tunnel_name tunnel_status; do
      if [ -z "$tunnel_id" ]; then
        continue
      fi
      if cf_api DELETE "/accounts/$account_id/cfd_tunnel/$tunnel_id" >/dev/null; then
        count=$((count + 1))
        log "Deleted stale Cloudflare E2E tunnel: $tunnel_name ($tunnel_status)"
      else
        log "Could not delete stale Cloudflare E2E tunnel: $tunnel_name ($tunnel_status)"
      fi
    done <<<"$candidates"

    total_pages="$(python3 - "$response_file" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    data = json.load(f)
print((data.get("result_info") or {}).get("total_pages") or 1)
PY
)"
    if [ "$page" -ge "$total_pages" ]; then
      break
    fi
    page=$((page + 1))
  done
  rm -f "$response_file"

  if [ "$count" -eq 0 ]; then
    log "No stale Cloudflare E2E tunnels found"
  else
    log "Deleted $count stale Cloudflare E2E tunnel(s)"
  fi
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

public_dns_ip_once() {
  local host="$1"
  local ip
  ip="$(dig +short @1.1.1.1 "$host" A | awk '/^[0-9.]+$/ {print; exit}')"
  if [ -n "$ip" ]; then
    printf "%s\n" "$ip"
    return 0
  fi
  dig +short @1.1.1.1 "$host" AAAA | awk '/:/ {print "[" $0 "]"; exit}'
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
