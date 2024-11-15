FROM debian:12-slim
ENTRYPOINT ["/pdns-etcd3", "-unix=/common/pdns-etcd3.sock", "-endpoints=etcd:2379", "-prefix=DNS/", "-log-debug=main+pdns+etcd+data"]
