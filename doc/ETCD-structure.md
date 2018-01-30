# ETCD structure

## Stability

The structure is currently in development and may change multiple times in a
backwards-incompatible way until a stable major release.

For each *release* (development or stable), which changes the structure in a
backwards-incompatible way, the structure major version changes too.
This is not true for a *commit*.

For details, see "Version" section below.

## Rules

* Record entry values are either JSON objects or plain strings (that is without
quotation marks). If an entry value begins with a `{`, it is parsed as a JSON object,
otherwise it is taken as plain string.<br>
There are exceptions to this rule: each JSON-supported record with a priority field
may not be stored as plain string, due to the incompatibility of backend protocols
of PowerDNS between versions 3 and 4.

* Each record which has a JSON entry value must be supported by the program.
Otherwise an error is emitted and the request/response fails. This is not true for plain strings,
which are returned as-is, without an error, but also without defaults support (except TTL).
This behaviour allows servicing JSON-unsupported records.

* Entry values store the *content* of a record, they do not include the domain name,
the DNS class (`IN`) or the record type (`A`, `MX`, …), these values are
in the key already. They may include a record-specific TTL value, see below rule for details.

* The record TTL is a regular field in case of a JSON object entry (key `"ttl"`), but there
is (currently) no way to define a record-specific TTL for a plain string entry.
One may use a default value as a (nearly equivalent) workaround for this limitation.

* For each record field a default value is searched for and used, if an entry value
does not specify the field value itself. If no value is found for the field,
an error is raised and the request/response fails.

* "Zones" are defined by domains having a `SOA` entry. The zone domain is used
for automatic appending to unqualified domain names beneath it. These entries
(beneath a zone) are served with the 'authoritative answer' bit (AA) set.

## Structure (Entries)

`<prefix>` is the global prefix from configuration (see [README](../README.md)).

### Version (versioned entries)

Versioned entries are used for upgrading the program version with to a higher
major version without interrupting service for rewriting the entries.

#### Syntax and rules

A versioned entry has a version number appended to the regular key, prefixed by `@`: `DNS/www.example.com./A/1@0.1`.

A version number has the syntax `<major>` or `<major>.<minor>`
(minor is optional, if zero)
with `<major>` and `<minor>` being non-negative integers.

`<major>` begins with `1` (exception: pre-1.0 development, see below).
Every time when a backward-incompatible change to the structure is introduced,
`<major>` increases and `<minor>` resets to `0`.
Otherwise a change (which should be only additions) increases only `<minor>`.

During the development of first stable release (`1` or `1.0`) the `<major>` number
is `0`, and the minor number starts with `1` and acts as the major number regarding
changes. Therefore there may be another minor number (usually called *patch*),
so that a development data version could be `0.3.2`.

Version compatibility is as follows:
* The program's version major number must be equal to the data version major number.<br>
Exception: Program version `1.0` (or `1.0.*`) supports data version `1.0` *and* `0.y.*`,
the last pre-1.0-development version (`y` is undetermined yet).
* The program's version minor number must be equal to or greater than the data version minor number. Otherwise the program refuses to work.

**Warning:** Versioning is not implemented yet, not even reading a versioned entry,
so **don't use it yet**. It will be implemented in the first release (0.1).

If two entries with the same key exist, one versioned with the currently supported version,
the other not versioned (plain), the versioned one is taken, the plain one is ignored.

#### Upgrading

Upgrading to a higher major version without interrupting service works as follows
(in this example from *old* version `1.1` to *new* version `2`):

1. Ensure that all servers have the *old* (current) version<br>
(that should be an invariant of any upgrading process anyway).
2. Determine which entries must be rewritten (updated), added or deleted.
3. For each entry-to-be-rewritten (in example `DNS/example.com./SOA`):
    1. add a new versioned entry of same key with the *old* content and the *old* version:<br>
    `DNS/example.com./SOA@1.1` → `<old content>`
    2. rewrite the plain entry with the *new* content<br>
    `DNS/example.com./SOA` → `<new content>`<br>
    (the plain entry is ignored yet because all servers currently prefer the versioned entry)
