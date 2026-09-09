FROM debian:jessie-slim

ENV DEBIAN_FRONTEND=noninteractive

ADD https://snapshot.debian.org/archive/debian/20160105/pool/main/p/pdns/pdns-server_3.4.7-1~bpo8%2B1_amd64.deb /
ADD https://snapshot.debian.org/archive/debian/20160105/pool/main/p/pdns/pdns-backend-remote_3.4.7-1~bpo8%2B1_amd64.deb /

RUN dpkg -i /pdns-*.deb ; \
    set -ex && \
    echo "deb [trusted=yes] http://archive.debian.org/debian/ jessie main contrib non-free" > /etc/apt/sources.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends -f

EXPOSE 53/tcp 53/udp

ENTRYPOINT ["/usr/sbin/pdns_server", "--guardian=no", "--daemon=no"]
