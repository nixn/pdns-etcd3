# ETCD structure

## Stability

The structure is currently in development and may change multiple times in an incompatible
way until a stable major release. After such a release the structure won't change
for the same major release (but may for future major releases).

## Rules

* Record entry values are either JSON objects or plain strings (that is without
quotation marks). If an entry value begins with a `{`, it is parsed as a JSON object,
otherwise it is taken as plain string.

* Each record which has a JSON entry value must be supported by the program.
Otherwise an error is emitted and the request/response fails. This is not true for plain strings,
which are returned as-is, without an error, but also without defaults support (except TTL).
This behaviour allows support for JSON-unsupported records.

* Entry values store the *content* of a record, they do not include the domain name,
the DNS class (`IN`) and the record type (`A`, `MX`, …), these values are
in the key already. They may include a record-specific TTL value, see below rule for details.

* The record TTL is a regular field in case of a JSON object entry (key `"ttl"`), but there
is (currently) no way to define a record-specific TTL for a plain string entry.
One may use a default value as a workaround for this limitation.

* For each record field a default value is searched for and used, if an entry value
does not specify the field value itself. If no value is found for the field,
an error is raised and the request/response fails.

* Subdomains are determined by the domain name in question (QNAME) minus the zone name
(and the separating dot). E.g. QNAME `some.thing.example.net` in zone `example.net`
yields the subdomain `some.thing`.
If the QNAME is equal to the zone name, the subdomain is set to `@` for ETCD requests.

## Structure (Entries)

`<prefix>` is the global prefix from configuration (see [README](README.md)).

### Version

* Key: `<prefix>/version`
* Value: `<major>[.<minor>]`
  * `<major>` and `<minor>` must be non-negative integers

`<major>` begins with `1` (exception: pre-1.0 development, see below).
Every time when a backward-incompatible change to the
structure is introduced, `<major>` increases and `<minor>` resets to `0`.
Otherwise a change (which should be only additions) increases only `<minor>`.

During the development of first stable release (`1` or `1.0`) the `<major>` number
is `0`, and the minor number starts with `1` and acts as the major number regarding
changes. Therefore there may be another minor number (usually called *patch*),
so that a development data version could be `0.3.2`.

Version compatibility is as follows:
* The program's version major number must be equal to the data version major number.
  * Exception: Program version `1.0` (or `1.0.*`) supports data version `1.0` *and* `0.y.*`,
  the last pre-1.0-development version (`y` is undetermined yet).
* The program's version minor number must be equal to or greater than the data version minor number. Otherwise the program refuses to work.

Version checking is not implemented yet.

The version described here is `0.1`.

### Records

Each resource record has at least one corresponding entry in the storage.
Entries are as follows:

* Key: `<prefix>/<zone>/<subdomain>/<QTYPE>/<id>`
  * `<zone>` is a domain name, e.g. `example.net`
  * `<subdomain>` is as described in the rules above
  * `<QTYPE>` is the type of the resource resource, e.g. `A`, `MX`, …
  * `<id>` is user-defined, it has no meaning in the program, it may even be empty
* Value: `<JSON object>` or `<plain string>`

For multiple values of the same record use multiple `<id>`s. All records
but `SOA` may have multiple values.

#### Exceptions

* For the `SOA` record the entry key is `<prefix>/<zone>/@/SOA` (no `<id>`).
It does not have multiple values.

* The QTYPE `ANY` is not a real record, so nothing to store for it.

### Defaults

There are four levels of default values, from most generic to most specific:

1. zone
  * Key: `<prefix>/<zone>/-defaults`
2. zone + QTYPE
  * Key: `<prefix>/<zone>/<QTYPE>-defaults`
3. zone + subdomain
  * Key: `<prefix>/<zone>/<subdomain>/-defaults`
4. zone + subdomain + QTYPE
  * Key: `<prefix>/<zone>/<subdomain>/<QTYPE>-defaults`

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

*Headings denote the logical type, top level list values the JSON type, sublevels are examples.*

###### "domain name"
* string
  * `"www"`
  * `"www.example.net."`

Domain names undergo a check whether to append the zone name.
The rule is the same as in [BIND][] zone files: if a name ends with a dot, the zone
name is not appended, otherwise it is. This is naturally only possible for JSON-entries.

[bind]: https://www.isc.org/downloads/bind/

###### "duration"
* number (seconds, only integral part taken)
  * `3600`
* string ([duration][tdur])
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

## Example

*To be clear on the value, it's always enclosed in ' (single quotes).*

Version:
```
/DNS/version ⇒ '0.1'
```

