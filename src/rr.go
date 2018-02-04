/* Copyright 2016-2018 nix <https://github.com/nixn>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. */

package main

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var rr2func map[string]rrFunc = map[string]rrFunc{
	"A":     a,
	"AAAA":  aaaa,
	"CNAME": domainName("CNAME", "target"),
	"DNAME": domainName("DNAME", "name"),
	"MX":    mx,
	"NS":    domainName("NS", "hostname"),
	"PTR":   domainName("PTR", "hostname"),
	"SOA":   soa,
	"SRV":   srv,
	"TXT":   txt,
}

func fqdn(domain, qname string) string {
	l := len(domain)
	if l == 0 || domain[l-1] != '.' {
		domain += "." + qname
		l = len(domain)
		if domain[l-1] != '.' {
			domain += "."
		}
	}
	return domain
}

func getUint16(name string, values objectType, qtype string, data *dataNode) (uint16, error) {
	if v, err := findValue(name, values, qtype, data); err == nil {
		if v, ok := v.(float64); ok {
			if v < 0 || v > 65535 {
				return 0, fmt.Errorf("'%s' out of range (0-65535)", name)
			}
			return uint16(v), nil
		}
		return 0, fmt.Errorf("'%s' is not a number", name)
	} else {
		return 0, err
	}
}

func getString(name string, values objectType, qtype string, data *dataNode) (string, error) {
	if v, err := findValue(name, values, qtype, data); err == nil {
		if v, ok := v.(string); ok {
			return v, nil
		}
		return "", fmt.Errorf("'%s' is not a string", name)
	} else {
		return "", err
	}
}

func getDuration(name string, values objectType, qtype string, data *dataNode) (time.Duration, error) {
	if v, err := findValue(name, values, qtype, data); err == nil {
		var dur time.Duration
		switch v.(type) {
		case float64:
			dur = time.Duration(int64(v.(float64))) * time.Second
		case string:
			if v, err := time.ParseDuration(v.(string)); err == nil {
				dur = v
			} else {
				return 0, fmt.Errorf("'%s' parse error: %s", name, err)
			}
		default:
			return 0, fmt.Errorf("'%s' is neither a number nor a string", name)
		}
		if dur < time.Second {
			return dur, fmt.Errorf("'%s' must be positive", name)
		}
		return dur, nil
	} else {
		return 0, err
	}
}

func getHostname(name string, values objectType, qtype string, data *dataNode) (string, error) {
	hostname, err := getString(name, values, qtype, data)
	if err != nil {
		return "", err
	}
	hostname = strings.TrimSpace(hostname)
	hostname = fqdn(hostname, data.getZoneNode().getQname())
	return hostname, nil
}

func domainName(qtype, fieldName string) rrFunc {
	return func(values objectType, data *dataNode, revision int64) (string, objectType, error) {
		name, err := getHostname(fieldName, values, qtype, data)
		if err != nil {
			return "", nil, err
		}
		ttl, err := getDuration("ttl", values, qtype, data)
		if err != nil {
			return "", nil, err
		}
		meta := objectType{
			"ttl": ttl,
		}
		return name, meta, nil
	}
}