4. For each entry-to-be-added (in example `DNS/example.com./NEW`):
    1. add a new versioned entry with that key, the *new* content and the *new* version:<br>
    `DNS/example.com./NEW@2` → `<new content>`<br>
    (this entry is ignored yet, because the version is unsupported by the current servers)
5. For each entry-to-be-removed (in example `DNS/example.com./OLD`):
    1. add a new versioned entry of same key with the *old* content and the *old* version:<br>
    `DNS/example.com./OLD@1.1` → `<old content>`<br>
    (this entry is instantly preferred by the current servers, so be sure to get the content right)
    2. delete the plain entry of same key:<br>
    `DNS/example.com./OLD` → *deleted*
6. Upgrade all servers (stop, update, restart), one by one, to the *new* version.<br>
*Tip*: Remove one server from public service, upgrade it and test the new entries.
If it works well, restore public service and continue upgrading the other servers.
7. Remove each versioned entry with *old* (now really old) version:<br>
`DNS/example.com./SOA@1.1`, `DNS/example.com./OLD@1.1`, … (`*@1.1`)<br>
(unfortunately ETCD does not support suffix requests)
8. For each entry-to-be-added (in example `DNS/example.com./NEW`):
    1. Add a new plain entry of same key with *new* content:<br>
    `DNS/example.com./NEW` → `<new content>`
    2. Remove the versioned entry of same key with *new* version:
    `DNS/example.com./NEW@2` → *deleted*
9. Make a break, you're done!

(There should be a migration script…)

#### Current version

The structure version described here is `0.1`.

### Records

Each resource record has at least one corresponding entry in the storage.
Entries are as follows:

* Key: `<prefix><domain>/<QTYPE>/<id>` (the slashes are literal!)
    * `<domain>` is a domain name, e.g. `example.net.` or `www.example.net.`<br>
    It could also be reversed (setting `reversed-names`), which then looks like `net.example.` or `net.example.www.`.
    Consider also the options `no-trailing-dot` and `no-trailing-dot-on-root`.
    * `<QTYPE>` is the type of the resource resource, e.g. `A`, `MX`, …
    * `<id>` is user-defined, it has no meaning in the program, it may even be empty
* Value: `<JSON object>` or `<plain string>`

For multiple values of the same record use multiple `<id>`s. All records
but `SOA` may have multiple values.

#### Exceptions

* For the `SOA` record the entry key is `<prefix><domain>/SOA` (no `/<id>`).
It does not have multiple values.

* The QTYPE `ANY` is not a real record, so nothing to store for it.

### Defaults

There are four top-levels of default values ("defaults"):
1. global<br>
Key: `<prefix>-defaults-/`
2. global + QTYPE<br>
Key: `<prefix>-defaults-/<QTYPE>`
3. domain<br>
Key: `<prefix><domain>/-defaults-/`
4. domain + QTYPE<br>
Key: `<prefix><domain>/-defaults-/<QTYPE>`

More specific defaults override the more generic defaults, field-wise. For the
domain defaults the sub-domain defaults override the parent domain defaults.
Also the QTYPE-defaults override the non-qtype-defaults.

For example, for the query `www.example.com` with qtype `A`, these are all
considered defaults entries, from most specific to most generic (prefix `DNS/`):
* `DNS/www.example.com./-defaults-/A`
* `DNS/www.example.com./-defaults-/`
* `DNS/example.com./-defaults-/A`
* `DNS/example.com./-defaults-/`
* `DNS/com./-defaults-/A`
* `DNS/com./-defaults-/`
* `DNS/./-defaults-/A`
* `DNS/./-defaults-/`
* `DNS/-defaults-/A`
* `DNS/-defaults-/`

Of course, the values in the record(s) itself (`DNS/www.example.com./A/<id>`) override all defaults.

