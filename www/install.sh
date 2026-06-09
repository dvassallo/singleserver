#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "Single Server installer must run as root." >&2
  exit 1
fi

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
  binary_url="https://singleserver.com/bin/singleserver-linux-${binary_arch}"
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
echo "Starting first-run setup."

if /usr/local/bin/singleserver tailscale connect; then
  :
else
  echo "tailscale pending: run singleserver tailscale connect"
fi

if [ -n "${CLOUDFLARE_API_TOKEN:-}" ] || [ -n "${CF_API_TOKEN:-}" ] || [ -f /etc/singleserver/cloudflare.json ]; then
  if /usr/local/bin/singleserver cloudflare connect; then
    :
  else
    echo "cloudflare pending: run singleserver cloudflare connect"
  fi
else
  echo "cloudflare pending: set CLOUDFLARE_API_TOKEN, then run singleserver cloudflare connect"
fi

if [ -f /etc/singleserver/github-app.json ] && [ -f /etc/singleserver/github-app.private-key.pem ]; then
  echo "github ok"
elif grep -q "^SINGLESERVER_PUBLIC_URL=" /etc/singleserver/singleserver.env 2>/dev/null; then
  if /usr/local/bin/singleserver github connect; then
    :
  else
    echo "github pending: run singleserver github connect"
  fi
else
  echo "github pending: connect Tailscale first, then run singleserver github connect"
fi

/usr/local/bin/singleserver doctor || true

echo
echo "Next: run singleserver add https://github.com/you/my-app"
