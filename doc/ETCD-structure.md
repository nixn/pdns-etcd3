# ETCD structure

## Stability

The structure is currently in development and may change multiple times in a
backwards-incompatible way until a stable major release.
For each *release* (development or stable, since 0.1.0), which changes the structure in such a way,
the structure major version changes too. This is not true for a *commit*.

For details, see "Version" section below.

## Entries

### Structure

The DNS data is a logically hierarchical collection (by reversed domain) of resource record entries ("normal")
and optional entries for default field values ("defaults") and "options" (affecting value interpretations).

* `<prefix>` is the global prefix from configuration (see [README](../README.md)).
All entry keys for pdns-etcd3 must begin with that `<prefix>`, otherwise they are ignored.
The rest of the document does not mention `<prefix>` anymore, but it always must go first.

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
`<domain>` must be all lowercase. Although PowerDNS does not force lowercase on domain queries, this program converts them internally before querying the database.

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

The content can be one of the following:

* A plain string (that is without quotation marks), if it does not begin with any marker of the other types of content (see below).<br>
  Plain strings give the content of the record directly. They are not parsed or changed in any way, just returned as-is.<br>
  Plain strings have no support for defaults (see below), but they can be used for not supported (or custom) resource records.
  They can be used for supported records too, but that's not cool and even not possible for entries with a priority field,
  when using PowerDNS v3, because the priority of such records must be reported in a separate field in the backend protocol.
  As of PowerDNS v4 the priority fields are part of the content and the restriction does not apply anymore.<br>
  But there is still one exception to this: the `SOA` record cannot be given as a plain string due to the automatically
  handled `serial` field.<br>
  Subject to change (NOT YET IMPLEMENTED): Plain strings for probably most or even all object-supported records will be parsed,
  gaining support for defaults, syntax-checking and more without using object-style notation.

