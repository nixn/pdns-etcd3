FROM debian:jessie-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN echo "deb [trusted=yes] http://archive.debian.org/debian jessie main contrib non-free" > /etc/apt/sources.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends pdns-server pdns-backend-remote && \
    apt-get purge -y --auto-remove && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb

EXPOSE 53/tcp 53/udp

ENTRYPOINT ["/usr/sbin/pdns_server", "--guardian=no", "--daemon=no"]