func soa(values objectType, data *dataNode, revision int64) (string, objectType, error) {
	// primary
	primary, err := getString("primary", values, "SOA", data)
	if err != nil {
		return "", nil, err
	}
	zone := data.getZoneNode().getQname()
	primary = strings.TrimSpace(primary)
	primary = fqdn(primary, zone)
	// mail
	mail, err := getString("mail", values, "SOA", data)
	if err != nil {
		return "", nil, err
	}
	mail = strings.TrimSpace(mail)
	atIndex := strings.Index(mail, "@")
	if atIndex < 0 {
		mail = strings.Replace(mail, ".", "\\.", -1)
	} else {
		localpart := mail[0:atIndex]
		domain := ""
		if atIndex+1 < len(mail) {
			domain = mail[atIndex+1:]
		}
		localpart = strings.Replace(localpart, ".", "\\.", -1)
		mail = localpart + "." + domain
	}
	mail = fqdn(mail, zone)
	// serial
	serial := revision
	// refresh
	refresh, err := getDuration("refresh", values, "SOA", data)
	if err != nil {
		return "", nil, err
	}
	// retry
	retry, err := getDuration("retry", values, "SOA", data)
	if err != nil {
		return "", nil, err
	}
	// expire
	expire, err := getDuration("expire", values, "SOA", data)
	if err != nil {
		return "", nil, err
	}
	// negative ttl
	negativeTTL, err := getDuration("neg-ttl", values, "SOA", data)
	if err != nil {
		return "", nil, err
	}
	// ttl
	ttl, err := getDuration("ttl", values, "SOA", data)
	if err != nil {
		return "", nil, err
	}
	// (done)
	content := fmt.Sprintf("%s %s %d %d %d %d %d", primary, mail, serial, seconds(refresh), seconds(retry), seconds(expire), seconds(negativeTTL))
	meta := objectType{
		"ttl": ttl,
	}
	return content, meta, nil
}

func a(values objectType, data *dataNode, revision int64) (string, objectType, error) {
	var ip net.IP
	v, err := findValue("ip", values, "A", data)
	if err != nil {
		return "", nil, err
	}
	switch v.(type) {
	case string:
		v := v.(string)
		ipv4HexRE := regexp.MustCompile("^([0-9a-fA-F]{2}){4}$")
		if ipv4HexRE.MatchString(v) {
			ip = net.IP{0, 0, 0, 0}
			for i := 0; i < 4; i++ {
				v, err := strconv.ParseUint(v[i*2:i*2+2], 16, 8)
				if err != nil {
					return "", nil, err
				}
				ip[i] = byte(v)
			}
		} else {
			ip = net.ParseIP(v)
			if ip == nil {
				return "", nil, fmt.Errorf("invalid IPv4: failed to parse")
			}
			ip = ip.To4()
			if ip == nil {
				return "", nil, fmt.Errorf("invalid IPv4: parsed, but not as IPv4")
			}
		}
	case []interface{}:
		v := v.([]interface{})
		if len(v) != 4 {
			return "", nil, fmt.Errorf("invalid IPv4: array length not 4")
		}
		ip = net.IP{0, 0, 0, 0}
		for i, v := range v {
			switch v.(type) {
			case float64:
				v := int64(v.(float64))
				if v < 0 || v > 255 {
					return "", nil, fmt.Errorf("invalid IPv4: part %d out of range", i+1)
				}
				ip[i] = byte(v)
			case string:
				v, err := strconv.ParseUint(v.(string), 0, 8)
				if err != nil {
					return "", nil, err
				}
				if v > 255 {
					return "", nil, fmt.Errorf("invalid IPv4: part %d out of range", i+1)
				}
				ip[i] = byte(v)
			default:
				return "", nil, fmt.Errorf("invalid IPv4: part neither number nor string")
			}
		}
	default:
		return "", nil, fmt.Errorf("invalid IPv4: not string or array")
	}
	ttl, err := getDuration("ttl", values, "A", data)
	if err != nil {
		return "", nil, err
	}
	content := ip.String()
	meta := objectType{
		"ttl": ttl,
	}
	return content, meta, nil
}

