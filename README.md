# pdns-etcd3

[![Go Report Card](https://goreportcard.com/badge/github.com/nixn/pdns-etcd3)](https://goreportcard.com/report/github.com/nixn/pdns-etcd3)

A [PowerDNS][pdns] [remote backend][pdns-remote] with [ETCD][] v3 cluster as storage.
It uses the [official client][etcd-client]
to get the data from the cluster. Responses are authoritative for each zone found in
the data. Only the DNS class `IN` is supported, but that's a because of the limitation
of PowerDNS.

There is no stable release yet, even no beta. Any testing is appreciated.

[pdns]: https://www.powerdns.com/
[pdns-remote]: https://doc.powerdns.com/3/authoritative/backend-remote/
[etcd]: https://github.com/coreos/etcd/
[etcd-client]: https://github.com/coreos/etcd/tree/master/clientv3/

## Features

* Automatic serial for `SOA` records (based on the cluster revision).
* Replication is handled by the ETCD cluster, no additional configuration is needed for using multiple authoritative PowerDNS servers.
* [Multiple syntax possibilities for JSON-supported records](doc/ETCD-structure.md#syntax)
* Support for automatically appending zone name to unqualified domain names
* [Multi-level defaults, overridable](doc/ETCD-structure.md#defaults)
* [Upgrade data structure](doc/ETCD-structure.md#upgrading) (if needed for new program version) without interrupting service

#### Planned

* Reduce redundancy in the data by automatically deriving corresponding data
  * `A` ⇒ `PTR` (`in-addr.arpa`)
  * `AAAA` ⇒ `PTR` (`ip6.arpa`)
  * …
* Default prefix for IP addresses
  * overrideable per entry
* Override of domain name appended to unqualified names (instead of zone name)
  * useful for `PTR` records in reverse zones
* Support more encodings for data (beside JSON)
  * [EDN][] by [go-edn][]
  * possibly [YAML][] by [go-yaml][]
  * …
* DNSSEC support (PowerDNS DNSSEC-specific calls)

[edn]: https://github.com/edn-format/edn
[go-edn]: https://github.com/go-edn/edn
[yaml]: http://www.yaml.org/
[go-yaml]: https://github.com/go-yaml/yaml

## Installation

```sh
git clone https://github.com/nixn/pdns-etcd3.git
cd pdns-etcd3
git submodule update --init
make
```

Of course you need an up and running ETCD v3 cluster and a PowerDNS installation.

## Usage

### PowerDNS configuration
```
launch+=remote
remote-connection-string=pipe:command=/path/to/pdns-etcd3[,pdns-version=3|4][,<config>][,prefix=anything][,reversed-names=<boolean>][,timeout=2000]
```

`pdns-version` is `3` by default, but may be set to `4` to enable PowerDNS v4 compatibility.
Version 3 and 4 have incompatible protocols with the backend, so one must use the proper one.

`<config>` is one of
* `configFile=/path/to/etcd-config-file`
* `endpoints=192.168.1.7:2379|192.168.1.8:2379`
* MAYBE LATER (see below) `discovery-srv=example.com`

TLS and authentication is only possible when using the configuration file.

The configuration file is the one accepted by the official client
(see [etcd/clientv3/config.go](https://github.com/coreos/etcd/blob/master/clientv3/config.go),
TODO find documentation).

`endpoints` accepts hostnames too, but be sure they are resolvable before PowerDNS
has started. Same goes for `discovery-srv`; it is undecided yet if this config is needed.

If `<config>` is not given, it defaults to `endpoints=[::1]:2379|127.0.0.1:2379`

`prefix` is optional and is empty by default.

`reversed-names` controls, whether the domain names in the data are in normal or in reversed form
(like for PTR queries). The value is a boolean and accepts the following strings:
`y`, `n`, `yes`, `no`, `true`, `false`, `on`, `off`, `1` and `0` (case-insensitive).
The default is `false`.<br>
**WARNING: Currently `reversed-names` must be set to true due to the current implementation!**

`timeout` is optional and defaults to 2 seconds. The value must be a positive integer,
given in milliseconds.

### ETCD structure

See [ETCD structure](doc/ETCD-structure.md). The structure lies beneath the `prefix`
configured in PowerDNS (see above).

## Compatibility

pdns-etcd3 is tested on PowerDNS versions 3 and 4, and uses an ETCD v3 cluster.
It's currently only one version of each (pdns 3.4.1 and 4.0.3, ETCD API 3.0),
until I find a way to test it on different versions easily.
Therefore each release shall state which versions were used for testing,
so one can be sure to have a working combination for deploying,
when using those (tested) versions.
Most likely it will work on other "usually compatible" versions,
but that cannot be guaranteed.

## Testing / Debugging

For now, there is much simple logging, as the program is in heavy development / alpha state.
The plan is to build a logging structure, which can be used to selectively
trace and debug different components.

## License

Copyright © 2016-2018 nix <https://github.com/nixn>

Distributed under the Apache 2.0 license, available in the file [LICENSE](LICENSE).

## Donations

If you like pdns-etcd3, please consider donating to support the further development. Thank you!

Bitcoin (BTC): `1pdns4U2r4JqkzsJRpTEYNirTFLtuWee9`<br>
Monero (XMR): `4CjXUfpdcba5G5z1LXAx3ngoDtAHoFGdpJWvCayULXeaEhA4QvJEHdR7Xi3ptsbhSfGcSpdBHbK4CgyC6Qcwy5Rt2GGDfQCM7PcTgfEQ5Q`<br>
Ethereum (ETH): `0x003D87efb7069e875a8a1226c9DadaC03dE1f779`

These addresses are dedicated to pdns-etcd3 development.
For my general development, other projects and personal donation addresses see my profile or my web page.
