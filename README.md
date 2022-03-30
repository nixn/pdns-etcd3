# pdns-etcd3

[![Go Report Card](https://goreportcard.com/badge/github.com/nixn/pdns-etcd3)](https://goreportcard.com/report/github.com/nixn/pdns-etcd3)

A [PowerDNS][pdns] [remote backend][pdns-remote] with [ETCD][] v3 cluster as storage.
It uses the [official client][etcd-client]
to get the data from the cluster. Responses are authoritative for each zone found in
the data. Only the DNS class `IN` is supported, but that's because of the limitation
of PowerDNS.

There is no stable release yet, even no beta. Currently the first development release (0.1.0)
is being prepared, it could be called 'alpha state'. Any testing is appreciated.

[pdns]: https://www.powerdns.com/
[pdns-remote]: https://doc.powerdns.com/authoritative/backends/remote.html
[etcd]: https://github.com/coreos/etcd/
[etcd-client]: https://github.com/coreos/etcd/tree/master/clientv3/

## Features

* Automatic serial for `SOA` records (based on the cluster revision).
* Replication is handled by the ETCD cluster, no additional configuration is needed for using multiple authoritative PowerDNS servers.
  * DNS respoonses are nearly instantly up-to-date (on every server instance!) after data changes by using a watcher into ETCD (multi-master)
* [Multiple syntax possibilities](doc/ETCD-structure.md#syntax) for JSON-supported records
* Support for custom records (types), like those [supported by PowerDNS][qtypes] but unimplemented in pdns-etcd3
* Support for [automatically appending zone name to unqualified domain names](doc/ETCD-structure.md#domain-name)
* [Multi-level defaults](doc/ETCD-structure.md#defaults-and-options), overridable
* [Upgrade data structure](doc/ETCD-structure.md#upgrading) (if needed for new program version) without interrupting service

[qtypes]: https://doc.powerdns.com/authoritative/appendices/types.html

#### Planned

* Reduce redundancy in the data by automatically deriving corresponding data
  * `A` ⇒ `PTR` (`in-addr.arpa`)
  * `AAAA` ⇒ `PTR` (`ip6.arpa`)
  * …
* Default prefix for IP addresses
  * overrideable per entry
* Override of domain name appended to unqualified names (instead of zone name)
  * useful for `PTR` records in reverse zones
* Support for defaults, zone appending and possibly more in plain-string records (those which are also JSON-supported/implemented)
* "Collect record", automatically combining A and/or AAAA records from "server records"
  * e.g. `etcd.example.com` based on `etcd-1.example.com`, `etcd-2.example.com`, …
* Possibly "labels" for selectively applying defaults and/or options to record entries
  * e.g. `com/example/-options-+ptr` → `{"auto-ptr": true}` and `com/example/WWW-options-+collect` → `{"collect": …}` for `com/example/www-1/A+ptr+collect` without global options
  * precedence betweeen QTYPE and id (id > label > QTYPE)
* Support more encodings for values (beside JSON)
  * [JSON5][]
  * [EDN][] by [go-edn](https://github.com/go-edn/edn)
  * [TOML][] by [pelletier/go-toml](https://github.com/pelletier/go-toml) or [BurntSushi/toml](https://github.com/BurntSushi/toml)
  * possibly [YAML][] by [go-yaml](https://github.com/go-yaml/yaml)
  * …
* DNSSEC support ([PowerDNS DNSSEC-specific calls][pdns-dnssec])
* Run standalone for usage as a [Unix connector][pdns-unix-conn]
  * This could be needed for big data sets, b/c the initialization from PowerDNS is done lazily on first request (which possibly could timeout on "big data"…) :-(
* Perhaps [DNS update support][pdns-update-support], if needed by me or wished by the community

When the required features are done, I should open polls for the optional features ("possibly", "perhaps").

[json5]: https://json5.org/
[edn]: https://github.com/edn-format/edn
[yaml]: http://www.yaml.org/
[toml]: https://github.com/toml-lang/toml
[pdns-dnssec]: https://doc.powerdns.com/authoritative/appendices/backend-writers-guide.html#dnssec-support
[pdns-unix-conn]: https://doc.powerdns.com/authoritative/backends/remote.html#unix-connector
[pdns-update-support]: https://doc.powerdns.com/authoritative/appendices/backend-writers-guide.html#dns-update-support

## Installation

```sh
git clone https://github.com/nixn/pdns-etcd3.git
cd pdns-etcd3
make
```

NOTE: `go build` will also work, but you will get a dynamically linked executable and incomplete version information in the binary.
The build command in `Makefile` produces a static build with setting the version string properly.

## Usage

Of course you need an up and running ETCD v3 cluster and a PowerDNS installation.

### PowerDNS configuration
```
launch+=remote
remote-connection-string=pipe:command=/path/to/pdns-etcd3[,pdns-version=3|4][,<config>][,prefix=<string>][,timeout=<integer>]
```

`pdns-version` is `4` by default, but may be set to `3` to enable PowerDNS v3 compatibility.
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

`timeout` is optional, given in milliseconds and defaults to 2000 (2 seconds). The value must be a positive integer.

### ETCD structure

See [ETCD structure](doc/ETCD-structure.md). The structure lies beneath the `prefix`
configured in PowerDNS (see above).

## Compatibility

pdns-etcd3 is tested on PowerDNS versions 3 and 4, and uses an ETCD v3 cluster.
It's currently only one version of each (pdns 3.x and 4.y, ETCD API 3.0),
until I find a way to test it on different versions easily.
Therefore each release shall state which exact versions were used for testing,
so one can be sure to have a working combination for deploying,
when using those (tested) versions.
Most likely it will work on other "usually compatible" versions,
but that cannot be guaranteed.

## Testing / Debugging

For now, there is very much simple logging, as the program is in heavy development / alpha state.
The plan is to build a logging structure, which can be used to selectively
trace and debug different components. Probably [logrus][] will be used for that.

[logrus]: https://github.com/Sirupsen/logrus

## License

Copyright © 2016-2022 nix <https://keybase.io/nixn>

Distributed under the Apache 2.0 license, available in the file [LICENSE](LICENSE).

## Donations

If you like pdns-etcd3, please consider donating to support the further development. Thank you!

Bitcoin (BTC): `1pdns4U2r4JqkzsJRpTEYNirTFLtuWee9`<br>
Monero (XMR): `4CjXUfpdcba5G5z1LXAx3ngoDtAHoFGdpJWvCayULXeaEhA4QvJEHdR7Xi3ptsbhSfGcSpdBHbK4CgyC6Qcwy5Rt2GGDfQCM7PcTgfEQ5Q`<br>
Ethereum (ETH): `0x003D87efb7069e875a8a1226c9DadaC03dE1f779`

These addresses are dedicated to pdns-etcd3 development.
For my general development, other projects and personal donation addresses see my profile or my web page.
