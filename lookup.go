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
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

var (
	zone2id          = map[string]int32{}
	id2zone          = map[int32]string{}
	nextZoneId int32 = 1
)

var defaults struct {
	revision int64
	values   map[string]map[string]interface{} // key = "example.net" or "example.net/subdomain" or "example.net/[subdomain/]RR"
}

func extractSubdomain(domain, zone string) string {
	subdomain := strings.TrimSuffix(domain, zone)
	subdomain = strings.TrimSuffix(subdomain, ".")
	return subdomain
}

type defaultsMessage struct {
	key   string
	value map[string]interface{}
	err   error
}

func loadDefaults(key string, c chan defaultsMessage) {
	response, err := get(key, false)
	if err != nil {
		c <- defaultsMessage{key, nil, err}
		return
	}
	defs := map[string]interface{}{}
	if response.Count > 0 {
		err := json.Unmarshal(response.Kvs[0].Value, &defs)
		if err != nil {
			c <- defaultsMessage{key, nil, err}
			return
		}
	}
	c <- defaultsMessage{key, defs, nil}
}

func ensureDefaults(qp *queryParts) error {
	keys := []string{
		qp.zoneDefaultsKey(),
		qp.zoneQtypeDefaultsKey(),
		qp.zoneSubdomainDefaultsKey(),
		qp.zoneSubdomainQtypeDefaultsKey()}
	c := make(chan defaultsMessage, len(keys))
	n := 0
	since := time.Now()
	for _, key := range keys {
		if _, ok := defaults.values[key]; !ok {
			log.Println("loading defaults:", key)
			go loadDefaults(key, c)
			n++
		} else {
			log.Println("reusing defaults:", key)
		}
	}
	var err error
	for i := 0; i < n; i++ {
		msg := <-c
		if msg.err == nil {
			defaults.values[msg.key] = msg.value
		} else {
			if err == nil {
				err = msg.err
			}
		}
	}
	dur := time.Since(since)
	log.Println("ensureDefaults dur:", dur)
	return err
}

type queryParts struct {
	zoneId                        int32
	qname, zone, subdomain, qtype string
}

func (qp *queryParts) isANY() bool { return qp.qtype == "ANY" }
func (qp *queryParts) isSOA() bool { return qp.qtype == "SOA" }

func (qp *queryParts) zoneKey() string      { return prefix + qp.zone }
func (qp *queryParts) subdomainKey() string { return prefix + qp.zone + "/" + qp.subdomain }
func (qp *queryParts) recordKey() string {
	key := prefix + qp.zone + "/" + qp.subdomain
	if !qp.isANY() {
		key += "/" + qp.qtype
	}
	if !qp.isSOA() {
		key += "/"
	}
	return key
}

func (qp *queryParts) zoneDefaultsKey() string { return prefix + qp.zone + "/-defaults" }
func (qp *queryParts) zoneSubdomainDefaultsKey() string {
	return prefix + qp.zone + "/" + qp.subdomain + "/-defaults"
}
func (qp *queryParts) zoneQtypeDefaultsKey() string {
	return prefix + qp.zone + "/" + qp.qtype + "-defaults"
}
func (qp *queryParts) zoneSubdomainQtypeDefaultsKey() string {
	return prefix + qp.zone + "/" + qp.subdomain + "/" + qp.qtype + "-defaults"
}
func (qp *queryParts) isDefaultsKey(key string) bool {
	if key == qp.zoneDefaultsKey() {
		return true
	}
	if key == qp.zoneSubdomainDefaultsKey() {
		return true
	}
	if key == qp.zoneQtypeDefaultsKey() {
		return true
	}
	if key == qp.zoneSubdomainQtypeDefaultsKey() {
		return true
	}
	return false
}

func lookup(params map[string]interface{}) (interface{}, error) {
	qp := queryParts{
		qname:  params["qname"].(string),
		zoneId: int32(params["zone-id"].(float64)), // note: documentation says 'zone_id', but it's 'zone-id'! further it is called 'domain_id' in responses (what a mess)
		qtype:  params["qtype"].(string),
	}
	var isNewZone bool
	if z, ok := id2zone[qp.zoneId]; ok {
		qp.zone = z
		isNewZone = false
	} else {
		qp.zone = qp.qname
		isNewZone = true
	}
	qp.subdomain = extractSubdomain(qp.qname, qp.zone)
	if len(qp.subdomain) == 0 {
		qp.subdomain = "@"
	}
	response, err := get(qp.recordKey(), !qp.isSOA())
	if err != nil {
		return false, fmt.Errorf("failed to load %s: %s", qp.recordKey(), err)
	}
	// defaults
	if defaults.revision != response.Header.Revision {
		// TODO recheck version
		log.Println("clearing defaults cache. old revision:", defaults.revision, ", new revision:", response.Header.Revision)
		defaults.revision = response.Header.Revision
		defaults.values = map[string]map[string]interface{}{}
	}
	if qp.isSOA() && isNewZone && response.Count > 0 {
		qp.zoneId = nextZoneId
		nextZoneId++
		log.Printf("storing zone '%s' as id %d", qp.zone, qp.zoneId)
		zone2id[qp.zone] = qp.zoneId
		id2zone[qp.zoneId] = qp.zone
	}
	result := []map[string]interface{}{}
	for _, item := range response.Kvs {
		itemKey := string(item.Key)
		if qp.isDefaultsKey(itemKey) {
			continue
		} // this is needed for 'ANY' requests
		if len(item.Value) == 0 {
			return false, fmt.Errorf("empty value")
		}
		qp := qp // clone
		if qp.isANY() {
			qp.qtype = strings.TrimPrefix(itemKey, qp.recordKey())
			idx := strings.Index(qp.qtype, "/")
			if idx >= 0 {
				qp.qtype = qp.qtype[0:idx]
			}
		}
		var content string
		var ttl time.Duration
		if item.Value[0] == '{' {
			var obj map[string]interface{}
			err = json.Unmarshal(item.Value, &obj)
			if err != nil {
				return false, err
			}
			err = nil
			switch qp.qtype {
			case "SOA":
				content, ttl, err = soa(obj, &qp, response.Header.Revision)
			case "NS":
				content, ttl, err = ns(obj, &qp)
			case "A":
				content, ttl, err = a(obj, &qp)
			case "AAAA":
				content, ttl, err = aaaa(obj, &qp)
			case "PTR":
				content, ttl, err = ptr(obj, &qp)
			// TODO more qtypes
			default:
				return false, fmt.Errorf("unknown/unimplemented qtype '%s', but have (JSON) object data for it (%s)", qp.qtype, qp.recordKey())
			}
			if err != nil {
				return false, err
			}
		} else {
			content = string(item.Value)
			ttl, err = getDuration("ttl", nil, &qp)
			if err != nil {
				return false, err
			}
		}
		result = append(result, makeResultItem(&qp, content, ttl))
	}
	return result, nil
}

func makeResultItem(qp *queryParts, content string, ttl time.Duration) map[string]interface{} {
	return map[string]interface{}{
		"domain_id": qp.zoneId,
		"qname":     qp.qname,
		"qtype":     qp.qtype,
		"content":   content,
		"ttl":       seconds(ttl),
		"auth":      true,
	}
	// TODO handle 'priority'. from remote backend docs:
	// "Note: priority field is required before 4.0, after 4.0 priority is added to content. This applies to any resource record which uses priority, for example SRV or MX."
}

func seconds(dur time.Duration) int64 {
	return int64(dur.Seconds())
}