* A forced plain string, if it begins with `` ` `` (a backquote) **(NOT YET IMPLEMENTED)**.<br>
  Effectively the same as a normal plain string, but no interpretation as a special notation (other markers from below) is applied.
  The leading backquote is not included in the resulting value (string). The string remains subject to parsing.

* A JSON object, if it begins with `{`.<br>
  Objects are the heart of the data. They store values for the content fields, have multiple syntax possibilities,
  are supported for default/inherited values and options handling (see below for details).<br>
  Objects can only be used for supported resource records. See below for more details to object-supported records.

* A last-field-value, if it begins with `=`.<br>
  If an object-supported resource record has only one field (e.g. `CNAME`, `A`), or only one field left which is not set
  by some default value (e.g. `SRV`, when `weight`, `priority` and `port` are given by defaults with `target` as the
  "last field"), then the value for that field could be stored with only the value after the `=`. This is very handy
  for such records and prevents much boilerplate.<br>
  The value must be given in JSON syntax.<br>
  Examples:
    * `com.example/www-1/A` => `="1.2.3.4"` (fills the `ip` field)
    * `com.example/www-2/A` => `=7` (when the option `ip-prefix` is set to something like `1.2.3.`)
    * `com.example/NS#1` => `="ns1"` (still utilizing the automatic zone appending)

* A YAML object, if it begins with `---`, followed by a newline. **(NOT IMPLEMENTED YET)**<br>
  Same as a JSON object, but written in YAML syntax.

(All markers do not accept whitespace before them, they would be read as plain strings then.)

Not all records are implemented, thus are not object-supported. But the list shall be ever-growing.
For the other types there is always the possibility to store them as plain strings.<br>
If a record content is given as an object, but is not supported by the program, it is warned about and ignored.
(TODO unsupported entries with values starting with markers like `{` or `=`)

The record TTL is a regular field in case of an object entry (key `ttl`), but there
is no way to directly define a record-specific TTL for a plain string entry.
One may use a default value as a workaround for this limitation: For example to have a specific TTL
on a record with the (currently unsupported) QTYPE `HINFO` one can use the entry
`<domain>/-defaults-/HINFO` → `{"ttl":"<specific-ttl-value>"}`
to specify the TTL for the entry `<domain>/HINFO` → `<plain content for HINFO>`.<br>

For each record field a default value is searched for and used, if the entry value
does not specify the field value itself. If no value is found for the field,
an error message is logged and the record entry is ignored.

### Version (versioned entries)

Versioned entries are used when upgrading to a higher data version
without interrupting service while adjusting the entries.

The program's data version is given in the release notes and also in the version string logged at program start.
The full version string ("release version") is `<program version>+<data version>` (with a literal `+`).
The release version string is appended by a detailed git version string, if it differs from the release tag.

#### Syntax and rules

A versioned entry has a version number appended to the regular key,
prefixed by `@`: `com/example/NS#1@0.1`.

A version number has the syntax `<major>` or `<major>.<minor>` (`.<minor>` is optional, if minor is zero)
with `<major>` and `<minor>` being non-negative integers.

`<major>` begins with `1` (exception: pre-1.0 development, see below).
Every time when a backward-incompatible change to the structure is introduced,
`<major>` increases and `<minor>` resets to `0`.
Otherwise, a change (which should be only additions) increases only `<minor>`.

During the development of the first stable release (`1` or `1.0`) the `<major>` number is `0`,
the minor number starts with `1` and acts as the major number regarding changes.
Therefore, there may be another number (usually called *patch*), which acts as the minor number regarding changes,
so that a development data version could be `0.3.2` (major `3`, minor `2`).

The program ignores all versioned entries, which are either of a different major version
or of a higher minor version (same major version) than the program's data version.
All unversioned entries can be read and used by all program versions (if not overridden by a matching versioned entry).

For multiple entries with an equivalent key and an equivalent version specification
it is not defined, which entry is taken (but a versioned entry with a compatible version is still preferred).
It could be any of those, but only one (no merging applied).

#### Upgrading

##### Major version change

Upgrading to a higher major version without interrupting service works as follows
(in this example from *old* version `1.1` to *new* version `2`):

1. Ensure that all servers have the *old* (current) version<br>
(that should be an invariant of any upgrading process anyway).
2. Determine which entries must be rewritten (updated), added or deleted.
3. For each entry-to-be-rewritten (in example `com/example/SOA`):
    1. add a new versioned entry of same key with the *old* content and the *old* version:<br>
    `com/example/SOA@1.1` → `<old content>`<br>
    (this entry is instantly preferred by the current servers, so be sure to get the content right)
    2. rewrite the unversioned entry with the *new* content<br>
    `com/example/SOA` → `<new content>`<br>
    (the unversioned entry is ignored yet because all servers currently prefer the versioned entry)
4. For each entry-to-be-added (in example `com/example/NEW`):
    1. add a new versioned entry with that key, the *new* content and the *new* version:<br>
    `com/example/NEW@2` → `<new content>`<br>
    (this entry is ignored yet, because the version is unsupported by the current servers)
5. For each entry-to-be-removed (in example `com/example/OLD`):
    1. add a new versioned entry of same key with the *old* content and the *old* version:<br>
    `com/example/OLD@1.1` → `<old content>`<br>
    (this entry is instantly preferred by the current servers, so be sure to get the content right)
    2. delete the unversioned entry of same key:<br>
    `com/example/OLD` → *deleted*
6. Upgrade all servers (stop, update, restart), one by one, to the *new* version.<br>
*Tip*: Remove one server from public service, upgrade it and test the new entries.
If it works well, restore public service and continue upgrading the other servers.
7. Remove each versioned entry with *old* (now actually old) version:<br>
`com/example/SOA@1.1`, `com/example/OLD@1.1`, … (`*@1.1`)<br>
(unfortunately ETCD does not support suffix requests)
8. For each entry-to-be-added (in example `com/example/NEW`):
    1. Add a new unversioned entry of same key with *new* content:<br>
    `com/example/NEW` → `<new content>`
    2. Remove the versioned entry of same key with *new* version:
    `com/example/NEW@2` → *deleted*
9. Make a break, you're done!

##### Minor version change

Upgrading to a higher minor version (same major version) is a subset of the steps
from above: 1, 2 (only added entries), 4, 6, 8 and 9.

<small>(There should be a migration script or two…)</small>

#### Current version

The current data version is `0.1.1` and is described in this document.

### Defaults and options

There are four levels of defaults and options for each domain level (subdomain):

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
domain values the subdomain values override the parent domain values (the levels).
Also, the QTYPE values override the non-qtype values. At last, the id-only values
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

Defaults/options entries must be (currently only JSON) objects, with any number of fields (including zero).
Defaults/options entries may be non-existent, which is equivalent to an empty object.

Field names of defaults objects are the same as record field names. That means there could
be an ambiguity in non-QTYPE defaults, if different record types define the same
field name. The program only checks for the types of field values, not their content,
so take care yourself.<br>
An example: the `ip` field from `A` is not compatible to the `ip` field from `AAAA`.

Field names of defaults and options objects are case-sensitive.

## Supported records

For each of the supported record types the entry values may be objects.
The recognized specific field names and syntax are given below for each entry.

All entries can have a `ttl` field, for the record TTL. There must be a TTL value for each record (easy to set as a global default).

### Syntax

*Headings denote the logical type, top level list values the technical type, sublevels are notes and examples.*

*Field names (keys) are always case-sensitive (in the record itself, in defaults and in options).
For example in `A` or `AAAA` only `ip` is recognized and used, not `Ip`, `IP` or `iP`.*

###### "domain name"
* string
  * `"www"`
  * `"www.example.net."`

Domain names undergo a check whether to append the zone name.
The rule is the same as in [BIND][] zone files: if a name ends with a dot, the zone
name is not appended, otherwise it is. This is only possible for object-entries.

[bind]: https://www.isc.org/downloads/bind/

###### "duration"
* number
  * seconds, only integral part taken
  * `3600`
* string
  * [duration](https://pkg.go.dev/time#ParseDuration) (go.time syntax)
  * `"1h"`

Values must be positive (that is >= 1 second).

###### "IPv4 address"
* string
  * full (all 4 octets given)
    * `"192.168.1.2"`
    * `"::ffff:192.168.1.2"`
    * `"::ffff:c0a8:0102"` (syntactically this is an IPv6, but technically an IPv4)
    * `"c0a80102"` (hexadecimal)
  * partial (1 - 3 octets)
    * valid only if used as prefix (value for option `ip-prefix`) or as suffix when option `ip-prefix` is set
    * one octet only
      * auto-decimal if 1-3 decimal digits (0-9)
      * `"1"`
      * `"12"`
      * `"123"`
      * ~~`"345"`~~ (this results in an error: octet value out of range)
      * auto-hexadecimal if it contains hexadecimal-only digits (A-F)
      * `"d"`
      * `"2a"`
      * hexadecimal may be forced
        * `"0x12"`
    * more octets
      * only decimals
      * `"3.4"`
      * `"20.30.40"`
  * with leading or trailing `.` (1 - 3 octets)
    * if leading, then only for IP values (and the option `ip-prefix` must be set, otherwise not enough octets)
    * if trailing, then only for prefix IPs (in the option `ip-prefix`)
    * `"1."`
    * `".3.4"`
  * without `.` (1 - 4 octets)
    * auto-hexadecimal if it contains hexadecimal-only digits (A-F) or is at least 4 characters long
      * three digits in (auto-)hexadecimal mode are taken already as two octets (left-padded zero)
    * `"c0a80102"`
    * `"1"`
    * `"abc"`
    * `"0345"`
    * eligible as prefix IP and as IP value
  * case-insensitive
* array of bytes or numeric strings, only octets (uint8)
  * length 1 - 4
  * `[3, 4]`
  * number strings with optional base specification (e.g. "0x")
  * `[192, "0xa8", 1, "2"]`
  * eligible as prefix IP and as IP value
* number
  * is taken as exactly one octet
  * `2`
  * eligible as prefix IP and as IP value

###### "IPv6 address"
* string
  * full (all 16 octets given)
    * `"2001:db8:abcd::20"`
    * `"::1"`
    * `"1:2:3:4:5:6:7:8"`
  * partial (1 - 15 octets)
    * valid only if used as prefix (value for option `ip-prefix`) or as suffix when option `ip-prefix` is set
    * always hexadecimal
    * values are left padded with zero when too short
    * `"2"` (1 octet)
    * `"99"` (1 octet, 0x99)
    * `"cafe"` (2 octets)
    * with leading or trailing `:`
      * if leading, then only for IP values (and the option `ip-prefix` must be set, otherwise not enough octets)
      * if trailing, then only for prefix IPs (in the option `ip-prefix`)
      * `"1"` (1 octet: 0x01)
      * `"1:"` (2 octets: 0x00, 0x01)
      * `"12"` (1 octet: 0x12)
      * `"12:"` (2 octets: 0x00, 0x12)
      * `"123"` (2 octets: 0x01, 0x23)
      * `"123:"` (2 octets: 0x01, 0x23)
      * `"1234"` (2 octets: 0x12, 0x34)
      * `"1234:"` (2 octets: 0x12, 0x34)
      * padding can be tricky, it occurs on the left for IP values, but on the right for prefix IPs; and it respects a leading/trailing `:` (or a missing one)
      * `":1"` (2 octets: 0x00, 0x01)
      * `"1:"` (2 octets: 0x00, 0x01)
      * `"1:2"`
        * as IP value (left-padded): 3 octets: 0x01, 0x00, 0x02
        * as prefix IP (right-padded): 3 octets: 0x00, 0x01, 0x20
      * `":1:2"` (only as IP value: 4 octets: 0x00, 0x01, 0x00, 0x02)
      * `"1:2:"` (only as prefix IP: 4 octets: 0x00, 0x01, 0x00, 0x02)
  * without `:` (full or partial), hexadecimal octets
    * `"20010db8000000000000000000000020"` (16 octets)
    * `"030004"` (3 octets)
    * eligible as prefix IP and as IP value
  * case-insensitive
* array of numbers or numeric strings, only octets (uint8)
  * length 1 - 16
  * `[32, "1", "0xd", "0xb8", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, "040"]`
  * `[3, 4]`
  * eligible as prefix IP and as IP value
* number
  * is taken as exactly one octet
  * `2`
  * eligible as prefix IP and as IP value

###### "uint16"
* number
    * only integral part is taken
    * range: 0 - 65535
    * `42`
    * `8080`

###### "string"
* string
    * taken as-is
    * `"anything"`

### QTYPEs

#### `SOA`
* `primary`: domain name
* `mail`: an e-mail address, in regular syntax (`mail@example.net.`), but the domain name undergoes the zone append check,
as described in syntax for "domain name"! It also can be only the local part (without `@<domain>`), then the zone domain name is appended.
* `refresh`: duration
* `retry`: duration
* `expire`: duration
* `neg-ttl`: duration

There is no serial field, because the program takes the latest modification revision of the zone as serial.
This way the operator does not have to increase it manually each time he/she changes DNS data.

Options:
* `no-aa` or `not-authoritative`: boolean
    * don't set the AA-bit for this zone, when set to true
    * __NOT YET IMPLEMENTED__
* `zone-append-domain`: domain name
    * when performing zone append checks, take this value (domain) instead of the FQDN of the current zone
    * undergoes itself a zone append check with the parent zone (if not ending with a `.`)
    * this option can be applied to any QTYPE with a domain name in its value, but is mostly useful here
        * currently `SOA`, `NS`, `PTR`, `CNAME`, `DNAME`, `MX` and `SRV`

#### `NS`
* `hostname`: domain name

Options:
* `zone-append-domain`: domain name
  * [see `SOA`](#soa) for description

#### `A`
* `ip`: IPv4 address
  * the value octets

Options:
* `ip-prefix`: IPv4 address
  * prefix octets are used in the front, value octets are used at the back, middle is padded with zero-valued octets up to the total length of 4 octets (prefix + middle + value)
  * if there are "too many" value octets, they override the prefix octets
    * example: if `ip-prefix` is `"192.168.1."`, `ip` is `"2.4"`, the resulting IP address is `192.168.2.4`

#### `AAAA`
* `ip`: IPv6 address
  * the value octets

Options:
* `ip-prefix`: IPv6 address
  * prefix octets are used in the front, value octets are used at the back, middle is padded with zero-valued octets up to the total length of 16 octets (prefix + middle + value)
  * if there are "too many" value octets, they override the prefix octets
    * example: if `ip-prefix` is `"2001:db8:a:b:1:2:"`, `ip` is `":5:6:7:8"`, the resulting IP address is `2001:db8:a:b:5:6:7:8`

#### `PTR`
* `hostname`: domain name

Options:
* `zone-append-domain`: domain name
  * [see `SOA`](#soa) for description

#### `CNAME`
* `target`: domain name

Options:
* `zone-append-domain`: domain name
  * [see `SOA`](#soa) for description

#### `DNAME`
* `target`: domain name

Options:
* `zone-append-domain`: domain name
  * [see `SOA`](#soa) for description

#### `MX`
* `priority`: uint16
* `target`: domain name

Options:
* `zone-append-domain`: domain name
  * [see `SOA`](#soa) for description

#### `SRV`
* `priority`: uint16
* `weight`: uint16
* `port`: uint16
* `target`: domain name

Options:
* `zone-append-domain`: domain name
  * [see `SOA`](#soa) for description

#### `TXT`
* `text`: string

## Changelog

The changelog lists every change which led to a data version increase (major or minor).
One can use it to check their data - whether an adjustment is needed for a new program version which has a new data version.

### 0.1.1
* added options (keyword `-options-`)
* added option `ip-prefix` to `A` and `AAAA`
* added option `zone-append-domain` to every supported record with a domain name (`SOA`, `NS`, `PTR`, `CNAME`, `DNAME`, `MX`, `SRV`)
* reworked parsing of IPs, added more possibilities for the value
* added last-value syntax
* domains can now be separated by `.` and/or `/` (also intermixed)

### 0.1.0
Initial version (base).

## Full example

*To be precise on the value, it is always enclosed in '...' (single quotes); they are not part of the value themselves.*

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
DNS/net.example/SOA → '{"primary": "ns1", "mail": "horst.master"}'
DNS/net.example/NS#first → '{"hostname": "ns1"}'
DNS/net.example/NS#second → '="ns2"'
DNS/net.example/-options-/A → '{"ip-prefix": [192, 0, 2]}'
DNS/net.example/-options-/AAAA → '{"ip-prefix": "20010db8"}'
DNS/net.example/ns1/A → '=2'
DNS/net.example/ns1/AAAA → '="02"'
DNS/net.example/ns2/A → '{"ip": "192.0.2.3"}'
DNS/net.example/ns2/AAAA → '{"ip": [3]}'
DNS/net.example/-defaults-/MX → '{"ttl": "2h"}'
DNS/net.example/MX#1 → '{"priority": 10, "target": "mail"}'
DNS/net.example/mail/A → '{"ip": [192,0,2,10]}'
DNS/net.example/mail/AAAA → '2001:db8::10'
DNS/net.example/TXT#spf → 'v=spf1 ip4:192.0.2.0/24 ip6:2001:db8::/32 -all'
DNS/net.example/TXT#{} → '{"text":"{text which begins with a curly brace (the id too)}"}'
DNS/net.example/kerberos1/A#1 → '192.0.2.15'
DNS/net.example/kerberos1/AAAA#1 → '2001:db8::15'
DNS/net.example/kerberos2/A# → '192.0.2.25'
DNS/net.example/kerberos2/AAAA# → '2001:db8::25'
DNS/net.example/_tcp/_kerberos/-defaults-/SRV → '{"port": 88}'
DNS/net.example/_tcp/_kerberos/SRV#1 → '{"target": "kerberos1"}'
DNS/net.example/_tcp/_kerberos/SRV#2 → '="kerberos2"'
DNS/net.example/kerberos-master/CNAME → '{"target": "kerberos1"}'
DNS/net.example/mail/HINFO → '"amd64" "Linux"'
DNS/net.example/mail/-defaults-/HINFO → '{"ttl": "2h"}'
DNS/net.example/TYPE123 → '\# 0'
DNS/net.example/TYPE237 → '\# 1 2a'
```

Reverse zone for `192.0.2.0/24`:
```
DNS/arpa.in-addr/192.0.2/-options- → '{"zone-append-domain": "example.net."}'
DNS/arpa.in-addr/192.0.2/SOA → '{"primary": "ns1", "mail": "horst.master"}'
DNS/arpa.in-addr/192.0.2/NS#a → '{"hostname": "ns1"}'
DNS/arpa.in-addr/192.0.2/NS#b → 'ns2.example.net.'
DNS/arpa.in-addr/192.0.2/2/PTR → '="ns1"'
DNS/arpa.in-addr/192.0.2/3/PTR → '="ns2"'
DNS/arpa.in-addr/192.0.2/10/PTR → '="mail"'
DNS/arpa.in-addr/192.0.2/15/PTR → '="kerberos1"'
DNS/arpa.in-addr/192.0.2/25/PTR → '="kerberos2"'
```

Reverse zone for `2001:db8::/32`:
```
DNS/arpa.ip6/2.0.0.1.0.d.b.8/SOA → '{"primary":"ns1.example.net.", "mail":"horst.master@example.net."}'
DNS/arpa.ip6/2.0.0.1.0.d.b.8/NS#1 → 'ns1.example.net.'
DNS/arpa.ip6/2.0.0.1.0.d.b.8/NS#2 → 'ns2.example.net.'
DNS/arpa.ip6/2.0.0.1.0.d.b.8/0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/0.0.0.2/PTR → 'ns1.example.net.'
DNS/arpa.ip6/2.0.0.1.0.d.b.8/0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/0.0.0.3/PTR → 'ns2.example.net.'
DNS/arpa.ip6/2.0.0.1.0.d.b.8/0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/0.0.1.0/PTR → 'mail.example.net.'
DNS/arpa.ip6/2.0.0.1.0.d.b.8/0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/0.0.1.5/PTR → 'kerberos1.example.net.'
DNS/arpa.ip6/2.0.0.1.0.d.b.8/0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/0.0.2.5/PTR → 'kerberos2.example.net.'
```

Delegation and glue records for `subunit.example.net.`:
```
DNS/net.example/subunit/NS#1 → '{"hostname": "ns1.subunit"}'
DNS/net.example/subunit/NS#2 → '="ns2.subunit"'
DNS/net.example/subunit/ns1/A → '192.0.3.2'
DNS/net.example/subunit/ns2/A → '=[3, 3]'
```
