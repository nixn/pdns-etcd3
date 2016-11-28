/* Copyright 2016 nix <https://github.com/nixn>

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

func findValue(name string, obj map[string]interface{}, qp *queryParts) (interface{}, error) {
	if v, ok := obj[name]; ok {
		return v, nil
	}
	if err := ensureDefaults(qp); err != nil {
		return nil, err
	}
	if v, ok := defaults.values[qp.zoneSubdomainQtypeDefaultsKey()][name]; ok {
		return v, nil
	}
	if v, ok := defaults.values[qp.zoneSubdomainDefaultsKey()][name]; ok {
		return v, nil
	}
	if v, ok := defaults.values[qp.zoneQtypeDefaultsKey()][name]; ok {
		return v, nil
	}
	if v, ok := defaults.values[qp.zoneDefaultsKey()][name]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("missing '%s'", name)
}

func getInt32(name string, obj map[string]interface{}, qp *queryParts) (int32, error) {
	if v, err := findValue(name, obj, qp); err == nil {
		if v, ok := v.(float64); ok {
			if v < 0 {
				return 0, fmt.Errorf("'%s' may not be negative", name)
			} else {
				return int32(v), nil
			}
		}
		return 0, fmt.Errorf("'%s' is not a number", name)
	} else {
		return 0, err
	}
}

func getString(name string, obj map[string]interface{}, qp *queryParts) (string, error) {
	if v, err := findValue(name, obj, qp); err == nil {
		if v, ok := v.(string); ok {
			return v, nil
		}
		return "", fmt.Errorf("'%s' is not a string", name)
	} else {
		return "", err
	}
}

func getDuration(name string, obj map[string]interface{}, qp *queryParts) (time.Duration, error) {
	if v, err := findValue(name, obj, qp); err == nil {
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

func soa(obj map[string]interface{}, qp *queryParts, revision int64) (string, time.Duration, error) {
	// primary
	primary, err := getString("primary", obj, qp)
	if err != nil {
		return "", 0, err
	}
	primary = strings.TrimSpace(primary)
	primary = fqdn(primary, qp.zone)
	// mail
	mail, err := getString("mail", obj, qp)
	if err != nil {
		return "", 0, err
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
	mail = fqdn(mail, qp.zone)
	// serial
	serial := revision
	// refresh
	refresh, err := getDuration("refresh", obj, qp)
	if err != nil {
		return "", 0, err
	}
	// retry
	retry, err := getDuration("retry", obj, qp)
	if err != nil {
		return "", 0, err
	}
	// expire
	expire, err := getDuration("expire", obj, qp)
	if err != nil {
		return "", 0, err
	}
	// negative ttl
	negativeTTL, err := getDuration("neg-ttl", obj, qp)
	if err != nil {
		return "", 0, err
	}
	// ttl
	ttl, err := getDuration("ttl", obj, qp)
	if err != nil {
		return "", 0, err
	}
	// (done)
	var content = fmt.Sprintf("%s %s %d %d %d %d %d", primary, mail, serial, seconds(refresh), seconds(retry), seconds(expire), seconds(negativeTTL))
	return content, ttl, nil
}

func ns(obj map[string]interface{}, qp *queryParts) (string, time.Duration, error) {
	hostname, err := getString("hostname", obj, qp)
	if err != nil {
		return "", 0, err
	}
	hostname = strings.TrimSpace(hostname)
	hostname = fqdn(hostname, qp.zone)
	ttl, err := getDuration("ttl", obj, qp)
	if err != nil {
		return "", 0, err
	}
	content := fmt.Sprintf("%s", hostname)
	return content, ttl, nil
}

func a(obj map[string]interface{}, qp *queryParts) (string, time.Duration, error) {
	var ip net.IP
	v, err := findValue("ip", obj, qp)
	if err != nil {
		return "", 0, err
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
					return "", 0, err
				}
				ip[i] = byte(v)
			}
		} else {
			ip = net.ParseIP(v)
			if ip == nil {
				return "", 0, fmt.Errorf("invalid IPv4: failed to parse")
			}
			ip = ip.To4()
			if ip == nil {
				return "", 0, fmt.Errorf("invalid IPv4: parsed, but not as IPv4")
			}
		}
	case []interface{}:
		v := v.([]interface{})
		if len(v) != 4 {
			return "", 0, fmt.Errorf("invalid IPv4: array length not 4")
		}
		ip = net.IP{0, 0, 0, 0}
		for i, v := range v {
			switch v.(type) {
			case float64:
				v := int64(v.(float64))
				if v < 0 || v > 255 {
					return "", 0, fmt.Errorf("invalid IPv4: part %d out of range", i+1)
				}
				ip[i] = byte(v)
			case string:
				v, err := strconv.ParseUint(v.(string), 0, 8)
				if err != nil {
					return "", 0, err
				}
				if v > 255 {
					return "", 0, fmt.Errorf("invalid IPv4: part %d out of range", i+1)
				}
				ip[i] = byte(v)
			default:
				return "", 0, fmt.Errorf("invalid IPv4: part neither number nor string")
			}
		}
	default:
		return "", 0, fmt.Errorf("invalid IPv4: not string or array")
	}
	ttl, err := getDuration("ttl", obj, qp)
	if err != nil {
		return "", 0, err
	}
	content := ip.String()
	return content, ttl, nil
}

func aaaa(obj map[string]interface{}, qp *queryParts) (string, time.Duration, error) {
	var ip net.IP
	v, err := findValue("ip", obj, qp)
	if err != nil {
		return "", 0, err
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
					return "", 0, err
				}
				ip[i] = byte(v)
			}
		} else {
			ip = net.ParseIP(v)
			if ip == nil {
				return "", 0, fmt.Errorf("invalid IPv6: failed to parse")
			}
			ip = ip.To16()
			if ip == nil {
				return "", 0, fmt.Errorf("invalid IPv6: parsed, but no IPv6")
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
			return "", 0, fmt.Errorf("invalid IPv6: array length neither 8 nor 16")
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
					return "", 0, fmt.Errorf("invalid IPv6: part out of range")
				}
				v := uint64(v.(float64))
				if v > maxVal {
					return "", 0, fmt.Errorf("invalid IPv6: part out of range")
				}
				setPart(i, v)
			case string:
				v, err := strconv.ParseUint(v.(string), 0, bitSize)
				if err != nil {
					return "", 0, fmt.Errorf("invalid IPv6: %s", err)
				}
				if v > maxVal {
					return "", 0, fmt.Errorf("invalid IPv6: part out of range")
				}
				setPart(i, v)
			default:
				return "", 0, fmt.Errorf("invalid IPv6: not string or number")
			}
		}
	default:
		return "", 0, fmt.Errorf("invalid IPv6: not string or array")
	}
	ttl, err := getDuration("ttl", obj, qp)
	if err != nil {
		return "", 0, err
	}
	content := ip.String()
	return content, ttl, nil
}

func ptr(obj map[string]interface{}, qp *queryParts) (string, time.Duration, error) {
	hostname, err := getString("hostname", obj, qp)
	if err != nil {
		return "", 0, err
	}
	hostname = strings.TrimSpace(hostname)
	hostname = fqdn(hostname, qp.zone)
	ttl, err := getDuration("ttl", obj, qp)
	if err != nil {
		return "", 0, err
	}
	content := fmt.Sprintf("%s", hostname)
	return content, ttl, nil
}
