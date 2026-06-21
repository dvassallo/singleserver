# E2E image with Docker CE (docker-ce-cli + containerd.io) already installed,
# reproducing the VPS from PR #1: `apt-get install docker.io` fails there because
# docker.io pulls `containerd`, which conflicts with the present `containerd.io`.
# The installer must detect the existing docker and skip the conflicting packages.
FROM ubuntu:24.04

ENV container=docker
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    dbus \
    git \
    gnupg \
    iproute2 \
    iptables \
    openssh-client \
    procps \
    python3 \
    systemd \
    systemd-sysv \
  && install -m 0755 -d /etc/apt/keyrings \
  && curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc \
  && chmod a+r /etc/apt/keyrings/docker.asc \
  && . /etc/os-release \
  && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${VERSION_CODENAME} stable" \
       > /etc/apt/sources.list.d/docker.list \
  && apt-get update \
  && apt-get install -y --no-install-recommends \
    docker-ce \
    docker-ce-cli \
    containerd.io \
    docker-buildx-plugin \
  && apt-get clean \
  && rm -rf /var/lib/apt/lists/*

# Docker CE's docker.service is enabled, so it auto-starts when the container
# boots — before the installer can apply a storage driver. overlay2 can't mount
# nested in a container, so pin vfs up front (the same driver the harness forces
# for its other distros via SINGLESERVER_DOCKER_STORAGE_DRIVER).
RUN mkdir -p /etc/docker \
  && printf '{"storage-driver":"vfs"}\n' > /etc/docker/daemon.json

STOPSIGNAL SIGRTMIN+3
VOLUME ["/sys/fs/cgroup"]

CMD ["/sbin/init"]
