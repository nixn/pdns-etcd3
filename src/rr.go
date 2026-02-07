/* Copyright 2016-2026 nix <https://keybase.io/nixn>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. */

package src

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

type rrParams struct {
	// TODO refactor into single value, too (combine values and lastFieldValue)
	values         objectType[any]
	lastFieldValue *any
	qtype          string
	id             string
	version        *VersionType // TODO remove? not really needed, only used in logging...
	data           *dataNode
	ttl            time.Duration
	//logger         *logrus.Logger // TODO remove?
}

func (p *rrParams) Target() string {
	return targetString(p.data.getQname(), p.qtype, p.id)
}

func (p *rrParams) SetContent(content string, priority *uint16) {
	// p.data.records was set in dataNode.processValues(), no need to check it here
	if _, ok := p.data.records[p.qtype]; !ok {
		p.data.records[p.qtype] = map[string]recordType{}
	}
	p.data.records[p.qtype][p.id] = recordType{content, priority, p.ttl, p.version}
	str := fmt.Sprintf("stored record content: %q", content)
	if priority != nil {
		str += fmt.Sprintf(" !%d", *priority)
	}
	if p.version != nil {
		str += fmt.Sprintf(" @%s", p.version)
	}
	str += fmt.Sprintf(" (%s)", p.ttl)
	p.log().Trace(str)
}

func (p *rrParams) log(args ...any) *logrus.Entry {
	return p.data.log(append([]any{"target", p.Target(), "version", p.version, "ttl", p.ttl}, args...)...)
}

func (p *rrParams) exlog(args ...any) *logrus.Entry {
	return p.log(args...).WithField("lastFieldValue?", p.lastFieldValue != nil)
}

type rrFunc func(params *rrParams)

var rr2func = map[string]rrFunc{
	"A":     a,
	"AAAA":  aaaa,
	"CNAME": domainName("target"),
	"DNAME": domainName("name"),
	"MX":    mx,
	"NS":    domainName("hostname"),
	"PTR":   domainName("hostname"),
	"SOA":   soa,
	"SRV":   srv,
	"TXT":   txt,
}

func fqdn(domain string, params *rrParams) (string, error) {
	qSOA := params.qtype == "SOA"
	for data := params.data; !strings.HasSuffix(domain, "."); data = data.parent {
		zoneAppendDomain, valuePath, err := findValue[string](zoneAppendDomainOption, params.qtype, params.id, data, optionsEntry, true)
		if err != nil {
			return domain, fmt.Errorf("failed to get option %q (dn=%s, vp=%s): %s", zoneAppendDomain, data.getQname(), valuePath, err)
		}
		if valuePath != nil {
			zoneAppendDomain = strings.TrimSpace(zoneAppendDomain)
			if zoneAppendDomain[0] != '.' {
				domain += "."
			}
			domain += zoneAppendDomain
		}
		if !strings.HasSuffix(domain, ".") && (qSOA || data.hasSOA()) {
			if !data.isRoot() {
				domain += "."
			}
			domain += data.getQname()
			break
		}
		if data.parent == nil {
			return domain, fmt.Errorf("unfinished appending of zone domain (currently %q)", domain)
		}
	}
	return domain, nil
}

func getValue[T any](key string, params *rrParams) (T, *valuePath, error) {
	value, vPath, err := findValueOrDefault[T](key, params.values, params.qtype, params.id, params.data)
	if err != nil {
		return value, vPath, fmt.Errorf("failed to get value %s.%s (or default): %s", params.Target(), key, err)
	}
	qPath := valuePath{params.data, &searchOrderElement{params.qtype, params.id}}
	if vPath == nil {
		if params.lastFieldValue != nil {
			if lastFieldValue, ok := (*params.lastFieldValue).(T); ok {
				params.values[key] = lastFieldValue
				log.data("value", lastFieldValue).Tracef("using last-field-value for %s:%s", params.Target(), key)
				params.lastFieldValue = nil
				return lastFieldValue, &qPath, nil
			}
			return value, &qPath, fmt.Errorf("invalid value type: %T", *params.lastFieldValue)
		}
		return value, nil, nil
	}
	return value, &qPath, nil
}

func asNumber[T interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64
}](value any, min, max int64) (T, error) {
	var val int64
	switch value := value.(type) {
	case float64:
		if v, err := float2int(value); err != nil {
			return 0, fmt.Errorf("failed to convert float (%v) to int: %s", value, err)
		} else {
			val = v
		}
	case int:
		val = int64(value)
	default:
		return 0, fmt.Errorf("not a float64 or int: %T", value)
	}
	if val < min {
		return 0, fmt.Errorf("out of range: < %d", min)
	} else if max > min && val > max {
		return 0, fmt.Errorf("out of range: > %d", max)
	}
	return T(val), nil
}

