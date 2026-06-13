FROM amazonlinux:2023

ENV container=docker

RUN dnf install -y --setopt=install_weak_deps=False \
    bash \
    ca-certificates \
    curl-minimal \
    dbus \
    git \
    iproute \
    iptables \
    openssh-clients \
    procps-ng \
    python3 \
    systemd \
  && dnf clean all \
  && rm -rf /var/cache/dnf

STOPSIGNAL SIGRTMIN+3
VOLUME ["/sys/fs/cgroup"]

CMD ["/usr/sbin/init"]
