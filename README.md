# pdns-etcd3

A [PowerDNS][pdns] [remote backend][pdns-remote] with [ETCD][etcd] v3 cluster as storage.
It uses the [official client](https://github.com/coreos/etcd/tree/master/clientv3/)
to get the data from the cluster. Responses are authoritative for each zone found in
the data. Only the DNS class `IN` is supported, but that's a limitation of PowerDNS.

## Features

#### Already implemented

* Automatic serial for SOA records (based on the cluster revision).
* Replication is handled by the ETCD cluster, no additional configuration is needed for using multiple authoritative PowerDNS servers.
* Multiple syntax possibilities for supported records

#### Planned

* Default prefix for IP addresses in a zone

## Installation

```sh
go get github.com/nixn/pdns-etcd3
```

Of course you need an up and running ETCD v3 cluster and a PowerDNS installation.

## Usage

### PowerDNS configuration
```
launch+=remote,command=/path/to/pdns-etcd3[,<config>][,prefix=/DNS][,timeout=2000]
```
`<config>` is one of
* `configFile=/path/to/etcd-config-file`
* `endpoints=192.168.1.7:2379|192.168.1.8:2379`
* MAYBE LATER (see below) `discovery-srv=example.com`

TLS and authentication is only possible when using the configuration file.

If `<config>` is not given, it defaults to `endpoints=[::1]:2379|127.0.0.1:2379`

The configuration file is the one accepted by the official client
(see [etcd/clientv3/config.go](https://github.com/coreos/etcd/blob/master/clientv3/config.go),
TODO find documentation).

`endpoints` accepts hostnames too, but be sure they are resolvable before PowerDNS
has started. Same goes for `discovery-srv`; it is undecided yet if this config is needed.

`prefix` is optional and must begin with `/` (if given).

`timeout` is optional and defaults to 2 seconds. The value can be anything parseable
by [`time.ParseDuration`](https://golang.org/pkg/time/#ParseDuration), but only positive values.

### ETCD structure

See [ETCD-structure.md][ETCD-structure]. The structure lies beneath the `prefix`
configured in PowerDNS (see above). For better performance/caching it is
recommended to use a cluster for DNS exclusively:<br>
pdns-etcd3 caches defaults, using the global cluster revision as expiry indicator.
So every time when changing the data in the cluster (that is: changing the revision),
the cached defaults are invalidated and must be loaded again, resulting in additional
calls to the cluster, thus increasing the response latency.

## Compatibility

pdns-etcd3 is tested on PowerDNS&nbsp;3.x and uses an ETCD&nbsp;v3 cluster. It
may work on PowerDNS&nbsp;4.x, but is not tested, though it is planned to
support both eventually, or even only 4.x later. Issues regarding
PowerDNS&nbsp;4.x are not handled yet.

## Debugging

For now, there is much logging, as the program is in alpha state.

[pdns]: https://www.powerdns.com/
[pdns-remote]: https://doc.powerdns.com/3/authoritative/backend-remote/
[etcd]: https://github.com/coreos/etcd/
[etcd-structure]: ETCD-structure.md
