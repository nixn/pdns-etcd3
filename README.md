# pdns-etcd3

[![Go Report Card](https://goreportcard.com/badge/github.com/nixn/pdns-etcd3)](https://goreportcard.com/report/github.com/nixn/pdns-etcd3)
[![GitHub release (latest by date including pre-releases)](https://img.shields.io/github/v/release/nixn/pdns-etcd3?include_prereleases&sort=semver&label=latest%20(pre-)release)](https://github.com/nixn/pdns-etcd3/releases)

A [PowerDNS][pdns] [remote backend][pdns-remote] with [ETCD][] v3 cluster as storage.
It uses the [official client][etcd-client] to get the data from the cluster.
Responses are authoritative for each zone found in the data.
Only the DNS class `IN` is supported, but that's because of the limitation of PowerDNS.

There is no stable release yet, even no beta. The latest release is [v0.2.0+0.1.1][],
the second development release, considered alpha quality. Any testing is appreciated.

[pdns]: https://www.powerdns.com/
[pdns-remote]: https://doc.powerdns.com/authoritative/backends/remote.html
[etcd]: https://github.com/etcd-io/etcd/
[etcd-client]: https://github.com/etcd-io/etcd/tree/main/client/v3
[v0.2.0+0.1.1]: https://github.com/nixn/pdns-etcd3/releases/tag/v0.2.0%2B0.1.1

## Features

* Automatic serial for [`SOA` records](doc/ETCD-structure.md#soa) (based on the cluster revision).
* Replication is handled by the ETCD cluster, no additional configuration is needed for using multiple authoritative PowerDNS servers.
  * DNS responses are nearly instantly up-to-date (on every server instance!) after data changes by using a watcher into ETCD (multi-master)
* [Multiple syntax possibilities](doc/ETCD-structure.md#syntax) for object-supported records
* [Short syntax for single-value objects](doc/ETCD-structure.md#resource-record-values)
  * or for the last value left when using defaults (e.g. [`target` in `SRV`](doc/ETCD-structure.md#srv))
* [Default prefix for IP addresses](doc/ETCD-structure.md#a)
  * overrideable per entry
* Support for [custom records (types)](doc/ETCD-structure.md#resource-record-values), like those [supported by PowerDNS][pdns-qtypes] but unimplemented in pdns-etcd3
* Support for [automatically appending zone name to unqualified domain names](doc/ETCD-structure.md#domain-name)
* Override of domain name appended to unqualified names (instead of zone name)
  * useful for [`PTR` records](doc/ETCD-structure.md#ptr) in reverse zones
* [Multi-level defaults and options](doc/ETCD-structure.md#defaults-and-options), overridable
* [Upgrade data structure](doc/ETCD-structure.md#upgrading) (if needed for new program version) without interrupting service
* Run [standalone](#unix-mode) for usage as a [Unix connector][pdns-unix-conn]
  * This could be needed for big data sets, because the initialization from PowerDNS is done lazily (at least in v4) on first request (which possibly could time out on "big data"…) :-(

[pdns-qtypes]: https://doc.powerdns.com/authoritative/appendices/types.html

#### Planned

* Reduce redundancy in the data by automatically deriving corresponding data
  * `A` ⇒ `PTR` (`in-addr.arpa`)
  * `AAAA` ⇒ `PTR` (`ip6.arpa`)
  * …
* Support for defaults and zone appending (and possibly more) in plain-string records (those which are also object-supported)
* "Collect record", automatically combining A and/or AAAA records from "server records"
  * e.g. `etcd.example.com` based on `etcd-1.example.com`, `etcd-2.example.com`, …
* "Labels" for selectively applying defaults and/or options to record entries
  * sth. like `com/example/-options-ptr` → `{"auto-ptr": true}` and `com/example/www/-options-collect` → `{"collect": …}` for `com/example/www-1/A+ptr+collect` without global options
  * precedence betweeen QTYPE and id (id > label > QTYPE)
* Support [JSON5][] by [flynn/json5](https://github.com/flynn/json5) (replace default JSON, because JSON5 is a superset of JSON)
* Support [YAML][] by [go-yaml](https://github.com/go-yaml/yaml)
* DNSSEC support ([PowerDNS DNSSEC-specific calls][pdns-dnssec])

[pdns-dnssec]: https://doc.powerdns.com/authoritative/appendices/backend-writers-guide.html#dnssec-support
[pdns-unix-conn]: https://doc.powerdns.com/authoritative/backends/remote.html#unix-connector
[pdns-zone-cache]: https://doc.powerdns.com/authoritative/settings.html#setting-zone-cache-refresh-interval
[json5]: https://json5.org/
[yaml]: http://www.yaml.org/

#### Optional

* Support more encodings for values
  * [EDN](https://github.com/edn-format/edn)
    by [go-edn](https://github.com/go-edn/edn)
  * [TOML](https://github.com/toml-lang/toml)
    by [pelletier/go-toml](https://github.com/pelletier/go-toml)
    or [BurntSushi/toml](https://github.com/BurntSushi/toml)
  * …
* [DNS update support](https://doc.powerdns.com/authoritative/appendices/backend-writers-guide.html#dns-update-support)
* [Prometheus exporter](https://prometheus.io/docs/guides/go-application/)

## Installation

```sh
git clone https://github.com/nixn/pdns-etcd3.git
cd pdns-etcd3
make
```

NOTE: `go build` will also work, but you will get a dynamically linked executable and incomplete version information in the binary.
The build command in `Makefile` produces a static build with setting the version string properly.

## Usage

Of course, you need an up and running ETCD v3 cluster and a PowerDNS installation.

You have to decide in which mode you want to use the backend: either the pipe mode or the unix mode.

### Pipe mode

In pipe mode the backend is launched by PowerDNS dynamically and communicates with it via standard input and output.
All the configuration options must be given in the PowerDNS configuration file. But since PowerDNS (at least as of v4)
initiates the backend lazily, the 'initialize' call occurs with the first (client) request and the backend has to be fast
enough to connect to ETCD, read all data, and reply to this first request. This can be too long, if there is much data to read.

As of PowerDNS v4.5 there is a setting to cache zone data, so the backend would be started and initialized before the
first client request, but this call is currently not implemented (will be implemented later). Due to this the setting
`zone-cache-refresh-interval` currently must be set to `0` (v4.5+).

Example PowerDNS configuration file:
```
launch=remote
remote-connection-string=pipe:command=/path/to/pdns-etcd3[,pdns-version=3|4|5][,<config>][,prefix=<string>][,timeout=<integer>][,log-<level>=<components>]
zone-cache-refresh-interval=0
# since in pipe mode every instance connects to ETCD and loads the data for itself (uses memory), possibly do this:
distributor-threads=1
```

`<config>` is one of `config-file=...` or `endpoints=...` (see "Parameters" below for details on the value).
`config-file` overrides `endpoints`.

### Unix mode

In unix mode the backend must be launched outside PowerDNS (manually, e.g. as a system service). It then creates a unix
domain socket and listens for connections (from PowerDNS). It takes the ETCD related parameters from the command line
and connects to it right after starting up. Then it accepts connections on the socket and serves them.

Each connection still begins with an 'initialize' call, but only the non-ETCD parameters are available to it. In this
mode the data is loaded only once (uses memory only once).

The current restriction on the setting `zone-cache-refresh-interval` (see above) is here valid too, so set it to `0` for now.

Example PowerDNS configuration file:
```
launch=remote
remote-connection-string=unix:path=/path/to/pdns-etcd3-socket[,pdns-version=3|4|5][,log-<level>=<components>]
zone-cache-refresh-interval=0
# in unix mode it is ok to launch multiple access threads, the data is protected by mutexes for concurrent access (including updates)
distributor-threads=3
```

The backend is started in unix mode by passing the `-unix` argument to the executable (see below for details).
It accepts further arguments to configure access to ETCD, one can execute `./pdns-etcd3 -help` for usage information.

### Parameters

All parameter keys must be given exactly as denoted here (no case modifications). The ETCD related parameters in unix mode
are given as command line "options", starting with a `-`: e.g. `-config-file=...`.

The parameters in detail (the ETCD related parameters, which have to be passed as command line argument in unix mode,
are tagged by *#UNIX*):

* `config-file=/path/to/etcd.conf` *#UNIX*<br>
  The path to an ETCD (client) configuration file, as accepted by the official client
  (see [etcd/client/v3/config.go](https://github.com/etcd-io/etcd/blob/master/client/v3/config.go), TODO find documentation)<br>
  TLS and authentication is only possible when using such a configuration file.<br>
  Overrides `endpoints` parameter. Defaults to not set.
* `endpoints=<IP:Port>[|<IP:Port>|...]` *#UNIX*<br>
  For a simple connection use the endpoints given here. `endpoints` accepts hostnames too (instead of `IP`), but be sure
  they are resolvable before PowerDNS has started.<br>
  Defaults to `[::1]:2379|127.0.0.1:2379`.
* `prefix=<string>` *#UNIX*<br>
  Every entry in ETCD will be prefixed with that. It is not interpreted or changed in any way, also the data watcher uses it,
  so any other keys under another prefix do not affect DNS data.<br>
  Currently there seems to be a bug(?) in the ETCD client (not pdns-etcd3), which causes an empty prefix not to work.
  Just use one. Tip: Let the prefix start and end with `/`, so you can use [etcdkeeper][] for easier web-based data management.<br>
  There is no default (= empty).
* `timeout=<duration>` *#UNIX* or<br>
  `timeout=<integer>` *config file* (in milliseconds, e.g. `1500` for 1.5 seconds)<br>
  An optional parameter which sets the dial timeout to ETCD. Must be a positive value (>= 1ms).<br>
  Defaults to 2 seconds.
* `pdns-version=3|4|5`<br>
  The (major) PowerDNS version. Version 3 and 4 have incompatible protocols with the backend, so one must use the proper one.
  Version 5 is accepted, but works currently the same as 4 (no relevant API changes yet).<br>
  Defaults to `4`.
* `log-<level>=<components>` *#UNIX* and *config file*<br>
  Sets the logging level of `<components>` to `<level>` (see below for values). `<components>` is one or more of the
  component names, separated by `+`. This parameter can be "repeated" for different logging levels.
  In unix mode, the levels are set separately for the program and the clients (PowerDNS connections).<br>
  Example: `log-debug=main+pdns,log-trace=etcd+data`<br>
  Defaults to `info` for all components.

[etcdkeeper]: https://github.com/evildecay/etcdkeeper

### ETCD structure

See [ETCD structure](doc/ETCD-structure.md). The structure lies beneath the `prefix` parameter (see above).

## Compatibility

pdns-etcd3 is tested on PowerDNS versions 3.y.z and different 4.y.z, and uses an ETCD v3 cluster (API 3.0 or higher).
It's currently only one version of each minor (.y), but most likely all (later) "patch" versions (.z) are compatible.
Therefore, each release shall state which exact versions were used for testing,
so one can be sure to have a working combination for deploying, when using those (tested) versions.

## Testing / Debugging

There is much logging in the program for being able to test and debug it properly.
It is structured and leveled, utilizing [logrus][]. The structure consists of different components,
namely `main`, `pdns`, `etcd` and `data`; the (seven) logging levels are [taken from logrus][logrus-levels].
For each component an own logging level can be set, so that one can debug only the component(s) of interest.
In the unix mode the components are "doubled", there is the program side with its components (main, etcd, data) and
the (PDNS) client side (main, pdns), which can be configured separately. In pipe mode there is only one of each component.

The components in detail:
* `main` - The main thread / loop of the program, e.g. setting up logging, creating data objects, processing signals and events, etc.
* `pdns` - The communication with PowerDNS, e.g. incoming requests and sending results.
* `etcd` - The communication with ETCD, e.g. real queries against it, connection issues, watchers, etc.
* `data` - Everything concerning the values (records, ...), parsing data from ETCD, searching records for requests etc.

The levels in detail:
* `panic` - Something like the world's end. Actually not used.
* `fatal` - Errors which prevent the program to continue service. After a fatal error the program exits. (Mostly in `main` component.)
* `error` - Errors which don't prevent the program to continue service. Different meanings for different components.
* `warning` (or `warn`) - Not errors, but situations where it could be done better. An admin should take care of those.
* `info` - Useful information on the program, something like "initialized, ready for service". This is the default level for each component.
* `debug` - "Big steps", like "sending request to ETCD", "Handling event" or "default value not found for X"
* `trace` - Small steps and all values, e.g. "found default value for X in Y" or "record: www.example.com./A#some-id = 192.0.2.12"

[logrus]: https://github.com/Sirupsen/logrus
[logrus-levels]: https://github.com/sirupsen/logrus#level-logging

## License

Copyright © 2016-2025 nix <https://keybase.io/nixn>

Distributed under the Apache 2.0 license, available in the file [LICENSE](LICENSE).

## Donations

If you like pdns-etcd3, please consider donating to support the further development. Thank you!

Bitcoin (BTC): `1pdns4U2r4JqkzsJRpTEYNirTFLtuWee9`<br>
Monero (XMR): `4CjXUfpdcba5G5z1LXAx3ngoDtAHoFGdpJWvCayULXeaEhA4QvJEHdR7Xi3ptsbhSfGcSpdBHbK4CgyC6Qcwy5Rt2GGDfQCM7PcTgfEQ5Q`<br>
Ethereum (ETH): `0x003D87efb7069e875a8a1226c9DadaC03dE1f779`

These addresses are dedicated to pdns-etcd3 development.
For my general development, other projects and personal donation addresses see my profile or my web page.
