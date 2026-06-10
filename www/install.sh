#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "Single Server installer must run as root." >&2
  exit 1
fi

os_id="unknown"
os_pretty="unknown"
if [ -r /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  os_id="${ID:-unknown}"
  os_pretty="${PRETTY_NAME:-$os_id}"
fi

case "$os_id" in
  debian|ubuntu) ;;
  *)
    cat >&2 <<EOF
Single Server installer currently supports Ubuntu and Debian only.

Detected OS: $os_pretty

This installer uses apt, dpkg, systemd, and Debian package names. Please run it
on a fresh Ubuntu or Debian server.
EOF
    exit 1
    ;;
esac

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y ca-certificates curl git build-essential ruby-full ruby-dev docker.io docker-buildx openssh-server sqlite3

if ! command -v kamal >/dev/null 2>&1; then
  gem install kamal --no-document
fi

arch="$(dpkg --print-architecture)"
case "$arch" in
  amd64) cloudflared_arch=amd64; binary_arch=amd64 ;;
  arm64) cloudflared_arch=arm64; binary_arch=arm64 ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac

if ! command -v tailscale >/dev/null 2>&1; then
  curl -fsSL https://tailscale.com/install.sh | sh
fi
systemctl enable --now tailscaled || true

if ! command -v cloudflared >/dev/null 2>&1; then
  tmp_deb="/tmp/cloudflared-linux-${cloudflared_arch}.deb"
  curl -fsSL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${cloudflared_arch}.deb" -o "$tmp_deb"
  dpkg -i "$tmp_deb" || apt-get install -f -y
  rm -f "$tmp_deb"
fi
cloudflared_path="$(command -v cloudflared || true)"
if [ -n "$cloudflared_path" ] && [ "$cloudflared_path" != "/usr/local/bin/cloudflared" ]; then
  ln -sf "$cloudflared_path" /usr/local/bin/cloudflared
fi

if [ -n "${SINGLESERVER_DOCKER_STORAGE_DRIVER:-}" ]; then
  mkdir -p /etc/docker
  cat > /etc/docker/daemon.json <<EOF
{"storage-driver":"${SINGLESERVER_DOCKER_STORAGE_DRIVER}"}
EOF
fi

systemctl enable --now docker
systemctl enable --now ssh || systemctl enable --now sshd || true

if ! id deploy >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash deploy
fi
usermod -aG docker deploy

mkdir -p /root/.ssh /home/deploy/.ssh
chmod 700 /root/.ssh /home/deploy/.ssh
if [ ! -f /root/.ssh/id_ed25519 ]; then
  ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519
fi
touch /home/deploy/.ssh/authorized_keys
if ! grep -qxF "$(cat /root/.ssh/id_ed25519.pub)" /home/deploy/.ssh/authorized_keys; then
  cat /root/.ssh/id_ed25519.pub >> /home/deploy/.ssh/authorized_keys
fi
chown -R deploy:deploy /home/deploy/.ssh
chmod 600 /home/deploy/.ssh/authorized_keys

mkdir -p /srv/repos /srv/storage /srv/backups /etc/singleserver

install_binary() {
  download_base_url="${SINGLESERVER_DOWNLOAD_BASE_URL:-https://singleserver.com}"
  binary_url="${download_base_url%/}/bin/singleserver-linux-${binary_arch}"
  tmp_bin="/tmp/singleserver-linux-${binary_arch}"

  curl -fsSL "$binary_url" -o "$tmp_bin"
  install -m 0755 "$tmp_bin" /usr/local/bin/singleserver
  rm -f "$tmp_bin"
}

install_binary
ln -sf /usr/local/bin/singleserver /usr/local/bin/singleserverd

if [ ! -f /etc/singleserver/apps.yml ]; then
  printf 'apps: []\n' > /etc/singleserver/apps.yml
fi
if [ ! -f /etc/singleserver/singleserver.env ]; then
  cat > /etc/singleserver/singleserver.env <<'EOF'
SINGLESERVER_CONFIG='/etc/singleserver/apps.yml'
SINGLESERVER_STATE_DIR='/etc/singleserver'
SINGLESERVER_PORT='8787'
EOF
fi

cat > /etc/systemd/system/singleserver.service <<'EOF'
[Unit]
Description=Single Server deploy daemon
After=network-online.target docker.service
Wants=network-online.target docker.service

[Service]
Type=simple
WorkingDirectory=/etc/singleserver
EnvironmentFile=/etc/singleserver/singleserver.env
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=/usr/local/bin/singleserverd
Restart=always
RestartSec=2
KillSignal=SIGTERM

[Install]
WantedBy=multi-user.target
EOF

if ! docker ps -a --format '{{.Names}}' | grep -qx singleserver-registry; then
  docker run -d --restart=always --name singleserver-registry -p 127.0.0.1:5555:5000 registry:2
elif ! docker ps --format '{{.Names}}' | grep -qx singleserver-registry; then
  docker start singleserver-registry
fi

systemctl daemon-reload
systemctl enable --now singleserver.service

echo
echo "Single Server installed."

