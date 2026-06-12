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

prepare_static_build_case() {
  local marker="$1"
  reset_case_repo
  (
    cd "$REPO_DIR"
    mkdir -p public
    printf '<!doctype html><title>Single Server E2E Built Static</title><h1>%s</h1>\n' "$marker" > public/index.html
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

prepare_ops_case() {
  local marker="$1"
  reset_case_repo
  (
    cd "$REPO_DIR"
    cat > server.mjs <<EOF
import fs from "node:fs";
import http from "node:http";
import path from "node:path";

const marker = "$marker";
const port = Number(process.env.PORT || 3000);
const storageDir = process.env.E2E_STORAGE_DIR || "/storage";
const storedPath = path.join(storageDir, "message.txt");

console.log("ops-runtime-log:" + marker);

function send(res, status, body) {
  res.writeHead(status, { "content-type": "text/plain" });
  res.end(body + "\\n");
}

function storedValue() {
  try {
    return fs.readFileSync(storedPath, "utf8").trim();
  } catch {
    return "missing";
  }
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url || "/", "http://localhost");
  if (url.pathname === "/readyz") {
    send(res, 200, "ready");
    return;
  }
  if (url.pathname === "/stored") {
    send(res, 200, storedValue());
    return;
  }
  if (url.pathname === "/write") {
    const value = url.searchParams.get("value") || "";
    fs.mkdirSync(storageDir, { recursive: true });
    fs.writeFileSync(storedPath, value);
    send(res, 200, value);
    return;
  }
  send(res, 200, marker + "|" + (process.env.E2E_GREETING || ""));
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
    static-build) prepare_static_build_case "$marker" ;;
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

try_wait_for_github_push_delivery() {
  local sha="$1"
  local label="$2"
  local deliveries ids id detail delivered_sha status redelivered_ids last_status
  redelivered_ids=""
  last_status=""

  log "Waiting for GitHub webhook delivery for $label"
  for _ in $(seq 1 60); do
    deliveries="$(github_app_api GET "/app/hook/deliveries?per_page=30" 2>/dev/null || true)"
    if [ -z "$deliveries" ]; then
      last_status="delivery-list-unavailable"
      sleep 2
      continue
    fi
    ids="$(printf "%s" "$deliveries" | python3 -c 'import json,sys
for delivery in json.load(sys.stdin):
    if delivery.get("event") == "push":
        print(delivery.get("id", ""))
' 2>/dev/null || true)"
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
    log "GitHub webhook delivery for $label did not become OK; last status '$last_status'"
    return 1
  fi
  log "GitHub webhook delivery for $label did not arrive for $sha"
  return 1
}

wait_for_github_push_delivery() {
  local sha="$1"
  local label="$2"
  if ! try_wait_for_github_push_delivery "$sha" "$label"; then
    fail "GitHub webhook delivery for $label did not arrive or become OK for $sha"
  fi
}

push_case_with_delivery_retry() {
  local prepare_kind="$1"
  local marker_base="$2"
  local message_base="$3"
  local label="$4"
  local attempt marker sha

  PUSHED_MARKER=""
  PUSHED_SHA=""
  for attempt in 1 2 3; do
    marker="$marker_base"
    if [ "$attempt" != "1" ]; then
      marker="$marker_base-retry$attempt"
    fi
    case "$prepare_kind" in
      ops) prepare_ops_case "$marker" ;;
      *) prepare_case_repo "$prepare_kind" "$marker" ;;
    esac
    sha="$(commit_and_push_case "$message_base attempt $attempt")"
    if try_wait_for_github_push_delivery "$sha" "$label attempt $attempt"; then
      PUSHED_MARKER="$marker"
      PUSHED_SHA="$sha"
      return 0
    fi
    log "No usable GitHub webhook delivery for $label attempt $attempt; pushing a fresh commit"
  done
  fail "GitHub webhook delivery for $label did not arrive or become OK after retries"
}

case_public_path() {
  case "$1" in
    dockerfile) printf "/up" ;;
    static) printf "/case.txt" ;;
    static-build) printf "/case.txt" ;;
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
        --non-interactive
      ;;
    static)
      container_exec singleserver add "https://github.com/$GITHUB_TEST_REPO" \
        --name "$APP_NAME" \
        --branch main \
        --domain "$domain" \
        --runtime static \
        --static-dir public \
        --healthcheck-path /ready \
        --non-interactive
      ;;
    static-build)
      container_exec singleserver add "https://github.com/$GITHUB_TEST_REPO" \
        --name "$APP_NAME" \
        --branch main \
        --domain "$domain" \
        --runtime node \
        --install 'mkdir -p .e2e && printf installed > .e2e/install' \
        --build 'test "$(cat .e2e/install)" = installed && mkdir -p dist && cp public/case.txt dist/case.txt && cp public/index.html dist/index.html' \
        --static-dir dist \
        --healthcheck-path /ready \
        --non-interactive
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
        --non-interactive
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

