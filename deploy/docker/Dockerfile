FROM docker.m.daocloud.io/library/debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    iproute2 curl wget jq ca-certificates bash \
    iputils-ping dnsutils traceroute mtr-tiny net-tools \
    && rm -rf /var/lib/apt/lists/*
COPY bin/corelink-controller /usr/local/bin/
COPY bin/corelink-node /usr/local/bin/
RUN mkdir -p /data
