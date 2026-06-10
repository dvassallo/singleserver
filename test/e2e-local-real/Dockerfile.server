FROM ubuntu:26.04

ENV container=docker
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    dbus \
    git \
    iproute2 \
    iptables \
    openssh-client \
    procps \
    python3 \
    systemd \
    systemd-sysv \
  && apt-get clean \
  && rm -rf /var/lib/apt/lists/*

STOPSIGNAL SIGRTMIN+3
VOLUME ["/sys/fs/cgroup"]

CMD ["/sbin/init"]