wait_for_domain_verify() {
  local app_name="$1"
  local label="$2"
  local out
  for _ in $(seq 1 90); do
    out="$(docker exec "$CONTAINER" singleserver domains verify "$app_name" 2>&1 || true)"
    if grep -Fq "cloudflare_dns" <<<"$out" && ! grep -Fq "failed" <<<"$out"; then
      return 0
    fi
    sleep 2
  done
  printf 'Last domains verify output for %s:\n%s\n' "$label" "$out" >&2
  fail "$label domain verification did not pass"
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
  verify_live_app_installer_idempotency "$distro" "$case_name" "$APP_NAME" "$public_url" "$initial_marker"

  log "Pushing $case_name change to trigger real GitHub webhook"
  changed_marker="changed-$RUN_ID-$distro-$case_name"
  push_case_with_delivery_retry "$case_name" "$changed_marker" "E2E $distro $case_name change $RUN_ID" "$distro/$case_name change push"
  changed_marker="$PUSHED_MARKER"
  changed_sha="$PUSHED_SHA"
  wait_for_app_marker "$public_url" "$changed_marker" "$distro/$case_name webhook deploy"

  log "Running doctor for $case_name on $distro"
  container_exec singleserver doctor "$APP_NAME"

  log "Removing $case_name app from $distro"
  container_exec singleserver remove "$APP_NAME" --non-interactive
  APP_NAME=""

  log "Verifying Cloudflare DNS cleanup for $domain"
  verify_dns_removed "$domain"
}