func getUint16(key string, params *rrParams) (uint16, *valuePath, error) {
	value, vPath, err := getValue[any](key, params)
	if err != nil {
		return 0, vPath, fmt.Errorf("failed to get %s.%s: %s", params.Target(), key, err)
	}
	if vPath == nil {
		return 0, nil, nil
	}
	if value, err := asNumber[uint16](value, 0, 65535); err != nil {
		return 0, vPath, err
	} else {
		return value, vPath, nil
	}
}

func getDuration(key string, params *rrParams) (time.Duration, *valuePath, error) {
	value, vPath, err := getValue[any](key, params)
	if err != nil {
		return 0, vPath, fmt.Errorf("failed to get %s.%s: %s", params.Target(), key, err)
	}
	if vPath == nil {
		return 0, nil, nil
	}
	var dur time.Duration
	switch value := value.(type) {
	case float64, int:
		if value, err := asNumber[int64](value, 1, -1); err != nil {
			return 0, vPath, err
		} else {
			dur = time.Duration(value) * time.Second
		}
	case string:
		if v, err := time.ParseDuration(value); err == nil {
			dur = v
		} else {
			return 0, vPath, fmt.Errorf("parse error: %s", err)
		}
	default:
		return 0, vPath, fmt.Errorf("invalid value type (not a float64, int or string): %T", value)
	}
	if dur < time.Second {
		return 0, vPath, fmt.Errorf("must be >= 1s")
	}
	return dur, vPath, nil
}

func getHostname(key string, params *rrParams) (string, *valuePath, error) {
	hostname, vPath, err := getValue[string](key, params)
	if vPath == nil || err != nil {
		return "", vPath, fmt.Errorf("failed to get %s.%s as string: vp=%s, err=%s", params.Target(), key, *ptr2strS(vPath), err)
	}
	hostname = strings.TrimSpace(hostname)
	hostname, err = fqdn(hostname, params)
	if err != nil {
		return "", vPath, fmt.Errorf("failed to append zone domain to %s.%s: %s", params.Target(), key, err)
	}
	return hostname, vPath, nil
}

func domainName(key string) rrFunc {
	return func(params *rrParams) {
		name, vPath, err := getHostname(key, params)
		if vPath == nil || err != nil {
			params.exlog("vp", ptr2strS(vPath), "error", err).Errorf("failed to get %s.%s", params.Target(), key)
			return
		}
		params.SetContent(name, nil)
	}
}

func soa(params *rrParams) {
	// primary
	primary, vPath, err := getValue[string]("primary", params)
	if vPath == nil || err != nil {
		params.exlog("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'primary'")
		return
	}
	primary = strings.TrimSpace(primary)
	primary, err = fqdn(primary, params)
	if err != nil {
		params.exlog("vp", vPath.String(), "error", err).Error("failed to append zone domain to 'primary'")
	}
	// mail
	mail, vPath, err := getValue[string]("mail", params)
	if vPath == nil || err != nil {
		params.exlog("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'mail'")
		return
	}
	mail = strings.TrimSpace(mail)
	atIndex := strings.Index(mail, "@")
	if atIndex < 0 {
		mail = strings.ReplaceAll(mail, ".", "\\.")
	} else {
		localpart := mail[0:atIndex]
		domain := ""
		if atIndex+1 < len(mail) {
			domain = mail[atIndex+1:]
		}
		localpart = strings.ReplaceAll(localpart, ".", "\\.")
		mail = localpart + "." + domain
	}
	mail, err = fqdn(mail, params)
	if err != nil {
		params.exlog("vp", vPath.String(), "error", err).Error("failed to append zone domain to 'mail'")
	}
	// serial
	serial := params.data.zoneRev() // no need for findZone(), because SOA defines the zone
	// refresh
	refresh, vPath, err := getDuration("refresh", params)
	if vPath == nil || err != nil {
		params.exlog("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'refresh'")
		return
	}
	// retry
	retry, vPath, err := getDuration("retry", params)
	if vPath == nil || err != nil {
		params.exlog("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'retry'")
		return
	}
	// expire
	expire, vPath, err := getDuration("expire", params)
	if vPath == nil || err != nil {
		params.exlog("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'expire'")
		return
	}
	// negative ttl
	negativeTTL, vPath, err := getDuration("neg-ttl", params)
	if vPath == nil || err != nil {
		params.exlog("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'neg-ttl'")
		return
	}
	// TODO handle option 'not-authoritative' (alias 'not-aa'?)
	// (done)
	content := fmt.Sprintf("%s %s %d %d %d %d %d", primary, mail, serial, seconds(refresh), seconds(retry), seconds(expire), seconds(negativeTTL))
	params.SetContent(content, nil)
}

