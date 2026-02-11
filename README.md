# pdns-etcd3

[![Go Report Card](https://goreportcard.com/badge/github.com/nixn/pdns-etcd3)](https://goreportcard.com/report/github.com/nixn/pdns-etcd3)
[![GitHub release (latest by date including pre-releases)](https://img.shields.io/github/v/release/nixn/pdns-etcd3?include_prereleases&sort=semver&label=latest%20(pre-)release)](https://github.com/nixn/pdns-etcd3/releases)

A [PowerDNS][pdns] [remote backend][pdns-remote] with [ETCD][] v3 cluster as storage.
It uses the [official client][etcd-client] to get the data from the cluster.
Responses are authoritative for each zone found in the data.
Only the DNS class `IN` is supported, but that's because of the limitation of PowerDNS.

There is no stable release yet, even no beta. The latest release is [v0.3.0+0.1.2][],
the third development release, considered alpha quality. Any testing is appreciated.

[pdns]: https://www.powerdns.com/
[pdns-remote]: https://doc.powerdns.com/authoritative/backends/remote.html
[etcd]: https://github.com/etcd-io/etcd/
[etcd-client]: https://github.com/etcd-io/etcd/tree/main/client/v3
[v0.3.0+0.1.2]: https://github.com/nixn/pdns-etcd3/releases/tag/v0.3.0%2B0.1.2

## Features

* Automatic serial for [`SOA` records](doc/ETCD-structure.md#soa) (based on the cluster revision).
* Replication is handled by the ETCD cluster, no additional configuration is needed for using multiple authoritative PowerDNS servers.
  * DNS responses are nearly instantly up-to-date (on every server instance!) after data changes by using a watcher into ETCD (multi-master)
* [Multiple syntax possibilities](doc/ETCD-structure.md#syntax) for (values of) object-supported records
  * [JSON5][] or [YAML][]
  * different representations of values (e.g. an IPv4 as `"192.0.2.1"` or `[192, 0, 2, 1]`, a duration as `2h`, and more...)
* [Short syntax for single-value objects](doc/ETCD-structure.md#resource-record-values)
  * or for the last value left when using defaults (e.g. [`target` in `SRV`](doc/ETCD-structure.md#srv))
* [Default prefix for IP addresses](doc/ETCD-structure.md#a)
  * overrideable per entry
* Support for [custom records (types)](doc/ETCD-structure.md#resource-record-values), like those [supported by PowerDNS][pdns-qtypes] but unimplemented in pdns-etcd3
* Support for [automatically appending zone name to unqualified domain names](doc/ETCD-structure.md#domain-name)
* Override of domain name appended to unqualified names (instead of zone name)
  * useful for [`PTR` records](doc/ETCD-structure.md#ptr) in reverse zones
* Support for defaults and zone appending in most plain-string records (only supported ones)
    * e.g. in an `SRV` entry: `20 5 _ server1`, the port will be searched for in default values, the name `server1` will be appended with the zone name
    * same entry in JSON5 syntax: `{priority: 20, weight: 5, target: "server1"}` (this is longer but clearer)
* [Multi-level defaults and options](doc/ETCD-structure.md#defaults-and-options), overridable
* [Upgrade data structure](doc/ETCD-structure.md#upgrading) (if needed for new program version) without interrupting service
* Run [standalone](#standalone-modes) for usage as a [Unix or HTTP connector][pdns-remote-usage]
  * This could be needed for big data sets, because the initialization from PowerDNS is done lazily (at least as of v4) on first request (which possibly could time out on "big data"…) :-(

[JSON5]: https://json5.org/
[YAML]: https://yaml.org/
[pdns-qtypes]: https://doc.powerdns.com/authoritative/appendices/types.html

#### Planned

* Reduce redundancy in the data by automatically deriving corresponding data
  * `A` ⇒ `PTR` (`in-addr.arpa`)
  * `AAAA` ⇒ `PTR` (`ip6.arpa`)
  * …
* "Collect record", automatically combining A and/or AAAA records from "server records"
  * e.g. `etcd.example.com` based on `etcd-1.example.com`, `etcd-2.example.com`, …
* "Labels" for selectively applying defaults and/or options to record entries
  * sth. like `com/example/-options-ptr` → `{"auto-ptr": true}` and `com/example/www/-options-collect` → `{"collect": …}` for `com/example/www-1/A+ptr+collect` without global options
  * precedence betweeen QTYPE and id (id > label > QTYPE)
* DNSSEC support ([PowerDNS DNSSEC-specific calls][pdns-dnssec])

[pdns-dnssec]: https://doc.powerdns.com/authoritative/appendices/backend-writers-guide.html#dnssec-support
[pdns-remote-usage]: https://doc.powerdns.com/authoritative/backends/remote.html#usage

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
* ZeroMQ connector

## Installation

```shell
git clone https://github.com/nixn/pdns-etcd3.git
cd pdns-etcd3
make
```

NOTE: A plain `go build` will also work, but you will get a dynamically linked executable and incomplete version information in the binary.
The build command in `Makefile` produces a static build with setting the version string properly.
For convenience, it is repeated here:
```shell
export CGO_ENABLED=0
go build -o pdns-etcd3 -a -ldflags="-extldflags=-static -X main.gitVersion=$(git describe --always --dirty)"
```

## Usage

Of course, you need an up and running ETCD v3 cluster and a PowerDNS installation.

You have to decide in which mode you want to use the backend: either the pipe mode or a standalone mode.

### Pipe mode

In pipe mode the backend is launched by PowerDNS dynamically and communicates with it via standard input and output.
All the configuration options must be given in the PowerDNS configuration file. But since PowerDNS (at least as of v4)
initiates the backend lazily, the 'initialize' call occurs with the first (client) request and the backend has to be fast
enough to connect to ETCD, read all data, and reply to this first request. This can be too long, if there is much data to read.

Example PowerDNS configuration file:
```
launch=remote
remote-connection-string=pipe:command=/path/to/pdns-etcd3[,pdns-version=3|4|5][,<config>][,prefix=<string>][,timeout=<integer>][,log-<level>=<components>]
# since in pipe mode every instance connects to ETCD and loads the data for itself (uses memory), possibly do this:
distributor-threads=1
```

`<config>` is one of `config-file=...` or `endpoints=...` (see "Parameters" below for details on the value).
`config-file` overrides `endpoints`.

### Standalone mode(s)

All other modes are so-called "standalone" modes: the backend must be launched outside of PowerDNS (manually, e.g. as a system service).
The standalone mode creates a listening socket and waits for connections (from PowerDNS).
It takes the ETCD related parameters from the command line and connects to it right after starting up.
Then it accepts connections on the socket and serves them.
If the standalone mode begins with an 'initialize' call, only the non-ETCD parameters are available to it.

The data is loaded only once (uses memory only once). The data is loaded before accepting connections from PowerDNS,
so it is available directly after a PowerDNS instance has connected.
It is okay to have parallel accesses to the instance, the data access is protected by mutexes (including updates).

A standalone mode is started by passing the `-standalone=<connector-url>` flag to pdns-etcd3.
The `<connector-url>` must be a valid URL, specific for each mode.

#### Unix

The unix mode uses a UNIX domain socket, thus it can only run on the same system as PowerDNS.
The `<connector-url>` looks like:
```text
unix:///path/to/pdns-etcd3-socket[?relative=<bool>]
```
It gives the path to the socket file (which is then used in the PowerDNS configuration, see below).
`relative` is false by default, so the path is taken as an absolute path.
When set to true, the leading slash is ignored and the path is taken as a relative path.

The unix mode takes an 'initialize' call, so one can pass parameters to it, which are defined in the PowerDNS configuration.

Example PowerDNS configuration file:
```text
launch=remote
remote-connection-string=unix:path=/path/to/pdns-etcd3-socket[,pdns-version=3|4|5][,log-<level>=<components>]
distributor-threads=3
```

#### HTTP

The HTTP mode uses an HTTP listening socket, thus can serve PowerDNS instances from virtually everywhere,
based on the listening address. The `<connector-url>` looks like:
```text
http://<address>:<port>
```
One has to give both, `<address>` and `<port>`. To listen on all interfaces, the URL could look like: `http://0.0.0.0:8053`.

The HTTP mode does not take an 'initialize' call.

In the PowerDNS configuration, the parameters `post` and `post_json` must be both set to a truthy value (e.g. `yes`).
The requests would be rejected otherwise.

Example PowerDNS configuration file:
```text
launch=remote
remote-connection-string=http:url=http://localhost:8053/,post=yes,post_json=yes
```

Because there is no 'initialize' call, the version of a connecting PowerDNS cannot be known (and is not passed in the requests).
Thus, when one has to change the assumed version, they can use the `-pdns-version` option (see below).

### Parameters

All parameter keys must be given exactly as denoted here (no case modifications). The ETCD related parameters in standalone mode
are given as command line "options", starting with a `-`: e.g. `-config-file=...`.

The parameters in detail (the ETCD related parameters, which have to be passed as command line argument in standalone mode,
are tagged by *#STANDALONE*):

* `config-file=/path/to/etcd.conf` *#STANDALONE*<br>
  The path to an ETCD (client) configuration file, as accepted by the official client
  (see [etcd/client/v3/config.go](https://github.com/etcd-io/etcd/blob/master/client/v3/config.go), TODO find documentation)<br>
  TLS and authentication is only possible when using such a configuration file.<br>
  Overrides `endpoints` parameter. Defaults to not set.
* `endpoints=<IP:Port>[|<IP:Port>|...]` *#STANDALONE*<br>
  For a simple connection use the endpoints given here. `endpoints` accepts hostnames too (instead of `IP`), but be sure
  they are resolvable before PowerDNS has started.<br>
  Defaults to `[::1]:2379|127.0.0.1:2379`.
* `prefix=<string>` *#STANDALONE*<br>
  Every entry in ETCD will be prefixed with that. It is not interpreted or changed in any way, also the data watcher uses it,
  so any other keys under another prefix do not affect DNS data.<br>
  Tip: Let the prefix start and end with `/`, so you can use [etcdkeeper][] for easier web-based data management.<br>
  There is no default (= empty).
* `timeout=<duration>` *#STANDALONE* or<br>
  `timeout=<integer>` *config file* (in milliseconds, e.g. `1500` for 1.5 seconds)<br>
  An optional parameter which sets the dial timeout to ETCD. Must be a positive value (>= 1ms).<br>
  Defaults to 2 seconds.
* `pdns-version=3|4|5`<br>
  The (major) PowerDNS version. Version 3 and 4 have incompatible protocols with the backend, so one must use the proper one.
  Version 5 is accepted, but works currently the same as 4 (no relevant API changes yet).<br>
  Defaults to `4`.
* `log-<level>=<components>` *#STANDALONE* and *config file*<br>
  Sets the logging level of `<components>` to `<level>` (see below for values). `<components>` is one or more of the
  component names, separated by `+`. This parameter can be "repeated" for different logging levels.
  In standalone mode, the levels are set separately for the program and the clients (PowerDNS connections).<br>
  Example: `log-debug=main+pdns,log-trace=etcd+data`<br>
  Defaults to `info` for all components.

One can see all available command-line (standalone) parameters with a short description, when running `pdns-etcd3 -help`.

[etcdkeeper]: https://github.com/evildecay/etcdkeeper

### ETCD structure

See [ETCD structure](doc/ETCD-structure.md). The structure lies beneath the `prefix` parameter (see above).

## Compatibility

pdns-etcd3 is tested on different PowerDNS versions (3.y.z, 4.y.z, and 5.y.z) and uses an ETCD v3 cluster (API 3.0 or higher).
It's only one version of each minor (.y), but most likely all (later and earlier) "patch" versions (.z) are compatible.
Therefore, each release shall state which exact versions were used for testing,
so one can be sure to have a working combination for deploying, when using those (tested) versions.

## Testing / Debugging

There is much logging in the program for being able to test and debug it properly.
It is structured and leveled, utilizing [logrus][]. The structure consists of different components,
namely `main`, `pdns`, `etcd` and `data`; the (seven) logging levels are [taken from logrus][logrus-levels].
For each component an own logging level can be set, so that one can debug only the component(s) of interest.
In the standalone modes the components are "doubled"; there is the program side with its components (main, etcd, data) and
the (PDNS) client side (main, pdns), which can be configured separately. In pipe mode there is only one of each component.

The components in detail:
* `main` - The main thread / loop of the program, e.g. setting up logging, creating data objects, processing signals and events, etc.
* `pdns` - The communication with PowerDNS, e.g. incoming requests and sending results.
* `etcd` - The communication with ETCD, e.g. real queries against it, connection issues, watcher, etc.
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

Copyright © 2016-2026 nix <https://keybase.io/nixn>

Distributed under the Apache 2.0 license, available in the file [LICENSE](LICENSE).

## Donations

If you like pdns-etcd3, please consider donating to support the further development. Thank you!

Bitcoin (BTC): `1pdns4U2r4JqkzsJRpTEYNirTFLtuWee9`<br>
Monero (XMR): `4CjXUfpdcba5G5z1LXAx3ngoDtAHoFGdpJWvCayULXeaEhA4QvJEHdR7Xi3ptsbhSfGcSpdBHbK4CgyC6Qcwy5Rt2GGDfQCM7PcTgfEQ5Q`<br>
Ethereum (ETH): `0x003D87efb7069e875a8a1226c9DadaC03dE1f779`

These addresses are dedicated to pdns-etcd3 development.
For my general development, other projects and personal donation addresses see my profile or my web page.
