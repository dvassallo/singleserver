#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "Single Server installer must run as root." >&2
  exit 1
fi

os_id="unknown"
os_pretty="unknown"
os_version="unknown"
if [ -r /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  os_id="${ID:-unknown}"
  os_pretty="${PRETTY_NAME:-$os_id}"
  os_version="${VERSION_ID:-unknown}"
fi

case "$os_id" in
  debian|ubuntu)
    os_family="debian"
    ;;
  amzn)
    case "$os_version" in
      2023|2023.*)
        os_family="amazon"
        ;;
      *)
        cat >&2 <<EOF
Single Server supports Amazon Linux 2023, but this host is running $os_pretty.

Supported operating systems:
- Ubuntu
- Debian
- Amazon Linux 2023
- Rocky Linux 9
EOF
        exit 1
        ;;
    esac
    ;;
  rocky)
    case "$os_version" in
      9|9.*)
        os_family="rocky"
        ;;
      *)
        cat >&2 <<EOF
Single Server supports Rocky Linux 9, but this host is running $os_pretty.

Supported operating systems:
- Ubuntu
- Debian
- Amazon Linux 2023
- Rocky Linux 9
EOF
        exit 1
        ;;
    esac
    ;;
  *)
    cat >&2 <<EOF
Single Server installer currently supports these operating systems:
- Ubuntu
- Debian
- Amazon Linux 2023
- Rocky Linux 9

Detected OS: $os_pretty

Please run it on a fresh server with one of the supported operating systems.
EOF
    exit 1
    ;;
esac

install_os_packages() {
  case "$os_family" in
    debian)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y ca-certificates curl git build-essential ruby-full ruby-dev docker.io docker-buildx openssh-server sqlite3
      ;;
    amazon)
      dnf install -y --setopt=install_weak_deps=False \
        ca-certificates \
        curl-minimal \
        git \
        gcc \
        gcc-c++ \
        make \
        ruby \
        ruby-devel \
        docker \
        openssh-server \
        sqlite \
        systemd \
        procps-ng \
        iproute \
        iptables \
        findutils \
        tar \
        gzip \
        shadow-utils
      ;;
    rocky)
      dnf install -y --setopt=install_weak_deps=False \
        ca-certificates \
        curl-minimal \
        dnf-plugins-core \
        git \
        gcc \
        gcc-c++ \
        make \
        redhat-rpm-config \
        ruby \
        ruby-devel \
        rubygem-io-console \
        rubygem-json \
        openssh-server \
        sqlite \
        systemd \
        procps-ng \
        iproute \
        iptables \
        findutils \
        tar \
        gzip \
        shadow-utils
      dnf config-manager --add-repo https://download.docker.com/linux/rhel/docker-ce.repo
      dnf install -y --setopt=install_weak_deps=False \
        docker-ce \
        docker-ce-cli \
        containerd.io \
        docker-buildx-plugin
      ;;
  esac
}

detect_arch() {
  case "$os_family" in
    debian)
      arch="$(dpkg --print-architecture)"
      ;;
    amazon|rocky)
      arch="$(uname -m)"
      ;;
  esac

  case "$arch" in
    amd64|x86_64)
      cloudflared_arch=amd64
      binary_arch=amd64
      ;;
    arm64|aarch64)
      cloudflared_arch=arm64
      binary_arch=arm64
      ;;
    *)
      echo "Unsupported architecture: $arch" >&2
      exit 1
      ;;
  esac
}

install_cloudflared() {
  if command -v cloudflared >/dev/null 2>&1; then
    return 0
  fi

  case "$os_family" in
    debian)
      tmp_deb="/tmp/cloudflared-linux-${cloudflared_arch}.deb"
      curl -fsSL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${cloudflared_arch}.deb" -o "$tmp_deb"
      dpkg -i "$tmp_deb" || apt-get install -f -y
      rm -f "$tmp_deb"
      ;;
    amazon|rocky)
      tmp_bin="/tmp/cloudflared-linux-${binary_arch}"
      curl -fsSL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${binary_arch}" -o "$tmp_bin"
      install -m 0755 "$tmp_bin" /usr/local/bin/cloudflared
      rm -f "$tmp_bin"
      ;;
  esac
}