func parseOctets(value any, ipVer int, asPrefix bool) ([]byte, error) {
	//goland:noinspection GoPreferNilSlice
	values := []any{}
	sepFirst := false
	sepLast := false
	switch value := value.(type) {
	case float64, int:
		values = append(values, value)
	case string:
		if value == "" {
			return nil, fmt.Errorf("invalid value: empty string")
		}
		if match := ipHexRE.FindStringSubmatch(value); match != nil {
			if ipVer == 4 && match[1] == "" && ip4OctetRE.MatchString(match[2]) {
				values = append(values, value)
				break
			}
			value = match[2]
			ls := len(value)
			if ls%2 == 1 {
				value = "0" + value
				ls++
			}
			for i := 0; i < ls; i += 2 {
				b, _ := strconv.ParseUint(value[i:i+2], 16, 8)
				values = append(values, byte(b))
			}
			break
		}
		sep := ipMeta[ipVer].separator
		sepFirstIndex := strings.Index(value, sep)
		doubleColonIndex := strings.Index(value, "::")
		sepFirst = sepFirstIndex == 0 && (ipVer != 6 || doubleColonIndex != 0)
		ls := len(value)
		sepLast = strings.LastIndex(value, sep) == ls-1 && (ipVer != 6 || ls < 2 || /*(*)*/ doubleColonIndex != ls-2)
		// (*) if there are multiple double colons and the last one is at the end, sepLast should be false but would be true
		// this is not a problem though, because net.ParseIP() would be still called, which would fail then, leading to returning an error
		if sepFirst {
			if asPrefix {
				return nil, fmt.Errorf("can't have a separator first in a prefix IP")
			}
			value = "0" + value
		}
		if sepLast {
			if !asPrefix {
				return nil, fmt.Errorf("can't have a separator last in an IP value")
			}
			value += "0"
		}
		if doubleColonIndex >= 0 || strings.Contains(value, ":") {
			ip := net.ParseIP(value)
			if ip != nil {
				switch ipVer {
				case 4:
					ip = ip.To4()
				case 6:
					ip = ip.To16()
				default:
					ip = nil
				}
			}
			if ip != nil {
				for _, octet := range ip {
					values = append(values, octet)
				}
				break
			}
			if ipVer != 6 || doubleColonIndex >= 0 {
				return nil, fmt.Errorf("failed to parse as an IPv%d address", ipVer)
			}
			parts := strings.Split(value, sep)
			for i, n := 0, len(parts); i < n; i++ {
				part := parts[i]
				doubleOctet, err := strconv.ParseUint(part, 16, 16)
				if err != nil {
					iDisplay := i
					if sepFirst {
						iDisplay--
					}
					return nil, fmt.Errorf("double octet #%d (%v): failed to parse as uint16: %s", iDisplay, part, err)
				}
				hi, lo := func() (bool, bool) {
					lp := len(part)
					if asPrefix {
						if i+1 < n {
							return true, true
						}
						for i := 4; i > lp; i-- {
							doubleOctet <<= 4
						}
						return true, sepLast || lp > 2
					}
					// else
					if i > 0 {
						return true, true
					}
					return sepFirst || lp > 2, true
				}()
				if hi {
					values = append(values, byte(doubleOctet>>8))
				}
				if lo {
					values = append(values, byte(doubleOctet&0xff))
				}
			}
		} else if ipVer == 4 && sepFirstIndex >= 0 {
			for _, octet := range strings.Split(value, sep) {
				values = append(values, octet)
			}
		} else {
			return nil, fmt.Errorf("invalid syntax")
		}
	case []any:
		values = value
	default:
		return nil, fmt.Errorf("invalid value type: %T", value)
	}
	if lv := len(values); lv == 0 || lv > ipMeta[ipVer].totalOctets {
		return nil, fmt.Errorf("invalid count of octets (found %d, need 1 - %d)", lv, ipMeta[ipVer].totalOctets)
	}
	if sepFirst {
		values = values[ipMeta[ipVer].partOctets:]
	}
	if sepLast {
		values = values[:len(values)-ipMeta[ipVer].partOctets]
	}
	octets := []byte{}
	for i, v := range values {
		switch v := v.(type) {
		case byte:
			octets = append(octets, v)
		case int:
			if b, err := asNumber[byte](v, 0, 255); err != nil {
				return nil, fmt.Errorf("octet #%d (%v): %v", i, v, err)
			} else {
				octets = append(octets, b)
			}
		case float64:
			vI, err := float2int(v)
			if err != nil {
				return nil, fmt.Errorf("octet #%d (%v): failed to convert from float to int: %s", i, v, err)
			}
			if vI < 0 || v > 255 {
				return nil, fmt.Errorf("octet #%d (%v): value out of range (0-255)", i, vI)
			}
			octets = append(octets, byte(vI))
		case string:
			if v == "" {
				return nil, fmt.Errorf("octet #%d: empty string", i)
			}
			vB, err := strconv.ParseUint(v, 0, 8)
			if err != nil {
				return nil, fmt.Errorf("octet #%d(%v): failed to parse from string: %s", i, v, err)
			}
			// value range is already checked by ParseUint() above (bitSize argument)
			octets = append(octets, byte(vB))
		default:
			return nil, fmt.Errorf("octet #%d: invalid type: %T", i, v)
		}
	}
	return octets, nil
}