func aaaa(values objectType, data *dataNode, revision int64) (string, objectType, error) {
	var ip net.IP
	v, err := findValue("ip", values, "AAAA", data)
	if err != nil {
		return "", nil, err
	}
	switch v.(type) {
	case string:
		v := v.(string)
		ipv6HexRE := regexp.MustCompile("^([0-9a-fA-F]{2}){16}$")
		if ipv6HexRE.MatchString(v) {
			ip = net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
			for i := 0; i < 16; i++ {
				v, err := strconv.ParseUint(v[i*2:i*2+2], 16, 8)
				if err != nil {
					return "", nil, err
				}
				ip[i] = byte(v)
			}
		} else {
			ip = net.ParseIP(v)
			if ip == nil {
				return "", nil, fmt.Errorf("invalid IPv6: failed to parse")
			}
			ip = ip.To16()
			if ip == nil {
				return "", nil, fmt.Errorf("invalid IPv6: parsed, but no IPv6")
			}
		}
	case []interface{}:
		v := v.([]interface{})
		var bytesPerPart int
		switch len(v) {
		case 8:
			bytesPerPart = 2
		case 16:
			bytesPerPart = 1
		default:
			return "", nil, fmt.Errorf("invalid IPv6: array length neither 8 nor 16")
		}
		bitSize := bytesPerPart * 8
		maxVal := uint64(1<<uint(bitSize) - 1)
		ip = net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		setPart := func(i int, v uint64) {
			for j := 0; j < bytesPerPart; j++ {
				v := (v >> uint((bytesPerPart-1-j)*8)) & 0xFF
				ip[i*bytesPerPart+j] = byte(v)
			}
		}
		for i, v := range v {
			switch v.(type) {
			case float64:
				if v.(float64) < 0 {
					return "", nil, fmt.Errorf("invalid IPv6: part out of range")
				}
				v := uint64(v.(float64))
				if v > maxVal {
					return "", nil, fmt.Errorf("invalid IPv6: part out of range")
				}
				setPart(i, v)
			case string:
				v, err := strconv.ParseUint(v.(string), 0, bitSize)
				if err != nil {
					return "", nil, fmt.Errorf("invalid IPv6: %s", err)
				}
				if v > maxVal {
					return "", nil, fmt.Errorf("invalid IPv6: part out of range")
				}
				setPart(i, v)
			default:
				return "", nil, fmt.Errorf("invalid IPv6: not string or number")
			}
		}
	default:
		return "", nil, fmt.Errorf("invalid IPv6: not string or array")
	}
	ttl, err := getDuration("ttl", values, "AAAA", data)
	if err != nil {
		return "", nil, err
	}
	content := ip.String()
	meta := objectType{
		"ttl": ttl,
	}
	return content, meta, nil
}

func srv(values objectType, data *dataNode, revision int64) (string, objectType, error) {
	priority, err := getUint16("priority", values, "SRV", data)
	if err != nil {
		return "", nil, err
	}
	weight, err := getUint16("weight", values, "SRV", data)
	if err != nil {
		return "", nil, err
	}
	port, err := getUint16("port", values, "SRV", data)
	if err != nil {
		return "", nil, err
	}
	target, err := getHostname("target", values, "SRV", data)
	if err != nil {
		return "", nil, err
	}
	ttl, err := getDuration("ttl", values, "SRV", data)
	if err != nil {
		return "", nil, err
	}
	format := ""
	params := []interface{}(nil)
	if pdnsVersion == 4 {
		format += "%d "
		params = append(params, priority)
	}
	format += "%d %d %s"
	params = append(params, weight, port, target)
	content := fmt.Sprintf(format, params...)
	meta := objectType{
		"ttl": ttl,
	}
	if pdnsVersion == 3 {
		meta["priority"] = priority
	}
	return content, meta, nil
}

func mx(values objectType, data *dataNode, revision int64) (string, objectType, error) {
	priority, err := getUint16("priority", values, "MX", data)
	if err != nil {
		return "", nil, err
	}
	target, err := getHostname("target", values, "MX", data)
	if err != nil {
		return "", nil, err
	}
	ttl, err := getDuration("ttl", values, "MX", data)
	if err != nil {
		return "", nil, err
	}
	format := ""
	params := []interface{}(nil)
	if pdnsVersion == 4 {
		format += "%d "
		params = append(params, priority)
	}
	format += "%s"
	params = append(params, target)
	content := fmt.Sprintf(format, params...)
	meta := objectType{
		"ttl": ttl,
	}
	if pdnsVersion == 3 {
		meta["priority"] = priority
	}
	return content, meta, nil
}

func txt(values objectType, data *dataNode, revision int64) (string, objectType, error) {
	text, err := getString("text", values, "TXT", data)
	if err != nil {
		return "", nil, err
	}
	ttl, err := getDuration("ttl", values, "MX", data)
	if err != nil {
		return "", nil, err
	}
	content := text
	meta := objectType{
		"ttl": ttl,
	}
	return content, meta, nil
}
