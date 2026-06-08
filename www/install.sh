#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "Single Server installer must run as root." >&2
  exit 1
fi

binary_base_url="${SINGLESERVER_BINARY_BASE_URL:-https://singleserver.com/bin}"

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y ca-certificates curl git ruby-full docker.io docker-buildx openssh-server sqlite3

if ! command -v kamal >/dev/null 2>&1; then
  gem install kamal --no-document
fi

arch="$(dpkg --print-architecture)"
case "$arch" in
  amd64) cloudflared_arch=amd64; binary_arch=amd64; go_arch=amd64 ;;
  arm64) cloudflared_arch=arm64; binary_arch=arm64; go_arch=arm64 ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac

if ! command -v tailscale >/dev/null 2>&1; then
  curl -fsSL https://tailscale.com/install.sh | sh
fi
systemctl enable --now tailscaled || true

if [ "${SINGLESERVER_INSTALL_CLOUDFLARED:-0}" = "1" ] && ! command -v cloudflared >/dev/null 2>&1; then
  tmp_deb="/tmp/cloudflared-linux-${cloudflared_arch}.deb"
  curl -fsSL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${cloudflared_arch}.deb" -o "$tmp_deb"
  dpkg -i "$tmp_deb" || apt-get install -f -y
  rm -f "$tmp_deb"
fi
cloudflared_path="$(command -v cloudflared || true)"
if [ -n "$cloudflared_path" ] && [ "$cloudflared_path" != "/usr/local/bin/cloudflared" ]; then
  ln -sf "$cloudflared_path" /usr/local/bin/cloudflared
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

install_from_source() {
  repo_url="${SINGLESERVER_REPO_URL:-https://github.com/dvassallo/singleserver.git}"
  repo_ref="${SINGLESERVER_REF:-main}"
  repo_dir="${SINGLESERVER_REPO_DIR:-/srv/repos/singleserver}"

  apt-get install -y build-essential golang-go
  if [ ! -d "$repo_dir/.git" ]; then
    rm -rf "$repo_dir"
    git clone "$repo_url" "$repo_dir"
  else
    git -C "$repo_dir" remote set-url origin "$repo_url"
  fi
  git -C "$repo_dir" fetch origin "$repo_ref"
  git -C "$repo_dir" checkout -q FETCH_HEAD
  (cd "$repo_dir" && CGO_ENABLED=0 GOOS=linux GOARCH="$go_arch" go build -o /usr/local/bin/singleserver ./cmd/singleserverd)
}

install_binary() {
  binary_url="${SINGLESERVER_BINARY_URL:-${binary_base_url}/singleserver-linux-${binary_arch}}"
  tmp_bin="/tmp/singleserver-linux-${binary_arch}"

  if [ "${SINGLESERVER_INSTALL_FROM_SOURCE:-0}" = "1" ]; then
    install_from_source
    return
  fi

  if curl -fsSL "$binary_url" -o "$tmp_bin"; then
    install -m 0755 "$tmp_bin" /usr/local/bin/singleserver
    rm -f "$tmp_bin"
    return
  fi

  echo "Prebuilt Single Server binary was unavailable; falling back to source build." >&2
  install_from_source
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

if [ "${SINGLESERVER_SKIP_INIT:-0}" != "1" ]; then
  # SINGLESERVER_INIT_ARGS is intentionally shell-split for flags like:
  #   SINGLESERVER_INIT_ARGS="--skip-cloudflare"
  # shellcheck disable=SC2086
  /usr/local/bin/singleserver init ${SINGLESERVER_INIT_ARGS:-}
fi

echo
echo "Single Server installed."
echo "Next: run singleserver add https://github.com/you/my-app"
