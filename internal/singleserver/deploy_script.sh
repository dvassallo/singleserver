set -euo pipefail

now_ms() {
  echo "$(($(date +%s%N) / 1000000))"
}

ignore_checkout_path() {
  path="$1"
  if ! grep -qxF "$path" .git/info/exclude; then
    printf '\n%s\n' "$path" >> .git/info/exclude
  fi
}

start_ms=$(now_ms)
repo_dir="$SINGLESERVER_REPO_DIR"
repo="$SINGLESERVER_REPO"
sha="$SINGLESERVER_SHA"
app_name="$SINGLESERVER_APP_NAME"
env_file="$SINGLESERVER_ENV_FILE"
remote_url="https://x-access-token:${SINGLESERVER_GITHUB_TOKEN}@github.com/${repo}.git"
generated_deploy_file=""
generated_dockerfile_file=""
generated_dockerignore_file=""
dockerignore_backup_file=""

cleanup() {
  if [ -n "$generated_deploy_file" ]; then
    rm -f "$generated_deploy_file"
    rmdir config 2>/dev/null || true
  fi
  if [ -n "$generated_dockerfile_file" ]; then
    rm -f "$generated_dockerfile_file"
  fi
  if [ -n "$dockerignore_backup_file" ]; then
    cp "$dockerignore_backup_file" .dockerignore
    rm -f "$dockerignore_backup_file"
  elif [ -n "$generated_dockerignore_file" ]; then
    rm -f "$generated_dockerignore_file"
  fi
}
trap cleanup EXIT

mkdir -p "$(dirname "$repo_dir")"
if [ ! -d "$repo_dir/.git" ]; then
  rm -rf "$repo_dir"
  git clone --depth=1 "$remote_url" "$repo_dir"
fi

cd "$repo_dir"
git remote set-url origin "$remote_url"
git fetch --depth=1 origin "$sha"
git reset --hard "$sha"
git clean -fdx
git remote set-url origin "https://github.com/${repo}.git"
ignore_checkout_path "/.docker/"

git_done_ms=$(now_ms)

if [ -f Dockerfile ]; then
  dockerfile_source=repo
elif [ -n "$SINGLESERVER_GENERATED_DOCKERFILE" ]; then
  ignore_checkout_path "/Dockerfile"
  generated_dockerfile_file=Dockerfile
  printf '%s' "$SINGLESERVER_GENERATED_DOCKERFILE" > "$generated_dockerfile_file"
  dockerfile_source="$SINGLESERVER_GENERATED_DOCKERFILE_SOURCE"
  if [ -f .dockerignore ]; then
    dockerignore_backup_file="$(mktemp)"
    cp .dockerignore "$dockerignore_backup_file"
  else
    ignore_checkout_path "/.dockerignore"
    generated_dockerignore_file=.dockerignore
    : > .dockerignore
  fi
  cat >> .dockerignore <<'EOF'

# Single Server generated deploy files
.kamal
.git
.singleserver-generated
EOF
else
  echo "dockerfile=missing" >&2
  exit 1
fi
echo "dockerfile=${dockerfile_source}"

if git ls-files --error-unmatch config/deploy.yml >/dev/null 2>&1; then
  deploy_config_source=repo
else
  ignore_checkout_path "/config/deploy.yml"
  rm -f config/deploy.yml
  mkdir -p config
  generated_deploy_file=config/deploy.yml
  printf '%s' "$SINGLESERVER_GENERATED_DEPLOY_YML" > "$generated_deploy_file"
  deploy_config_source=generated
fi
echo "deploy_config=${deploy_config_source}"

mkdir -p .kamal
ignore_checkout_path "/.kamal/secrets"
if [ -f "$env_file" ]; then
  install -m 600 "$env_file" .kamal/secrets
else
  rm -f .kamal/secrets
fi

if docker ps -a --format '{{.Names}}' | grep -Eq "^${app_name}-"; then
  kamal_command=redeploy
else
  kamal_command=setup
fi

GITHUB_SHA="$sha" kamal "$kamal_command" -q
end_ms=$(now_ms)
echo "timing command=${kamal_command} config=${deploy_config_source} git_ms=$((git_done_ms - start_ms)) kamal_ms=$((end_ms - git_done_ms)) total_ms=$((end_ms - start_ms))"
