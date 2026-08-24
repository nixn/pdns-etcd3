# pdns-etcd3

[![Go Report Card](https://goreportcard.com/badge/github.com/nixn/pdns-etcd3)](https://goreportcard.com/report/github.com/nixn/pdns-etcd3)
[![GitHub release (latest by date including pre-releases)](https://img.shields.io/github/v/release/nixn/pdns-etcd3?include_prereleases&sort=semver&label=latest%20(pre-)release)](https://github.com/nixn/pdns-etcd3/releases)

A [PowerDNS][pdns] [remote backend][pdns-remote] with [ETCD][] v3 cluster as storage.
It uses the [official client][etcd-client] to get the data from the cluster.
Responses are authoritative for each zone found in the data.
Only the DNS class `IN` is supported, but that's because of the limitation of PowerDNS.

There is no stable release yet, even no beta. The latest release is [v0.4.0+0.2.0][],
the fourth development release, considered alpha quality. Any testing is appreciated.

[pdns]: https://www.powerdns.com/
[pdns-remote]: https://doc.powerdns.com/authoritative/backends/remote.html
[etcd]: https://github.com/etcd-io/etcd/
[etcd-client]: https://github.com/etcd-io/etcd/tree/main/client/v3
[v0.4.0+0.2.0]: https://github.com/nixn/pdns-etcd3/releases/tag/v0.4.0%2B0.2.0

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
* [`ALIAS`](https://doc.powerdns.com/authoritative/guides/alias.html) support
* [Primary (master) mode with AXFR zone transfer](#primary-mode-axfr-zone-transfer)
    * every zone is served to PowerDNS as `MASTER`, so secondaries can `AXFR` it (the `list` remote-backend method)
    * automatic `NOTIFY` on zone changes (via `getUpdatedMasters` / `setNotified`) in any run mode (the notified serial is persisted in ETCD)
    * AXFR ACL by IP (`allow-axfr-ips` / `ALLOW-AXFR-FROM` metadata) and/or [TSIG key](doc/ETCD-structure.md#tsig-keys) (`TSIG-ALLOW-AXFR` metadata)
    * pre-signed DNSSEC zones are transferred as-is
* [Multi-level defaults and options](doc/ETCD-structure.md#defaults-and-options), overridable
* [Domain metadata](https://doc.powerdns.com/authoritative/domainmetadata.html)
    * can also be read and modified with the command line tool `pdnsutil` (`pdnssec` in v3.4)
* [Pre-signed DNSSEC zones](doc/ETCD-structure.md#pre-signed-dnssec)
    * an external signer (e.g. `ldns-signzone`, `dnssec-signzone`, OpenDNSSEC) produces the signed records (`RRSIG`, `NSEC`/`NSEC3`, `DNSKEY`, …) and stores them in ETCD just like any other record
    * `PRESIGNED=1` is set as [metadata](doc/ETCD-structure.md#metadata) on the zone
    * the served SOA serial can be pinned with the [`X-PE3-FIXED-SERIAL`](doc/ETCD-structure.md#metadata) metadata to match the value baked into `RRSIG(SOA)` by the signer
    * online signing is still [planned](#planned)
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
* Online DNSSEC signing ([PowerDNS DNSSEC-specific calls][pdns-dnssec])
  * pre-signed zones are already supported, see [Features](#features) and [ETCD structure](doc/ETCD-structure.md#pre-signed-dnssec)
* [Search][pdns-search] support

[pdns-dnssec]: https://doc.powerdns.com/authoritative/appendices/backend-writers-guide.html#dnssec-support
[pdns-remote-usage]: https://doc.powerdns.com/authoritative/backends/remote.html#usage
[pdns-search]: https://doc.powerdns.com/authoritative/backends/remote.html#searchrecords

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
* Redirecting (or duplicating) log output to something else than stderr

### Overview over the support of optional [PDNS features in a remote backend][pdns-remote]:
* Primary (master): yes — see [Primary mode (AXFR zone transfer)](#primary-mode-axfr-zone-transfer)
  * AXFR support: yes (`list` method), with IP and/or TSIG ACL — requires PowerDNS 4.0+ (the legacy 3.4 remote-backend protocol does not support AXFR-out)
  * automatic NOTIFY: yes, in any run mode (the notified serial is persisted in ETCD)
* (Auto)Secondary: no
* DNSSEC: pre-signed yes, live-signing not yet (planned feature)
  * Metadata: yes
* Search (web API): not yet (planned feature)
* API lookup (for web): not yet
* Disabled zones/domains: no
* Zone caching: yes

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
remote-connection-string=pipe:command=/path/to/pdns-etcd3[,pdns-version=3|4|5][,<config>][,prefix=<string>][,timeout=<integer>][,dial-keep-alive-time=<duration>][,dial-keep-alive-timeout=<duration>][,auto-sync-interval=<duration>][,permit-without-stream=<bool>][,log-level=[<component>[.<subcomponent>]...=]<level>[;...]]
# since in pipe mode every instance connects to ETCD and loads the data for itself (uses memory), possibly do this:
distributor-threads=1
```

`<config>` is one of `config-file=...` or `endpoints=...` (see "Parameters" below for details on the value).
When using `config-file`, all other ETCD configuration flags are ignored.

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
remote-connection-string=unix:path=/path/to/pdns-etcd3-socket[,pdns-version=3|4|5]
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
remote-connection-string=http:url=http://localhost:8053[/client-id=xyz][/pdns-version=3],post=yes,post_json=yes
```

Because there is no 'initialize' call, the version of a connecting PowerDNS must be given as a parameter,
if it differs from the default PDNS version. The default PDNS version can be changed via the `-pdns-version` option (see below).
Other parameters could be set the same way, the URL path replaces the 'initialize' call for the HTTP connector.

### Primary mode (AXFR zone transfer)

pdns-etcd3 reports every zone it holds to PowerDNS as a [primary (master)][pdns-modes],
so PowerDNS can answer outgoing zone transfers (AXFR) and send `NOTIFY` to your secondaries.
On the backend side this is implemented by the remote-backend methods `getDomainInfo` / `getAllDomains`
(report `kind=MASTER`, an integer `id` and a `notified_serial`), `list` (serves the full zone for AXFR),
`getUpdatedMasters` / `getUpdatedPrimaries` + `setNotified` (drive automatic NOTIFY),
and `getTSIGKey` / `getTSIGKeys` (TSIG verification/signing). There is nothing to enable in the backend itself —
just configure PowerDNS and (optionally) the per-zone [metadata](doc/ETCD-structure.md#primary--axfr) below.
Note: TSIG-secured transfers additionally require `remote-dnssec=yes` in PowerDNS (see [TSIG](#tsig)).

The authoritative on-ETCD layout for everything mentioned here is in the ETCD structure document:
[Primary / AXFR](doc/ETCD-structure.md#primary--axfr) and [TSIG keys](doc/ETCD-structure.md#tsig-keys).

[pdns-modes]: https://doc.powerdns.com/authoritative/modes-of-operation.html

#### PowerDNS configuration

Enable primary operation in the PowerDNS configuration (in addition to the `remote-connection-string` from the run-mode
sections above):
```text
# PowerDNS >= 4.5
primary=yes
# PowerDNS < 4.5 use the old spelling instead:
#master=yes
```
For AXFR, PowerDNS notifies the zone's `NS` records plus any [`also-notify`][pdns-also-notify] targets
(per-zone via the `ALSO-NOTIFY` metadata). IP-based AXFR access can be restricted with the PowerDNS
[`allow-axfr-ips`][pdns-allow-axfr-ips] setting (global) and/or the per-zone `ALLOW-AXFR-FROM` metadata.

[pdns-also-notify]: https://doc.powerdns.com/authoritative/settings.html#also-notify
[pdns-allow-axfr-ips]: https://doc.powerdns.com/authoritative/settings.html#allow-axfr-ips

#### Run mode

Both plain AXFR serving (a secondary pulling the zone) and automatic `NOTIFY` on zone changes work in
**any** run mode (pipe or standalone). PowerDNS detects a changed zone by comparing the serial to the last
*notified* serial, which pdns-etcd3 persists in ETCD under a global `-notified-/<id>` entry (kept outside any
zone's prefix so that recording it never bumps a zone's own serial — which would otherwise cause a NOTIFY
feedback loop). Because that state lives in ETCD it is shared across processes, so it also works in pipe mode,
where PowerDNS spawns a fresh short-lived process per request thread.

#### Pointing an external secondary

Configure the secondary (BIND, Knot, PowerDNS, …) to transfer the zone *from your PowerDNS server* (not from ETCD or
pdns-etcd3 directly). Make sure the transfer is permitted: either by IP (`allow-axfr-ips` / `ALLOW-AXFR-FROM`) or by
TSIG (see below), or both. Verify with `dig` (see [Verifying](#verifying) below) before relying on the secondary.

#### TSIG

**Required PowerDNS setting:** enable `remote-dnssec=yes`. PowerDNS's remote backend gates the DNSSEC/TSIG
methods (including `getTSIGKey`) behind the backend's `dnssec` flag — without `remote-dnssec=yes` PowerDNS never
queries the backend for the TSIG key and denies the signed transfer with `NOTAUTH`. (pe3 manages no DNSSEC keys, so
it answers `getDomainKeys` with an empty set; pre-signed zones still work via the `PRESIGNED` metadata.)

TSIG keys are stored as a *global* pseudo-entry in ETCD (not under any zone), read on demand by `getTSIGKey` /
`getTSIGKeys`:
```text
<prefix>-tsig-/<keyname>   →   "<algorithm> <base64-secret>"
# e.g.
<prefix>-tsig-/axfrkey.    →   "hmac-sha256 <base64-secret>"
```
Then authorize the key for a zone's AXFR with the per-zone `TSIG-ALLOW-AXFR` metadata (a list of allowed key names,
e.g. via `pdnsutil set-meta <zone> TSIG-ALLOW-AXFR <keyname>`). Each value names one key, which must match a
`-tsig-/<keyname>` entry.

**Trailing-dot caveat:** `getTSIGKey` does an *exact-match* lookup in ETCD with no name canonicalization, so the key
must be stored under the exact name PowerDNS requests. Whether that name carries a trailing `.` (FQDN) depends on
PowerDNS; to be safe, store the secret under both spellings — `<keyname>` and `<keyname>.` — so the lookup matches
either way.

See [TSIG keys](doc/ETCD-structure.md#tsig-keys) in the ETCD structure document for the authoritative key layout.

#### Pre-signed DNSSEC over AXFR

A [pre-signed DNSSEC zone](doc/ETCD-structure.md#pre-signed-dnssec) is transferred as-is: store the signed records
(`RRSIG`, `NSEC`/`NSEC3`, `DNSKEY`, …) in ETCD like any other record, set `PRESIGNED=1` as metadata on the zone, and
pin the served serial with the [`X-PE3-FIXED-SERIAL`](doc/ETCD-structure.md#reserved-x-pe3--keys) metadata so it matches
the serial the signer baked into `RRSIG(SOA)` (otherwise validating resolvers reject the answer). Delegation `NS` records
and glue are emitted non-authoritative. See the [Pre-signed DNSSEC zones](#features) feature note and the
[ETCD structure section](doc/ETCD-structure.md#pre-signed-dnssec) for details.

#### Verifying

Trigger a transfer from the PowerDNS host to confirm it works:
```shell
# plain AXFR (allowed by allow-axfr-ips / ALLOW-AXFR-FROM)
dig AXFR example.com @<pdns-host>

# TSIG-protected AXFR
dig -y hmac-sha256:<keyname>:<base64-secret> AXFR example.com @<pdns-host>
```
A successful transfer prints the full zone (starting and ending with the `SOA` record).

### Parameters

All parameter keys must be given exactly as denoted here (no case modifications). The ETCD related parameters in standalone mode
are given as command line "options", starting with a `-`: e.g. `-config-file=...`.

The parameters in detail (the parameters, which have to be passed as command line argument in standalone mode,
are tagged by *#STANDALONE*; *pipe mode* means the PDNS setting `remote-connection-string=pipe:...`):

* `config-file=/path/to/etcd.conf` *#STANDALONE*<br>
  The path to an ETCD (client) configuration file, as accepted by the official client
  (see [etcd/client/v3/config.go](https://github.com/etcd-io/etcd/blob/master/client/v3/config.go), TODO find documentation)<br>
  TLS and authentication is only possible when using such a configuration file.<br>
  When used, disables (ignores) all other ETCD configuration flags. Defaults to not set.
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
  `timeout=<integer>` *pipe mode* (in milliseconds, e.g. `1500` for 1.5 seconds)<br>
  An optional parameter which sets the dial timeout to ETCD. Must be a positive value (>= 1ms).<br>
  Defaults to 2 seconds.
* `dial-keep-alive-time=<duration>` *#STANDALONE*<br>
  Interval at which the client sends keep-alive pings to ETCD on the underlying gRPC (HTTP/2) connection.
  These pings allow the client to detect a dead endpoint (e.g. a host that vanished without sending a TCP RST/FIN)
  and rotate to another endpoint instead of waiting for the kernel TCP timeout (~13–15 minutes).
  Set to `0` to disable keep-alive pings.<br>
  Defaults to 10 seconds.
* `dial-keep-alive-timeout=<duration>` *#STANDALONE*<br>
  Time the client waits for an acknowledgement after sending a keep-alive ping. If no ack arrives within this timeout,
  the connection is considered dead and the client reconnects (to another endpoint if available).<br>
  Defaults to 5 seconds.
* `auto-sync-interval=<duration>` *#STANDALONE*<br>
  Interval at which the client refreshes its view of the ETCD cluster member list.
  This makes new endpoints (e.g. cluster members added or rotated in after the client connected) reachable
  without restarting the backend. Set to `0` to disable.<br>
  Defaults to 1 minute.
* `permit-without-stream=<bool>` *#STANDALONE*<br>
  When true, the client sends keep-alive pings even when no RPC stream is active on the connection.
  This is needed to detect a dead endpoint while the backend is idle (e.g. between watch events on a low-traffic deployment).<br>
  Defaults to `true`.
* `pdns-version=3|4|5`<br>
  The (major) PowerDNS version. Version 3 and 4 have incompatible protocols with the backend, so one must use the proper one.
  Version 5 is accepted, but works currently the same as 4 (no relevant API changes yet).<br>
  Defaults to `4`.
* `client-id`<br>
  // TODO describe
* `log-level=[<component>[.<subcomponent>]...=]<level>[;...]` *#STANDALONE* and *pipe mode*<br>
  Sets the logging level(s) for the given (sub)component `<component>[.<subcomponent>]...` to `<level>` (see below for values).
  Leaving out the component names means to set the root level. Can be repeated for other (sub)components by using the `;` separator.
  The order is not important for different (sub)components: `-log-level=4;data.values=2` results in the same effect as `-log-level=data.values=2;4`
  (root level 4 = currently all messages, but omit messages of levels 3+ in `data.values`).<br>
  Currently only numbers are allowed for the `<level>` (names should be supported in a future version).<br>
  Example: `log-level=-1;conn=4;data.values=3`<br>
  Defaults to `0` (info) for the root with no overrides (= all components).

One can see all available command-line (standalone) parameters with a short description, when running `pdns-etcd3 -help`.

[etcdkeeper]: https://github.com/evildecay/etcdkeeper

### ETCD structure

See [ETCD structure](doc/ETCD-structure.md). The structure lies beneath the `prefix` parameter (see above).

## Compatibility

pdns-etcd3 is tested on different PowerDNS versions (3.y.z, 4.y.z, and 5.y.z) and uses an ETCD v3 cluster (API 3.0 or higher).
It's only one version of each minor (.y), but most likely all (later and earlier) "patch" versions (.z) are compatible.
Therefore, each release shall state which exact versions were used for testing,
so one can be sure to have a working combination for deploying, when using those (tested) versions.

*TODO describe (recommended) cache settings*

## Testing / Debugging

There is much logging in the program for being able to test and debug it properly. It is structured and leveled.

The structure consists of different components, namely `main`, `etcd`, `pdns`, `conn` and `data`;
all components (can) have subcomponents, every (sub)component can have its own level, which makes it easier to debug only things of interest.
The components and subcomponents in detail:
* `main` - The main thread / loop of the program, e.g. setting up logging, creating data objects, processing signals and events, etc.
  * `init` - Setting up things (reading configuration, creating main objects, ...).
  * `done` - Program shutdown (waiting for coroutines, ...).
  * `{signal}` - Coroutine for handling OS signals.
* `etcd` - The communication with ETCD, e.g. real queries against it, connection issues, watcher, etc.
  * `watch` - The watcher stuff (events and (re-)connects).
* `pdns` - The communication with PowerDNS, e.g. incoming requests and sending results.
  * `init` - Mostly the `initialize` method handling.
* `conn` - The standalone mode connector(s), setting them up, shutting them down, etc.
  * `unix`, `http` - The connector internal work (sockets, ...).
* `data` - Everything concerning the values (records, ...), parsing data from ETCD, searching records for requests etc.
  * `values` - Messages concerning the concrete values.
  * `locking` - Messages for concurrency control (level 4).

*TODO list and describe all components*

The levels are as follows:
* `FTL` (fatal, `-4`) - Errors which prevent the program to continue service. After a fatal error the program exits. (Mostly in `main` component.)
* `ERR` (error, `-3`) - Errors which don't prevent the program to continue service. Different meanings for different components.
* `WRN` (warning, `-2`) - Not errors, but situations where it could be done better. An admin should take care of those.
* `IMP` (important, `-1`) - Important information on the program, something like "initialized / ready for service".
* `INF` (info, `0`) - Useful information on the program, something like "initializing / connecting / starting". This is the default level for each component.
* `D+1` (debug 1, `1`) - "Coarse debug" ("big steps"), like "Reloading zones", "Handling watch events"
* `D+2` (debug 2, `2`) - "Fine debug", like "reloading zone X", "handling event 1 - PUT ..."
* `D+3` (debug 3, `3`) - "Coarse trace", like "parsing entry key Y"
* `D+4` (debug 4, `4`) - "Fine trace", like "value for X found in Y", coroutine tracing (begin, end, status)

There is no logical limit in the debug levels (and not a real technical limit, being an int),
but currently only the nine described levels are used (three exceptional, two informational, four debug).
Fatal errors cannot be suppressed; (simple) errors can, but that is not recommended (especially in non-data components).
The level for the root log can be set, which takes effect in every component, unless overridden.

For output, the [logrus][] library is used, with its output handling.
Later (optional feature) it could be used to send (selected) output into a file or even a log server.

[logrus]: https://github.com/Sirupsen/logrus

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