if [ "${SINGLESERVER_INSTALL_SKIP_FIRST_RUN:-}" = "1" ] || [ "${SINGLESERVER_INSTALL_SKIP_FIRST_RUN:-}" = "true" ]; then
  echo "Skipping first-run setup."
  exit 0
fi

echo "Starting first-run setup."

has_tty() {
  [ -r /dev/tty ] && [ -w /dev/tty ]
}

prompt_yes() {
  prompt="$1"
  default="${2:-Y}"
  if ! has_tty; then
    return 1
  fi
  printf "%s " "$prompt" > /dev/tty
  IFS= read -r answer < /dev/tty || answer=""
  answer="$(printf "%s" "$answer" | tr '[:upper:]' '[:lower:]')"
  if [ -z "$answer" ]; then
    answer="$(printf "%s" "$default" | tr '[:upper:]' '[:lower:]')"
  fi
  [ "$answer" = "y" ] || [ "$answer" = "yes" ]
}

prompt_secret() {
  prompt="$1"
  if ! has_tty; then
    printf ""
    return 0
  fi
  printf "%s" "$prompt" > /dev/tty
  old_tty="$(stty -g < /dev/tty 2>/dev/null || true)"
  stty -echo < /dev/tty 2>/dev/null || true
  IFS= read -r value < /dev/tty || value=""
  if [ -n "$old_tty" ]; then
    stty "$old_tty" < /dev/tty 2>/dev/null || true
  else
    stty echo < /dev/tty 2>/dev/null || true
  fi
  printf "\n" > /dev/tty
  printf "%s" "$value"
}

has_public_url() {
  grep -Eq "^SINGLESERVER_PUBLIC_URL=.*https://.*\\.ts\\.net" /etc/singleserver/singleserver.env 2>/dev/null
}

github_connected() {
  [ -f /etc/singleserver/github-app.json ] && [ -f /etc/singleserver/github-app.private-key.pem ]
}

wait_for_github_setup() {
  if ! has_tty; then
    return 1
  fi
  printf "After GitHub says the app is installed, press Enter to continue. " > /dev/tty
  IFS= read -r _ < /dev/tty || true
  github_connected
}

tailscale_running() {
  tailscale status --json 2>/dev/null | grep -q '"BackendState"[[:space:]]*:[[:space:]]*"Running"'
}

has_tailscale_authkey() {
  [ -n "${TAILSCALE_AUTHKEY:-}" ] || [ -n "${TS_AUTHKEY:-}" ]
}

if has_public_url || tailscale_running || has_tailscale_authkey; then
  if /usr/local/bin/singleserver tailscale connect; then
    :
  else
    echo "tailscale pending: run singleserver tailscale connect"
  fi
elif prompt_yes "Connect Tailscale now? This opens a Tailscale login URL. [Y/n]" "Y"; then
  if tailscale up --ssh < /dev/tty; then
    if /usr/local/bin/singleserver tailscale connect; then
      :
    else
      echo "tailscale pending: run singleserver tailscale connect"
    fi
  else
    echo "tailscale pending: run tailscale up --ssh, then run singleserver tailscale connect"
  fi
else
  echo "tailscale pending: run tailscale up --ssh, then run singleserver tailscale connect"
fi

if ! has_public_url; then
  echo "tailscale pending: run tailscale up --ssh, then run singleserver tailscale connect"
fi

if [ -n "${CLOUDFLARE_API_TOKEN:-}" ] || [ -n "${CF_API_TOKEN:-}" ] || [ -f /etc/singleserver/cloudflare.json ]; then
  if /usr/local/bin/singleserver cloudflare connect; then
    :
  else
    echo "cloudflare pending: run singleserver cloudflare connect"
  fi
elif prompt_yes "Connect Cloudflare now? This needs an API token that can manage DNS and tunnels. [Y/n]" "Y"; then
  cf_token="$(prompt_secret "Cloudflare API token: ")"
  if [ -n "$cf_token" ]; then
    if CLOUDFLARE_API_TOKEN="$cf_token" /usr/local/bin/singleserver cloudflare connect; then
      :
    else
      echo "cloudflare pending: run singleserver cloudflare connect"
    fi
  else
    echo "cloudflare pending: set CLOUDFLARE_API_TOKEN, then run singleserver cloudflare connect"
  fi
else
  echo "cloudflare pending: set CLOUDFLARE_API_TOKEN, then run singleserver cloudflare connect"
fi

if github_connected; then
  echo "github ok"
elif grep -q "^SINGLESERVER_PUBLIC_URL=" /etc/singleserver/singleserver.env 2>/dev/null; then
  if /usr/local/bin/singleserver github connect; then
    echo "github pending: open the setup URL above and install the GitHub App"
    if wait_for_github_setup; then
      echo "github ok"
    else
      echo "github pending: run singleserver github connect"
    fi
  else
    echo "github pending: run singleserver github connect"
  fi
else
  echo "github pending: connect Tailscale first, then run singleserver github connect"
fi

/usr/local/bin/singleserver doctor || true

echo
if github_connected; then
  echo "Next: run singleserver add https://github.com/you/my-app"
else
  echo "Next: finish GitHub setup with singleserver github connect, then run singleserver add https://github.com/you/my-app"
fi