# Presentation: a quiet, single-column checklist. Each step's verbose output goes
# to a log, and only a one-line status (TTY-colored) is printed. On failure the
# tail of the log is shown.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  TTY=1
  C_GREEN="$(printf '\033[32m')"; C_RED="$(printf '\033[31m')"; C_DIM="$(printf '\033[2m')"; C_BOLD="$(printf '\033[1m')"; C_RESET="$(printf '\033[0m')"; C_CLR="$(printf '\033[K')"
else
  TTY=0; C_GREEN=; C_RED=; C_DIM=; C_BOLD=; C_RESET=; C_CLR=
fi
INSTALL_LOG="${SINGLESERVER_INSTALL_LOG:-/var/log/singleserver-install.log}"
: > "$INSTALL_LOG" 2>/dev/null || INSTALL_LOG="$(mktemp)"

start_step() {
  [ "$TTY" = 1 ] && printf '  %s…%s %s' "$C_DIM" "$C_RESET" "$1" || true
}
finish_ok() {
  if [ "$TTY" = 1 ]; then printf '\r  %s✓%s %s%s\n' "$C_GREEN" "$C_RESET" "$1" "$C_CLR"; else printf '  - %s\n' "$1"; fi
}
finish_fail() {
  if [ "$TTY" = 1 ]; then printf '\r  %s✗%s %s%s\n' "$C_RED" "$C_RESET" "$1" "$C_CLR"; else printf '  x %s\n' "$1"; fi
  printf '\n%sInstall failed during: %s%s\nLast lines of %s:\n' "$C_RED" "$1" "$C_RESET" "$INSTALL_LOG" >&2
  tail -n 25 "$INSTALL_LOG" >&2 || true
  exit 1
}
run_step() {
  _label="$1"; shift
  start_step "$_label"
  # Run in a subshell with set -e so the step aborts on the first failing
  # command. set -e is ignored inside an `if` condition, so capture the status
  # explicitly with the outer -e disabled, then act on it.
  set +e
  ( set -e; "$@" ) >>"$INSTALL_LOG" 2>&1
  _rc=$?
  set -e
  if [ "$_rc" -eq 0 ]; then finish_ok "$_label"; else finish_fail "$_label"; fi
}

step_docker() {
  if [ -n "${SINGLESERVER_DOCKER_STORAGE_DRIVER:-}" ]; then
    mkdir -p /etc/docker
    printf '{"storage-driver":"%s"}\n' "${SINGLESERVER_DOCKER_STORAGE_DRIVER}" > /etc/docker/daemon.json
  fi
  systemctl enable --now docker
  case "$os_family" in
    debian) systemctl enable --now ssh || true ;;
    amazon|rocky) systemctl enable --now sshd || true ;;
  esac
}

step_kamal() {
  command -v kamal >/dev/null 2>&1 || gem install kamal --no-document
}

step_tailscale() {
  command -v tailscale >/dev/null 2>&1 || curl -fsSL https://tailscale.com/install.sh | sh
  systemctl enable --now tailscaled || true
}

step_cloudflared() {
  install_cloudflared
  cf_path="$(command -v cloudflared || true)"
  if [ -n "$cf_path" ] && [ "$cf_path" != "/usr/local/bin/cloudflared" ]; then
    ln -sf "$cf_path" /usr/local/bin/cloudflared
  fi
}

step_singleserver() {
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

  install_binary
  ln -sf /usr/local/bin/singleserver /usr/local/bin/singleserverd

  if [ ! -f /etc/singleserver/apps.yml ]; then
    printf 'apps: []\n' > /etc/singleserver/apps.yml
  fi
  if [ ! -f /etc/singleserver/singleserver.env ]; then
    cat > /etc/singleserver/singleserver.env <<'ENV_EOF'
SINGLESERVER_CONFIG='/etc/singleserver/apps.yml'
SINGLESERVER_STATE_DIR='/etc/singleserver'
SINGLESERVER_PORT='8787'
ENV_EOF
  fi

  cat > /etc/systemd/system/singleserver.service <<'UNIT_EOF'
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
UNIT_EOF

  if ! docker ps -a --format '{{.Names}}' | grep -qx singleserver-registry; then
    docker run -d --restart=always --name singleserver-registry -p 127.0.0.1:5555:5000 registry:2
  elif ! docker ps --format '{{.Names}}' | grep -qx singleserver-registry; then
    docker start singleserver-registry
  fi

  systemctl daemon-reload
  systemctl enable --now singleserver.service
}