run_ops_scenario() {
  local distro="$1"
  local domain alias_domain initial_marker changed_marker public_url stored_url initial_sha changed_sha
  local out backup_path storage_path

  APP_NAME="e2e-$RUN_ID-$distro-ops"
  domain="run-$RUN_ID-$distro-ops.$TEST_ZONE"
  alias_domain="run-$RUN_ID-$distro-ops-alt.$TEST_ZONE"
  public_url="https://$domain/"
  stored_url="https://$domain/stored"
  storage_path="/srv/storage/$APP_NAME"

  log "Preparing operational command coverage app for $distro"
  initial_marker="initial-$RUN_ID-$distro-ops"
  prepare_ops_case "$initial_marker"
  initial_sha="$(commit_and_push_case "E2E $distro ops initial $RUN_ID")"
  wait_for_github_push_delivery "$initial_sha" "$distro/ops initial push"

  log "Adding operational app on $distro"
  container_exec singleserver add "https://github.com/$GITHUB_TEST_REPO" \
    --name "$APP_NAME" \
    --branch main \
    --domain "$domain" \
    --runtime node \
    --start "node server.mjs" \
    --app-port 3000 \
    --healthcheck-path /readyz \
    --non-interactive
  wait_for_app_marker "$public_url" "$initial_marker|" "$distro/ops initial deploy"

  log "Checking list, status, and inspect for $distro"
  out="$(docker exec "$CONTAINER" singleserver list)"
  assert_contains "$out" "$APP_NAME" "$distro list"
  assert_contains "$out" "$domain" "$distro list"
  out="$(docker exec "$CONTAINER" singleserver status)"
  assert_contains "$out" "$APP_NAME" "$distro status"
  assert_contains "$out" "runtime=running:" "$distro status"
  out="$(docker exec "$CONTAINER" singleserver inspect "$APP_NAME")"
  assert_contains "$out" "service: $APP_NAME" "$distro inspect"
  assert_contains "$out" "$domain" "$distro inspect"
  assert_contains "$out" "path: /readyz" "$distro inspect"
  out="$(docker exec "$CONTAINER" singleserver logs "$APP_NAME" --runtime)"
  assert_contains "$out" "ops-runtime-log:$initial_marker" "$distro runtime logs"

  log "Checking env and edit commands for $distro"
  container_exec singleserver env set "$APP_NAME" E2E_GREETING=hello --non-interactive
  out="$(docker exec "$CONTAINER" singleserver env list "$APP_NAME")"
  assert_contains "$out" "E2E_GREETING=hello" "$distro env list"
  container_exec singleserver edit "$APP_NAME" \
    --healthcheck "http://127.0.0.1:8787/health" \
    --healthcheck-path /readyz \
    --no-deploy \
    --non-interactive
  container_exec singleserver deploy "$APP_NAME" --non-interactive
  wait_for_app_marker "$public_url" "$initial_marker|hello" "$distro env deploy"

  log "Checking manual deploy and rollback for $distro"
  changed_marker="changed-$RUN_ID-$distro-ops"
  push_case_with_delivery_retry ops "$changed_marker" "E2E $distro ops change $RUN_ID" "$distro/ops change push"
  changed_marker="$PUSHED_MARKER"
  changed_sha="$PUSHED_SHA"
  wait_for_app_marker "$public_url" "$changed_marker|hello" "$distro ops webhook deploy"
  out="$(docker exec "$CONTAINER" singleserver logs "$APP_NAME")"
  assert_contains "$out" "[deploy:$APP_NAME-" "$distro deploy logs"
  assert_contains "$out" "success total_ms=" "$distro deploy logs"
  container_exec singleserver deploy "$APP_NAME" "$initial_sha" --non-interactive
  wait_for_app_marker "$public_url" "$initial_marker|hello" "$distro ops rollback deploy"
  container_exec singleserver deploy "$APP_NAME" "$changed_sha" --non-interactive
  wait_for_app_marker "$public_url" "$changed_marker|hello" "$distro ops manual deploy"

  log "Checking domains commands for $distro"
  container_exec singleserver domains add "$APP_NAME" "$alias_domain" --no-deploy --non-interactive
  out="$(docker exec "$CONTAINER" singleserver domains list "$APP_NAME")"
  assert_contains "$out" "$domain" "$distro domains list"
  assert_contains "$out" "$alias_domain" "$distro domains list"
  container_exec singleserver deploy "$APP_NAME" --non-interactive
  wait_for_app_marker "https://$alias_domain/" "$changed_marker|hello" "$distro ops alias deploy"
  wait_for_domain_verify "$APP_NAME" "$distro ops alias"
  container_exec singleserver domains remove "$APP_NAME" "$alias_domain" --no-deploy --non-interactive
  container_exec singleserver deploy "$APP_NAME" --non-interactive
  verify_dns_removed "$alias_domain"

  log "Checking storage, backup, and restore commands for $distro"
  container_exec singleserver storage enable "$APP_NAME" --mount /storage --path "$storage_path" --non-interactive
  wait_for_app_marker "$public_url" "$changed_marker|hello" "$distro ops storage deploy"
  wait_for_app_marker "https://$domain/write?value=before-$RUN_ID-$distro" "before-$RUN_ID-$distro" "$distro ops storage write"
  wait_for_app_marker "$stored_url" "before-$RUN_ID-$distro" "$distro ops storage read"
  container_bash "sqlite3 '$storage_path/state.db' 'create table if not exists data(value text); insert into data values (\"before\");'"
  out="$(docker exec "$CONTAINER" singleserver backup "$APP_NAME")"
  assert_contains "$out" "backup" "$distro backup"
  assert_contains "$out" "ok" "$distro backup"
  assert_contains "$out" "sqlite=1" "$distro backup"
  backup_path="$(awk -v app="$APP_NAME" '$1 == app && $2 == "backup" && $3 == "ok" {print $4; exit}' <<<"$out")"
  if [ -z "$backup_path" ]; then
    printf 'Backup output for %s:\n%s\n' "$distro" "$out" >&2
    fail "could not parse backup path"
  fi
  wait_for_app_marker "https://$domain/write?value=after-$RUN_ID-$distro" "after-$RUN_ID-$distro" "$distro ops storage mutate"
  wait_for_app_marker "$stored_url" "after-$RUN_ID-$distro" "$distro ops storage mutated read"
  out="$(docker exec "$CONTAINER" singleserver restore "$APP_NAME" "$backup_path" --no-restart --non-interactive)"
  assert_contains "$out" "restore" "$distro restore no-restart"
  assert_contains "$out" "restart" "$distro restore no-restart"
  assert_contains "$out" "skipped" "$distro restore no-restart"
  assert_contains "$out" "--no-restart" "$distro restore no-restart"
  out="$(docker exec "$CONTAINER" cat "$storage_path/message.txt")"
  assert_contains "$out" "before-$RUN_ID-$distro" "$distro restore no-restart storage"
  container_exec singleserver deploy "$APP_NAME" --non-interactive
  wait_for_app_marker "$stored_url" "before-$RUN_ID-$distro" "$distro ops restore no-restart after deploy"
  wait_for_app_marker "https://$domain/write?value=after-normal-$RUN_ID-$distro" "after-normal-$RUN_ID-$distro" "$distro ops storage mutate after no-restart"
  wait_for_app_marker "$stored_url" "after-normal-$RUN_ID-$distro" "$distro ops storage mutated read after no-restart"
  container_exec singleserver restore "$APP_NAME" "$backup_path" --non-interactive
  wait_for_app_marker "$stored_url" "before-$RUN_ID-$distro" "$distro ops restore"

  log "Checking env unset and removing operational app from $distro"
  container_exec singleserver env unset "$APP_NAME" E2E_GREETING --non-interactive
  out="$(docker exec "$CONTAINER" singleserver env list "$APP_NAME")"
  assert_not_contains "$out" "E2E_GREETING=" "$distro env unset"
  container_exec singleserver remove "$APP_NAME" --delete-storage --non-interactive
  APP_NAME=""
  verify_dns_removed "$domain"
  verify_dns_removed "$alias_domain"
}
