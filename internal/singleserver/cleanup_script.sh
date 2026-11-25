set -euo pipefail

# Post-deploy cleanup. Runs only after a successful deploy and healthcheck.
# Removes stopped app containers beyond retain_containers, the images only they
# used, and build cache unused for 24h. It never touches Docker volumes, the
# local registry's contents or /srv/storage.

repo_dir="$SINGLESERVER_REPO_DIR"
env_file="$SINGLESERVER_ENV_FILE"
generated_deploy_file=""

cleanup() {
  if [ -n "$generated_deploy_file" ]; then
    rm -f "$generated_deploy_file"
    rmdir config 2>/dev/null || true
  fi
}
trap cleanup EXIT

cd "$repo_dir"

if ! git ls-files --error-unmatch config/deploy.yml >/dev/null 2>&1; then
  rm -f config/deploy.yml
  mkdir -p config
  generated_deploy_file=config/deploy.yml
  printf '%s' "$SINGLESERVER_GENERATED_DEPLOY_YML" > "$generated_deploy_file"
fi

mkdir -p .kamal
if [ -f "$env_file" ]; then
  install -m 600 "$env_file" .kamal/secrets
fi

count_lines() {
  "$@" | sort -u | grep -c . || true
}

images_before=$(count_lines docker image ls -q --no-trunc)
stopped_before=$(count_lines docker ps -a -q --filter status=exited)

kamal prune all -q

builder_out=$(docker builder prune -f --filter until=24h)
builder_reclaimed=$(printf '%s\n' "$builder_out" | awk '/^Total/ {print $NF}' | tail -n 1)

images_after=$(count_lines docker image ls -q --no-trunc)
stopped_after=$(count_lines docker ps -a -q --filter status=exited)

echo "cleanup containers_removed=$((stopped_before - stopped_after)) images_removed=$((images_before - images_after)) builder_reclaimed=${builder_reclaimed:-0B}"