func ipRR(params *rrParams, ipVer int) {
	value, vPath, err := getValue[any]("ip", params)
	if vPath == nil || err != nil {
		params.exlog("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'ip'")
		return
	}
	var prefix []byte
	prefixAny, oPath, err := findValue[any](ipPrefixOption, params.qtype, params.id, params.data, optionsEntry, false)
	if err != nil {
		params.exlog("vp", vPath.String(), "error", err).Errorf("failed to get option %q", ipPrefixOption)
		return
	}
	if oPath != nil {
		octets, err := parseOctets(prefixAny, ipVer, true)
		if err != nil {
			params.log("field", "ip", "option", ipPrefixOption).Errorf("failed to parse octets: %s", err)
			return
		}
		prefix = octets
		params.log("field", "ip", "option", ipPrefixOption, "value", prefix).Trace("option value")
	} else {
		params.log("field", "ip").Tracef("option %q not found", ipPrefixOption)
	}
	octets, err := parseOctets(value, ipVer, false)
	if err != nil {
		params.exlog("field", "ip", "value", value).Errorf("failed to parse value to octets: %s", err)
		return
	}
	vLen := len(octets)
	pLen := len(prefix)
	if pLen == 0 && vLen < ipMeta[ipVer].totalOctets {
		params.exlog("field", "ip", "value", octets).Errorf("too few octets")
		return
	}
	ip := net.IP(prefix)
	for i := pLen; i < ipMeta[ipVer].totalOctets; i++ {
		ip = append(ip, 0)
	}
	offset := ipMeta[ipVer].totalOctets - vLen
	for i, octet := range octets {
		ip[offset+i] = octet
	}
	content := ip.String()
	params.SetContent(content, nil)
	// TODO handle option 'auto-ptr': save the (hostname, ip) pair for later processing, b/c here the reverse zone could be not present yet (later it also could be not present, need to deal with it somehow)
}

func a(params *rrParams) {
	ipRR(params, 4)
}

func aaaa(params *rrParams) {
	ipRR(params, 6)
}

func srv(params *rrParams) {
	priority, vPath, err := getUint16("priority", params)
	if vPath == nil || err != nil {
		params.log("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'priority'")
		return
	}
	weight, vPath, err := getUint16("weight", params)
	if vPath == nil || err != nil {
		params.log("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'weight'")
		return
	}
	port, vPath, err := getUint16("port", params)
	if vPath == nil || err != nil {
		params.log("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'port'")
		return
	}
	target, vPath, err := getHostname("target", params)
	if vPath == nil || err != nil {
		params.log("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'target'")
		return
	}
	content := fmt.Sprintf("{priority:%%d }%d %d %s", weight, port, target)
	params.SetContent(content, &priority)
}

func mx(params *rrParams) {
	priority, vPath, err := getUint16("priority", params)
	if vPath == nil || err != nil {
		params.exlog("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'priority'")
		return
	}
	target, vPath, err := getHostname("target", params)
	if vPath == nil || err != nil {
		params.log("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'target'")
		return
	}
	content := fmt.Sprintf("{priority:%%d }%s", target)
	params.SetContent(content, &priority)
}

func txt(params *rrParams) {
	text, vPath, err := getValue[any]("text", params)
	if vPath == nil || err != nil {
		params.log("vp", ptr2strS(vPath), "error", err).Error("failed to get value for 'text'")
		return
	}
	var ts []any
	switch t := text.(type) {
	case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		ts = []any{t}
	case []any:
		ts = t
	default:
		params.log("vp", vPath.String(), "type", fmt.Sprintf("%T", text)).Error("invalid type for 'text' (expected a string or number or an array thereof)")
		return
	}
	if len(ts) == 0 {
		params.log("vp", vPath.String()).Error("empty array for 'text'")
		return
	}
	content := ""
	for i, t := range ts {
		if i > 0 {
			content += " "
		}
		switch t := t.(type) {
		case string:
			content += fmt.Sprintf("%q", t)
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			content += fmt.Sprintf(`"%d"`, t)
		case float32:
			content += fmt.Sprintf("%q", float2decimal(float64(t)))
		case float64:
			content += fmt.Sprintf("%q", float2decimal(t))
		default:
			params.log("vp", vPath.String(), "type", fmt.Sprintf("%T", t)).Errorf("invalid type for 'text' element at index %d (expected a string or number)", i)
			return
		}
	}
	params.SetContent(content, nil)
}