Defaults-entries must be JSON objects, with any number of fields (including zero).
Defaults-entries may be non-existent, which is equivalent to an empty object.

Field names of defaults objects are the same as record field names. That means there could
be an ambiguity in non-QTYPE defaults, if different record types define the same
field name. The program only checks for the types of field values, not their content,
so take care yourself.

## Supported records

For each of the supported record types the entry values may be JSON objects. The recognized
specific field names and syntax are given below for each entry.

All entries can have a `ttl` field, for the record TTL.

### Syntax

*Headings denote the logical type, top level list values the JSON type, sublevels are notes and examples.*

###### "domain name"
* string
    * `"www"`
    * `"www.example.net."`

Domain names undergo a check whether to append the zone name.
The rule is the same as in [BIND][] zone files: if a name ends with a dot, the zone
name is not appended, otherwise it is. This is naturally only possible for JSON-entries.

[bind]: https://www.isc.org/downloads/bind/

###### "duration"
* number
    * seconds, only integral part taken
    * `3600`
* string
    * [duration][tdur]
    * `"1h"`

Values must be positive (that is >= 1 second).

[tdur]: https://golang.org/pkg/time/#ParseDuration

###### "IPv4 address"
* string
    * `"192.168.1.2"`
    * `"::ffff:192.168.1.2"`
    * `"::ffff:c0a8:0102"`
    * `"c0a80102"`
* array of bytes or number strings, length 4
    * `[192, "168", 1, 2]`

###### "IPv6 address"
* string
    * `"2001:0db8::1"`
    * `"2001:db8:0:0:0000:0:0:1"`
    * `"20010db8000000000000000000000001"`
* array of numbers (uint16) or strings (of numbers), length 8
    * `[8193, "0xdb8", "0", 0, 0, 0, 0, 1]`
* array of numbers (uint8) or strings (of numbers), length 16
    * `[32, 1, 13, "0xb8", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1]`

###### "uint16"
* number
    * only integral part is taken
    * range: 0 - 65535
    * `42`

###### "string"
* string
    * taken as-is

### QTYPEs

#### `SOA`

* `primary`: domain name
* `mail`: an e-mail address, in regular syntax (`mail@example.net.`), but the domain name undergoes the zone append check, as described in syntax for "domain name"!
* `refresh`: duration
* `retry`: duration
* `expire`: duration
* `neg-ttl`: duration

There is no serial field, because the program takes the cluster revision as serial.
This way the operator does not have to increase it manually each time he/she changes DNS data.

#### `NS`
* `hostname`: domain name

#### `A`
* `ip`: IPv4 address

#### `AAAA`
* `ip`: IPv6 address

#### `PTR`
* `hostname`: domain name

#### `CNAME`
* `target`: domain name

#### `DNAME`
* `target`: domain name

#### `MX`
* `priority`: uint16
* `target`: domain name

#### `SRV`
* `priority`: uint16
* `weight`: uint16
* `port`: uint16
* `target`: domain name

#### `TXT`
* `text`: string

## Example

*To be clear on the value, it's always enclosed in ' (single quotes).*

* `prefix` is `DNS/`<br>
(note the trailing slash, it is part of the prefix, *not* inserted automatically)
* `reversed-names` is true.<br>
* `no-trailing-dot` is false.<br>
* `no-trailing-dot-on-root` is false.

Global defaults:
```
DNS/-defaults-/ → '{"ttl": "1h"}'
DNS/-defaults-/SRV → '{"priority": 0, "weight": 0}'
DNS/-defaults-/SOA → '{"refresh": "1h", "retry": "30m", "expire": 604800, "neg-ttl": "10m"}'
```