Forward zone:
```
/DNS/example.net/-defaults ⇒ '{"ttl": "1h"}'
/DNS/example.net/@/SOA ⇒ '{"primary": "ns1", "mail": "horst.master", "refresh": "1h", "retry": "30m", "expire": 604800, "neg-ttl": "10m"}'
/DNS/example.net/@/NS/first ⇒ '{"hostname": "ns1"}'
/DNS/example.net/@/NS/second ⇒ '{"hostname": "ns2"}'
/DNS/example.net/ns1/A/1 ⇒ '{"ip": [192, 0, 2, 2]}'
/DNS/example.net/ns1/AAAA/1 ⇒ '{"ip": "2001:db8::2"}'
/DNS/example.net/ns2/A/1 ⇒ '{"ip": "192.0.2.3"}'
/DNS/example.net/ns2/AAAA/1 ⇒ '{"ip": "2001:db8::3"}'
/DNS/example.net/@/MX-defaults ⇒ '{"ttl": "2h"}'
/DNS/example.net/@/MX/1 ⇒ '10 mail.example.net.'
/DNS/example.net/mail/A/1 ⇒ '{"ip": [192,0,2,10]}'
/DNS/example.net/mail/AAAA/1 ⇒ '2001:db8::10'
/DNS/example.net/@/TXT/1 ⇒ 'v=spf1 ip4:192.0.2.0/24 ip6:2001:db8::/32 -all'
/DNS/example.net/kerberos1/A/1 ⇒ '192.0.2.15'
/DNS/example.net/kerberos1/AAAA/1 ⇒ '2001:db8::15'
/DNS/example.net/kerberos2/A/1 ⇒ '192.0.2.25'
/DNS/example.net/kerberos2/AAAA/1 ⇒ '2001:db8::25'
/DNS/example.net/SRV-defaults ⇒ '{"priority": 0, "weight": 0}'
/DNS/example.net/_kerberos._tcp/SRV-defaults ⇒ '{"port": 88}'
/DNS/example.net/_kerberos._tcp/SRV/1 ⇒ '0 0 88 kerberos1.example.net.'
/DNS/example.net/_kerberos._tcp/SRV/2 ⇒ '0 0 88 kerberos2.example.net.'
/DNS/example.net/kerberos-master/CNAME/1 ⇒ 'kerberos1.example.net.'
```

Reverse zone for IPv4:
```
/DNS/2.0.192.in-addr.arpa/-defaults ⇒ '{"ttl": "1h"}'
/DNS/2.0.192.in-addr.arpa/@/SOA ⇒ '{"primary": "ns1.example.net.", "mail": "horst.master@example.net.", "refresh": "1h", "retry": "30m", "expire": "168h", "neg-ttl": "10m"}'
/DNS/2.0.192.in-addr.arpa/@/NS/a ⇒ '{"hostname": "ns1.example.net."}'
/DNS/2.0.192.in-addr.arpa/@/NS/b ⇒ 'ns2.example.net.'
/DNS/2.0.192.in-addr.arpa/2/PTR/1 ⇒ '{"hostname": "ns1.example.net."}'
/DNS/2.0.192.in-addr.arpa/3/PTR/1 ⇒ 'ns2.example.net.'
/DNS/2.0.192.in-addr.arpa/10/PTR/1 ⇒ '{"hostname": "mail.example.net."}'
/DNS/2.0.192.in-addr.arpa/15/PTR/1 ⇒ 'kerberos1.example.net.'
/DNS/2.0.192.in-addr.arpa/25/PTR/1 ⇒ 'kerberos2.example.net.'
```

Reverse zone for IPv6:
```
/DNS/8.b.d.0.1.0.0.2.ip6.arpa/-defaults ⇒ '{"ttl": 3600}'
/DNS/8.b.d.0.1.0.0.2.ip6.arpa/@/SOA ⇒ '{"primary":"ns1.example.net.", "mail":"horst.master@example.net.", "refresh":"1h", "retry":"30m", "expire":"168h","neg-ttl":"10m"}'
/DNS/8.b.d.0.1.0.0.2.ip6.arpa/@/NS/1 ⇒ 'ns1.example.net.'
/DNS/8.b.d.0.1.0.0.2.ip6.arpa/@/NS/2 ⇒ 'ns2.example.net.'
/DNS/8.b.d.0.1.0.0.2.ip6.arpa/2.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/PTR/1 ⇒ 'ns1.example.net.'
/DNS/8.b.d.0.1.0.0.2.ip6.arpa/3.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/PTR/1 ⇒ 'ns2.example.net.'
/DNS/8.b.d.0.1.0.0.2.ip6.arpa/0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/PTR/1 ⇒ 'mail.example.net.'
/DNS/8.b.d.0.1.0.0.2.ip6.arpa/5.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/PTR/1 ⇒ 'kerberos1.example.net.'
/DNS/8.b.d.0.1.0.0.2.ip6.arpa/5.2.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/PTR/1 ⇒ 'kerberos2.example.net.'
```

Well ... "glue records":
```
/DNS/ns1.example.net/-defaults ⇒ '{"ttl":"1h"}'
/DNS/ns1.example.net/A/1 ⇒ '192.0.2.2'
/DNS/ns1.example.net/AAAA/1 ⇒ '2001:db8::2'
/DNS/ns2.example.net/-defaults ⇒ '{"ttl":"1h"}'
/DNS/ns2.example.net/A/1 ⇒ '192.0.2.3'
/DNS/ns2.example.net/AAAA/1 ⇒ '2001:db8::3'
```
