# ETCD structure

## Stability

The structure is currently in development and may change multiple times in a
backwards-incompatible way until a stable major release.
For each *release* (development or stable, since 0.1.0), which changes the structure in such a way,
the structure major version changes too. This is not true for a *commit*.

For details, see "Version" section below.

## Entries

### Structure

The DNS data is a logically hierarchical (by domain) collection of resource record entries ("normal")
and optional entries for default field values ("defaults") and "options" (affecting value interpretations).

* `<prefix>` is the global prefix from configuration (see [README](../README.md)).
All entry keys for pdns-etcd3 must begin with that `<prefix>`, otherwise they are ignored.
The following sections do not mention `<prefix>` anymore, but it always must go first.

* "Zones" are defined by domains having a `SOA` record. The zone domain is used
for automatic appending to unqualified domain names beneath it.
The entries beneath a zone are served with the 'authoritative answer' bit (AA) set.<br>
(TODO `non-auth` option)

* Defaults and options are valid for their level and all levels beneath
(unless overridden in some sublevel).

### Resource Record keys

Resource record keys consist of the concatenated parts `<domain>`, `/<QTYPE>`
and the optional parts `#<id>` and `@<version>` (in that order). `/`, `#` and `@` are literal.

* `<domain>` is the full domain name of a resource record, but in reversed form, with the subdomains separated by `.` or `/` (can be mixed).
The `/` is allowed to support (graphical) tools which apply a logical structure to the flat key namespace in ETCDv3
like in directories and files. (It's really easier to browse it then!)<br>
`<domain>` must be all lowercase, because the queries from PowerDNS are normalized to lowercase;
and the program does not change any names from the entries or queries.

* `<QTYPE>` are the record types, such as `A`, `MX`, and so on.
They must be all uppercase, otherwise they will be mistaken for a domain name part.<br>
`ANY` is not a real record type, so there is nothing to store for it.<br>
(TODO ignore and/or warn about mixed case names)

* `<id>` can be anything but must not contain `@` or `#` (the version and id separators). The content of `<id>` is not interpreted in any way,
but the id as a whole plays a part in defaults and options resolution. See below for details.<br>
It is also *the* way to store multiple values for a resource record (multiple entries with equal domain and QTYPE, but different ids).

* `<version>` is only relevant when upgrading the program which upgrades the data structure to a newer version.
Normally one need not give a version to any entry. After such an upgrade there should be no versioned entries anymore. See below for details.

Examples:
* `com/example/www/A`
* `com/example/NS#1` (record entry with id `1`)
* `com/example/SOA@1.1` (record entry with version `1.1`)
* `com/example/TXT#spf@2` (record entry with id `spf` and version `2`)
* `com.example/dept.fin/SOA` (mixed `.` and `/`, resulting domain is `fin.dept.example.com.`)

### Resource Record values

Resource record values store the content of the record. The content does not include the domain name,
the DNS class (`IN`) or the record type (`A`, `MX`, …); these are given in the entry key already.

The content may be a JSON object or a plain string (that is without quotation marks).
Records which are given as JSON objects, are supported for default values and options (see below for details).<br>
If the content value begins with a `{` (no whitespace before!), it is parsed as a JSON object, otherwise it is taken as plain string.<br>
There are exceptions to this rule: For PowerDNS v3 each JSON-supported record with a priority field
may not be stored as plain string, because the priority of such a record must be reported in a
separate field in the backend protocol. As of PowerDNS v4 the priority field is given in the content
and is not a separate field anymore. Thus, such records could then be given as plain strings.<br>
Also the SOA record cannot be given as a plain string due to the automatically handled 'serial' field.

Not all records are implemented, thus are not JSON-supported. But the list shall be ever-growing.
For the other types there is always the possibility to store them as plain strings.<br>
If a record content is given as a JSON object, but is not supported by the program, it is warned about and ignored.

The record TTL is a regular field in case of a JSON object entry (key `ttl`), but there
is no way to directly define a record-specific TTL for a plain string entry.
One may use a default value as a workaround for this limitation: For example to have a specific TTL
on a record with the (unsupported custom) QTYPE `ABC` one can use the entry
`<domain>/-defaults-/ABC#some-id` → `{"ttl":"<specific-ttl-value>"}`
to specify the TTL for the entry `<domain>/ABC#some-id` → `<plain content for ABC>`.
(TODO add example)

For each record field a default value is searched for and used, if the entry value
does not specify the field value itself. If no value is found for the field,
an error message is logged and the record entry is ignored.

### Version (versioned entries)

Versioned entries are used when upgrading to a higher data version
without interrupting service while adjusting the entries.

The program's data version is given in the release notes and also in the version string logged at program start.
The full version string ("release version") is `<program version>+<data version>` (with a literal `+`).
The default version string is appended by a detailed git version string, if it differs from the release tag.

#### Syntax and rules

A versioned entry has a version number appended to the regular key,
prefixed by `@`: `com/example/NS#1@0.1`.

A version number has the syntax `<major>` or `<major>.<minor>` (`.<minor>` is optional, if minor is zero)
with `<major>` and `<minor>` being non-negative integers.

`<major>` begins with `1` (exception: pre-1.0 development, see below).
Every time when a backward-incompatible change to the structure is introduced,
`<major>` increases and `<minor>` resets to `0`.
Otherwise a change (which should be only additions) increases only `<minor>`.

During the development of first stable release (`1` or `1.0`) the `<major>` number
is `0`, the minor number starts with `1` and acts as the major number regarding
changes. Therefore, there may be another minor number (usually called *patch*),
so that a development data version could be `0.3.2`.

The program ignores all versioned entries, which are either of a different major version
or of a higher minor version (same major version) than the program's data version.
All unversioned entries can be read and used by all program versions (if not overridden
by a supported¹ versioned entry).

For multiple entries with an equivalent key and an equivalent version specification
(same version or unversioned) it is not defined, which entry is taken.
It could be any of those, but only one (no merging applied).

For multiple entries with an equivalent key and different version specifications
the versioned entry with the highest supported version is taken¹.
If no versioned entry is supported, the unversioned entry is taken (if any).

<small>
¹ TODO perhaps it should be only the exact data version of the program
</small>

#### Upgrading

##### Major version change

Upgrading to a higher major version without interrupting service works as follows
(in this example from *old* version `1.1` to *new* version `2`):

1. Ensure that all servers have the *old* (current) version<br>
(that should be an invariant of any upgrading process anyway).
2. Determine which entries must be rewritten (updated), added or deleted.
3. For each entry-to-be-rewritten (in example `com/example/SOA`):
    1. add a new versioned entry of same key with the *old* content and the *old* version:<br>
    `com/example/SOA@1.1` → `<old content>`
    2. rewrite the plain entry with the *new* content<br>
    `com/example/SOA` → `<new content>`<br>
    (the plain entry is ignored yet because all servers currently prefer the versioned entry)
4. For each entry-to-be-added (in example `com/example/NEW`):
    1. add a new versioned entry with that key, the *new* content and the *new* version:<br>
    `com/example/NEW@2` → `<new content>`<br>
    (this entry is ignored yet, because the version is unsupported by the current servers)
5. For each entry-to-be-removed (in example `com/example/OLD`):
    1. add a new versioned entry of same key with the *old* content and the *old* version:<br>
    `com/example/OLD@1.1` → `<old content>`<br>
    (this entry is instantly preferred by the current servers, so be sure to get the content right)
    2. delete the plain entry of same key:<br>
    `com/example/OLD` → *deleted*
6. Upgrade all servers (stop, update, restart), one by one, to the *new* version.<br>
*Tip*: Remove one server from public service, upgrade it and test the new entries.
If it works well, restore public service and continue upgrading the other servers.
7. Remove each versioned entry with *old* (now really old) version:<br>
`com/example/SOA@1.1`, `com/example/OLD@1.1`, … (`*@1.1`)<br>
(unfortunately ETCD does not support suffix requests)
8. For each entry-to-be-added (in example `com/example/NEW`):
    1. Add a new plain entry of same key with *new* content:<br>
    `com/example/NEW` → `<new content>`
    2. Remove the versioned entry of same key with *new* version:
    `com/example/NEW@2` → *deleted*
9. Make a break, you're done!

##### Minor version change

Upgrading to a higher minor version (same major version) is a subset of the steps
from above: 1, 2 (only added entries), 4, 6, 8 and 9.

<small>(There should be a migration script or two…)</small>

#### Current version

The current data version is `0.1` and is described in this document.

### Defaults and options

There are four levels of defaults and options for each domain level (sub-domain):

1. global<br>
Defaults key: `<domain>/-defaults-`<br>
Options key: `<domain>/-options-`<br>
2. QTYPE<br>
Default key: `<domain>/-defaults-/<QTYPE>`<br>
Options key: `<domain>/-options-/<QTYPE>`<br>
3. id<br>
Defaults key: `<domain>/-defaults-/#<id>`<br>
Options key: `<domain>/-options-/#<id>`<br>
4. QTYPE + id<br>
Defaults key: `<domain>/-defaults-/<QTYPE>#<id>`<br>
Options key: `<domain>/-options-/<QTYPE>#<id>`<br>

More specific defaults/options ("values") override the more generic values, field-wise. For the
domain values the sub-domain values override the parent domain values (the levels).
Also the QTYPE values override the non-qtype values At last, the id-only values
override the QTYPE-only values.

For example, for the query `www.example.com` with qtype `A`, the following lists all
defaults entries, with the former overriding the latter. Same goes for options.<br>
The defaults/options with an `#<id>` part are only used for the corresponding `www.example.com`, qtype `A`, id `<id>` normal entry (if any).

* `com/example/www/-defaults-/A#<id>`
* `com/example/www/-defaults-/#<id>`
* `com/example/www/-defaults-/A`
* `com/example/www/-defaults-`
* `com/example/-defaults-/A#<id>`
* `com/example/-defaults-/#<id>`
* `com/example/-defaults-/A`
* `com/example/-defaults-`
* `com/-defaults-/A#<id>`
* `com/-defaults-/#<id>`
* `com/-defaults-/A`
* `com/-defaults-`
* `-defaults-/A#<id>`
* `-defaults-/#<id>`
* `-defaults-/A`
* `-defaults-`

Of course, the values in the record itself (`com/example/www/A#<id>`) override all defaults.

Defaults/options entries must be JSON objects, with any number of fields (including zero).
Defaults/options entries may be non-existent, which is equivalent to an empty object.

Field names of defaults objects are the same as record field names. That means there could
be an ambiguity in non-QTYPE defaults, if different record types define the same
field name. The program only checks for the types of field values, not their content,
so take care yourself.<br>
An example: the `ip` field from `A` is not compatible to the `ip` field from `AAAA`.

## Supported records

For each of the supported record types the entry values may be JSON objects.
The recognized specific field names and syntax are given below for each entry.

All entries can have a `ttl` field, for the record TTL. There must be a TTL value for each record (easy to set as a global default).

### Syntax

*Headings denote the logical type, top level list values the JSON type, sublevels are notes and examples.*

###### "domain name"
* string
    * `"www"`
    * `"www.example.net."`

Domain names undergo a check whether to append the zone name.
The rule is the same as in [BIND][] zone files: if a name ends with a dot, the zone
name is not appended, otherwise it is. This is only possible for JSON-entries.

[bind]: https://www.isc.org/downloads/bind/

###### "duration"
* number
    * seconds, only integral part taken
    * `3600`
* string
    * [duration][tdur] (go.time syntax)
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

## Full example

*To be clear on the value, it's always enclosed in ' (single quotes).*

* `prefix` is `DNS/`<br>
(note the trailing slash, it is part of the prefix, *not* inserted automatically)

Global defaults:
```
DNS/-defaults- → '{"ttl": "1h"}'
DNS/-defaults-/SRV → '{"priority": 0, "weight": 0}'
DNS/-defaults-/SOA → '{"refresh": "1h", "retry": "30m", "expire": 604800, "neg-ttl": "10m"}'
```

Forward zone for `example.net`:
```
DNS/net/example/SOA → '{"primary": "ns1", "mail": "horst.master"}'
DNS/net/example/NS#first → '{"hostname": "ns1"}'
DNS/net/example/NS#second → '{"hostname": "ns2"}'
DNS/net/example/ns1/A → '{"ip": [192, 0, 2, 2]}'
DNS/net/example/ns1/AAAA → '{"ip": "2001:db8::2"}'
DNS/net/example/ns2/A → '{"ip": "192.0.2.3"}'
DNS/net/example/ns2/AAAA → '{"ip": "2001:db8::3"}'
DNS/net/example/-defaults-/MX → '{"ttl": "2h"}'
DNS/net/example/MX#1 → '{"priority": 10, "target": "mail"}'
DNS/net/example/mail/A → '{"ip": [192,0,2,10]}'
DNS/net/example/mail/AAAA → '2001:db8::10'
DNS/net/example/TXT#1 → 'v=spf1 ip4:192.0.2.0/24 ip6:2001:db8::/32 -all'
DNS/net/example/TXT#sth → '{"text":"{text which begins with a curly brace}"}'
DNS/net/example/kerberos1/A#1 → '192.0.2.15'
DNS/net/example/kerberos1/AAAA#1 → '2001:db8::15'
DNS/net/example/kerberos2/A# → '192.0.2.25'
DNS/net/example/kerberos2/AAAA# → '2001:db8::25'
DNS/net/example/_tcp/_kerberos/-defaults-/SRV → '{"port": 88}'
DNS/net/example/_tcp/_kerberos/SRV#1 → '{"target": "kerberos1"}'
DNS/net/example/_tcp/_kerberos/SRV#2 → '{"target": "kerberos2"}'
DNS/net/example/kerberos-master/CNAME → '{"target": "kerberos1"}'
```

Reverse zone for `192.0.2.0/24`:
```
DNS/arpa/in-addr/192/0/2/SOA → '{"primary": "ns1.example.net.", "mail": "horst.master@example.net."}'
DNS/arpa/in-addr/192/0/2/NS#a → '{"hostname": "ns1.example.net."}'
DNS/arpa/in-addr/192/0/2/NS#b → 'ns2.example.net.'
DNS/arpa/in-addr/192/0/2/2/PTR → '{"hostname": "ns1.example.net."}'
DNS/arpa/in-addr/192/0/2/3/PTR → 'ns2.example.net.'
DNS/arpa/in-addr/192/0/2/10/PTR → '{"hostname": "mail.example.net."}'
DNS/arpa/in-addr/192/0/2/15/PTR → 'kerberos1.example.net.'
DNS/arpa/in-addr/192/0/2/25/PTR → 'kerberos2.example.net.'
```

Reverse zone for `2001:db8::/32`:
```
DNS/arpa/ip6/2/0/0/1/0/d/b/8/SOA → '{"primary":"ns1.example.net.", "mail":"horst.master@example.net."}'
DNS/arpa/ip6/2/0/0/1/0/d/b/8/NS#1 → 'ns1.example.net.'
DNS/arpa/ip6/2/0/0/1/0/d/b/8/NS#2 → 'ns2.example.net.'
DNS/arpa/ip6/2/0/0/1/0/d/b/8/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/2/PTR → 'ns1.example.net.'
DNS/arpa/ip6/2/0/0/1/0/d/b/8/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/3/PTR → 'ns2.example.net.'
DNS/arpa/ip6/2/0/0/1/0/d/b/8/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/1/0/PTR → 'mail.example.net.'
DNS/arpa/ip6/2/0/0/1/0/d/b/8/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/1/5/PTR → 'kerberos1.example.net.'
DNS/arpa/ip6/2/0/0/1/0/d/b/8/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/0/2/5/PTR → 'kerberos2.example.net.'
```

Delegation and glue records for `subunit.example.net.`:
```
DNS/net/example/subunit/NS#1 → '{"hostname": "ns1.subunit"}'
DNS/net/example/subunit/NS#2 → '{"hostname": "ns2.subuint"}'
DNS/net/example/subunit/ns1/A → '192.0.3.2'
DNS/net/example/subunit/ns2/A → '192.0.3.3'
```