Forward zone for `example.net`:
```
DNS/net.example./SOA → '{"primary": "ns1", "mail": "horst.master"}'
DNS/net.example./NS/first → '{"hostname": "ns1"}'
DNS/net.example./NS/second → '{"hostname": "ns2"}'
DNS/net.example.ns1./A/ → '{"ip": [192, 0, 2, 2]}'
DNS/net.example.ns1./AAAA/ → '{"ip": "2001:db8::2"}'
DNS/net.example.ns2./A/ → '{"ip": "192.0.2.3"}'
DNS/net.example.ns2./AAAA/ → '{"ip": "2001:db8::3"}'
DNS/net.example/-defaults-/MX → '{"ttl": "2h"}'
DNS/net.example./MX/1 → '{"priority": 10, "target": "mail"}'
DNS/net.example.mail./A/ → '{"ip": [192,0,2,10]}'
DNS/net.example.mail./AAAA/ → '2001:db8::10'
DNS/net.example./TXT/1 → 'v=spf1 ip4:192.0.2.0/24 ip6:2001:db8::/32 -all'
DNS/net.example./TXT/sth → '{"text":"{text which begins with a curly brace}"}'
DNS/net.example.kerberos1./A/1 → '192.0.2.15'
DNS/net.example.kerberos1./AAAA/1 → '2001:db8::15'
DNS/net.example.kerberos2./A/ → '192.0.2.25'
DNS/net.example.kerberos2./AAAA/ → '2001:db8::25'
DNS/net.example._tcp._kerberos./-defaults-/SRV → '{"port": 88}'
DNS/net.example._tcp._kerberos./SRV/1 → '{"target": "kerberos1"}'
DNS/net.example._tcp._kerberos./SRV/2 → '{"target": "kerberos2"}'
DNS/net.example.kerberos-master./CNAME/ → '{"target": "kerberos1"}'
```

Reverse zone for IPv4:
```
DNS/arpa.in-addr.192.0.2./SOA → '{"primary": "ns1.example.net.", "mail": "horst.master@example.net."}'
DNS/arpa.in-addr.192.0.2./NS/a → '{"hostname": "ns1.example.net."}'
DNS/arpa.in-addr.192.0.2./NS/b → 'ns2.example.net.'
DNS/arpa.in-addr.192.0.2.2./PTR/ → '{"hostname": "ns1.example.net."}'
DNS/arpa.in-addr.192.0.2.3./PTR/ → 'ns2.example.net.'
DNS/arpa.in-addr.192.0.2.10./PTR/ → '{"hostname": "mail.example.net."}'
DNS/arpa.in-addr.192.0.2.15./PTR/ → 'kerberos1.example.net.'
DNS/arpa.in-addr.192.0.2.25./PTR/ → 'kerberos2.example.net.'
```

Reverse zone for IPv6:
```
DNS/arpa.ip6.2.0.0.1.0.d.b.8./SOA → '{"primary":"ns1.example.net.", "mail":"horst.master@example.net."}'
DNS/arpa.ip6.2.0.0.1.0.d.b.8./NS/1 → 'ns1.example.net.'
DNS/arpa.ip6.2.0.0.1.0.d.b.8./NS/2 → 'ns2.example.net.'
DNS/arpa.ip6.2.0.0.1.0.d.b.8.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.2./PTR/ → 'ns1.example.net.'
DNS/arpa.ip6.2.0.0.1.0.d.b.8.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.3./PTR/ → 'ns2.example.net.'
DNS/arpa.ip6.2.0.0.1.0.d.b.8.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.1.0./PTR/ → 'mail.example.net.'
DNS/arpa.ip6.2.0.0.1.0.d.b.8.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.1.5./PTR/ → 'kerberos1.example.net.'
DNS/arpa.ip6.2.0.0.1.0.d.b.8.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.2.5./PTR/ → 'kerberos2.example.net.'
```

Delegation and glue records:
```
DNS/net.example.subunit./NS/1 → '{"hostname": "ns1.subunit"}'
DNS/net.example.subunit./NS/2 → '{"hostname": "ns2.subuint"}'
DNS/net.example.subunit.ns1./A/ → '192.0.3.2'
DNS/net.example.subunit.ns2./A/ → '192.0.3.3'
```