# install_binary fetches the singleserver binary for this host. It supports two
# channels plus an explicit mirror override:
#   - stable (default): the latest tagged GitHub release, verified against its
#     published sha256 checksums.
#   - edge (SINGLESERVER_CHANNEL=edge): the latest build of main from the site.
#   - SINGLESERVER_DOWNLOAD_BASE_URL: an explicit <base>/bin mirror, which takes
#     precedence over the channel (used by the e2e harness and self-host mirrors).
install_binary() {
  channel="${SINGLESERVER_CHANNEL:-stable}"
  tmp_bin="/tmp/singleserver-linux-${binary_arch}"

  if [ -n "${SINGLESERVER_DOWNLOAD_BASE_URL:-}" ]; then
    curl -fsSL "${SINGLESERVER_DOWNLOAD_BASE_URL%/}/bin/singleserver-linux-${binary_arch}" -o "$tmp_bin"
  elif [ "$channel" = "edge" ]; then
    curl -fsSL "https://singleserver.com/bin/singleserver-linux-${binary_arch}" -o "$tmp_bin"
  else
    release_url="https://github.com/dvassallo/singleserver/releases/latest/download"
    tmp_sums="/tmp/singleserver-checksums.txt"
    curl -fsSL "${release_url}/singleserver-linux-${binary_arch}" -o "$tmp_bin"
    curl -fsSL "${release_url}/checksums.txt" -o "$tmp_sums"
    expected="$(grep "singleserver-linux-${binary_arch}" "$tmp_sums" | awk '{print $1}')"
    actual="$(sha256sum "$tmp_bin" | awk '{print $1}')"
    if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
      echo "Single Server: checksum verification failed for singleserver-linux-${binary_arch}." >&2
      rm -f "$tmp_bin" "$tmp_sums"
      exit 1
    fi
    rm -f "$tmp_sums"
  fi

  install -m 0755 "$tmp_bin" /usr/local/bin/singleserver
  rm -f "$tmp_bin"
}

detect_arch
printf '\n%sSingle Server%s · installing on %s (%s)\n\n' "$C_BOLD" "$C_RESET" "$os_pretty" "$binary_arch"

run_step "System packages" install_os_packages
run_step "Docker" step_docker
run_step "Kamal" step_kamal
run_step "Tailscale" step_tailscale
run_step "cloudflared" step_cloudflared

start_step "Single Server"
set +e
( set -e; step_singleserver ) >>"$INSTALL_LOG" 2>&1
_rc=$?
set -e
if [ "$_rc" -eq 0 ]; then
  ss_ver="$(/usr/local/bin/singleserver version 2>/dev/null | head -n1 | awk '{print $2}')"
  if [ -n "$ss_ver" ]; then finish_ok "Single Server $ss_ver"; else finish_ok "Single Server"; fi
else
  finish_fail "Single Server"
fi

if [ "${SINGLESERVER_INSTALL_SKIP_FIRST_RUN:-}" = "1" ] || [ "${SINGLESERVER_INSTALL_SKIP_FIRST_RUN:-}" = "true" ]; then
  echo "Skipping first-run setup."
  exit 0
fi

echo

# Hand off to the guided wizard. It prompts over the terminal when one is
# available and otherwise prints what still needs connecting. Either way it
# exits 0 so a non-interactive install completes cleanly.
if [ -r /dev/tty ] && [ -w /dev/tty ]; then
  /usr/local/bin/singleserver setup < /dev/tty || true
else
  SINGLESERVER_NON_INTERACTIVE=1 /usr/local/bin/singleserver setup || true
fi
