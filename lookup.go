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

type defaultsMessage struct {
	key   string
	value map[string]interface{}
	err   error
}

func loadDefaults(key string, revision int64, c chan interface{}) {
	response, err := get(key, true, &revision)
	if err != nil {
		c <- err
		return
	}
	c <- int(response.Count) + 1
	haveMain := false
	for _, item := range response.Kvs {
		itemKey := string(item.Key)
		msg := defaultsMessage{itemKey, map[string]interface{}{}, nil}
		msg.err = json.Unmarshal(item.Value, &msg.value)
		c <- msg
		if itemKey == key {
			haveMain = true
		}
	}
	if haveMain {
		c <- 0
	} else {
		c <- 1
		c <- defaultsMessage{key, map[string]interface{}{}, nil}
	}
}

func ensureDefaults(qp *queryParts) error {
	c := make(chan interface{}, 10)
	n := 0
	since := time.Now()
	for _, withSubdomain := range []bool{false, true} {
		key := qp.defaultsKey(withSubdomain, false)
		if _, ok := defaults.values[key]; !ok {
			log.Println("loading defaults:", key)
			go loadDefaults(key, *qp.revision, c)
			n++
		} else {
			log.Println("reusing defaults:", key)
		}
	}
	var err error
	for i := 0; i < n; i++ {
		msg := <-c
		switch msg.(type) {
		case int:
			n += msg.(int)
		case defaultsMessage:
			msg := msg.(defaultsMessage)
			if msg.err == nil {
				// TODO check record (QTYPE supported? version constraints, ...)
				log.Println("storing defaults:", msg.key)
				defaults.values[msg.key] = msg.value
			} else if err == nil {
				err = msg.err
			}
		case error:
			if err == nil {
				err = msg.(error)
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
	revision                      *int64
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

func (qp *queryParts) defaultsKey(withSubdomain, withQtype bool) string {
	key := prefix + qp.zone
	if withSubdomain {
		key += "/" + qp.subdomain
	}
	key += "/-defaults-/"
	if withQtype {
		key += qp.qtype
	}
	return key
}

func (qp *queryParts) isDefaultsKey(key string) bool {
	for _, withSubdomain := range []bool{false, true} {
		for _, withQtype := range []bool{false, true} {
			if key == qp.defaultsKey(withSubdomain, withQtype) {
				return true
			}
		}
	}
	return false
}

type rr_func func(obj map[string]interface{}, qp *queryParts) (string, time.Duration, error)

var rr2func map[string]rr_func = map[string]rr_func{
	"A":    a,
	"AAAA": aaaa,
	"NS":   ns,
	"PTR":  ptr,
	"SOA":  soa,
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
	} else if id, ok := zone2id[qp.qname]; ok {
		qp.zone = qp.qname
		qp.zoneId = id
		isNewZone = false
	} else {
		qp.zone = qp.qname
		isNewZone = true
	}
	if qp.isSOA() && !isNewZone {
		log.Printf("found zone '%s' as id '%d'", qp.zone, qp.zoneId)
	}
	qp.subdomain = extractSubdomain(qp.qname, qp.zone)
	if len(qp.subdomain) == 0 {
		qp.subdomain = "@"
	}
	response, err := get(qp.recordKey(), !qp.isSOA(), nil)
	if err != nil {
		return false, fmt.Errorf("failed to load %s: %s", qp.recordKey(), err)
	}
	qp.revision = &response.Header.Revision
	// defaults
	if defaults.revision != *qp.revision {
		// TODO recheck version
		log.Printf("clearing defaults cache. old revision: %d, new revision: %d", defaults.revision, *qp.revision)
		defaults.revision = *qp.revision
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
		if qp.isDefaultsKey(itemKey) { // this is needed for 'ANY' requests
			continue
		}
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
			rrFunc, ok := rr2func[qp.qtype]
			if !ok {
				return false, fmt.Errorf("unknown/unimplemented qtype '%s', but have (JSON) object data for it (%s)", qp.qtype, qp.recordKey())
			}
			content, ttl, err = rrFunc(obj, &qp)
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

func extractSubdomain(domain, zone string) string {
	subdomain := strings.TrimSuffix(domain, zone)
	subdomain = strings.TrimSuffix(subdomain, ".")
	return subdomain
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

func findValue(name string, obj map[string]interface{}, qp *queryParts) (interface{}, error) {
	if v, ok := obj[name]; ok {
		return v, nil
	}
	if err := ensureDefaults(qp); err != nil {
		return nil, err
	}
	for _, withSubdomain := range []bool{true, false} {
		for _, withQtype := range []bool{true, false} {
			if v, ok := defaults.values[qp.defaultsKey(withSubdomain, withQtype)][name]; ok {
				return v, nil
			}
		}
	}
	return nil, fmt.Errorf("missing '%s'", name)
}
