#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "Single Server installer must run as root." >&2
  exit 1
fi

repo_url="${SINGLESERVER_REPO_URL:-https://github.com/dvassallo/singleserver.git}"
repo_ref="${SINGLESERVER_REF:-main}"
repo_dir="${SINGLESERVER_REPO_DIR:-/srv/repos/singleserver}"

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y ca-certificates curl git build-essential ruby-full golang-go docker.io openssh-server sqlite3

if ! command -v kamal >/dev/null 2>&1; then
  gem install kamal --no-document
fi

arch="$(dpkg --print-architecture)"
case "$arch" in
  amd64) cloudflared_arch=amd64 ;;
  arm64) cloudflared_arch=arm64 ;;
  *) echo "Unsupported architecture for cloudflared: $arch" >&2; exit 1 ;;
esac

if ! command -v cloudflared >/dev/null 2>&1; then
  tmp_deb="/tmp/cloudflared-linux-${cloudflared_arch}.deb"
  curl -fsSL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${cloudflared_arch}.deb" -o "$tmp_deb"
  dpkg -i "$tmp_deb" || apt-get install -f -y
  rm -f "$tmp_deb"
fi
cloudflared_path="$(command -v cloudflared)"
if [ "$cloudflared_path" != "/usr/local/bin/cloudflared" ]; then
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

if [ ! -d "$repo_dir/.git" ]; then
  rm -rf "$repo_dir"
  git clone "$repo_url" "$repo_dir"
else
  git -C "$repo_dir" remote set-url origin "$repo_url"
fi
git -C "$repo_dir" fetch origin "$repo_ref"
git -C "$repo_dir" checkout -q FETCH_HEAD

(cd "$repo_dir" && go build -o /usr/local/bin/singleserver ./cmd/singleserverd)
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

install -m 0644 "$repo_dir/systemd/singleserver.service" /etc/systemd/system/singleserver.service

if ! docker ps -a --format '{{.Names}}' | grep -qx singleserver-registry; then
  docker run -d --restart=always --name singleserver-registry -p 127.0.0.1:5555:5000 registry:2
elif ! docker ps --format '{{.Names}}' | grep -qx singleserver-registry; then
  docker start singleserver-registry
fi

/usr/local/bin/singleserver init

systemctl daemon-reload
systemctl enable --now singleserver.service

echo
echo "Single Server installed."
echo "Next: run singleserver add https://github.com/you/my-app"
