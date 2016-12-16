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

func reversed(a []string) []string {
	n := len(a)
	r := make([]string, n)
	for i := 0; i < n; i++ {
		r[n-i-1] = a[i]
	}
	return r
}

type dataNode struct {
	parent   *dataNode
	hasSOA   bool
	lname    string // local name
	defaults map[string]*map[string]interface{}
	children map[string]*dataNode
}

func (dn *dataNode) getQname() string {
	qname := dn.lname
	for dn := dn.parent; dn != nil; dn = dn.parent {
		qname += "." + dn.lname
	}
	return qname
}

func (dn *dataNode) getZoneNode() *dataNode {
	for dn := dn; dn != nil; dn = dn.parent {
		if dn.hasSOA {
			return dn
		}
	}
	return nil
}

type query struct {
	nameParts []string
	qtype     string
}

// 'fqdn' is for getting the domain name in normal form (with a trailing dot)
func (q *query) fqdn() string {
	parts := q.nameParts
	if reversedNames {
		parts = reversed(parts)
	}
	return strings.Join(parts, ".") + "."
}

// 'name' is for getting the domain name in storage form
func (q *query) name(partsCount int) string {
	last := len(q.nameParts)
	if reversedNames {
		last = partsCount
	}
	name := strings.Join(q.nameParts[last-partsCount:last], ".")
	if name == "" {
		if noTrailingDotOnRoot {
			return ""
		}
		return "."
	}
	if noTrailingDot {
		return name
	}
	return name + "."
}

func (q *query) isANY() bool { return q.qtype == "ANY" }
func (q *query) isSOA() bool { return q.qtype == "SOA" }

// TODO CNAME and DNAME also single value records?

func (q *query) nameKey(partsCount int) string {
	return prefix + q.name(partsCount) + "/"
}

func (q *query) recordKey() string {
	key := q.nameKey(len(q.nameParts))
	if !q.isANY() {
		key += q.qtype
		if !q.isSOA() {
			key += "/"
		}
	}
	return key
}

func (q *query) getKeys() []keyMultiPair {
	keys := []keyMultiPair{{q.recordKey(), !q.isSOA()}} // record
	// defaults
	for i := len(q.nameParts); i >= 0; i-- {
		keys = append(keys, keyMultiPair{q.nameKey(i) + "-defaults-/", true})
		keys = append(keys, keyMultiPair{q.nameKey(i) + "SOA", false})
	}
	keys = append(keys, keyMultiPair{prefix + "-defaults-/", true}) // global defaults
	return keys
}

type rr_func func(values map[string]interface{}, data *dataNode, revision int64) (content string, meta map[string]interface{}, err error)

func splitDomainName(name string, reverse bool) []string {
	name = strings.TrimSuffix(name, ".")
	parts := strings.Split(name, ".")
	if reverse {
		for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
			parts[i], parts[j] = parts[j], parts[i]
		}
	}
	return parts
}

func lookup(params map[string]interface{}) (interface{}, error) {
	// TODO re-enable zone ids and cache zone answers to reply subsequent requests for same zone (id) fast (without ETCD request)
	q := query{
		nameParts: splitDomainName(params["qname"].(string), reversedNames),
		qtype:     params["qtype"].(string),
	}
	keys := q.getKeys()
	response, err := getall(keys, nil)
	if err != nil {
		return false, fmt.Errorf("failed to load %v +: %s", keys[0], err)
	}
	if !response.Succeeded {
		return false, fmt.Errorf("query not succeeded (%v +)", keys[0])
	}
	result := []map[string]interface{}{}
	itemResponse := response.Responses[0].GetResponseRange()
	if itemResponse.Count == 0 {
		return result, nil // 'result' is empty yet!
	}
	dataTree := &dataNode{
		lname:    "",
		defaults: map[string]*map[string]interface{}{},
		children: map[string]*dataNode{},
	}
	for _, treeResponseOp := range response.Responses[1:] {
		for _, item := range treeResponseOp.GetResponseRange().Kvs {
			qtype := strings.TrimPrefix(string(item.Key), prefix)
			idx := strings.Index(qtype, "/")
			if idx < 0 {
				log.Fatal("should never happen: idx < 0 for", string(item.Key))
			}
			name := qtype[:idx]
			qtype = qtype[idx+1:] // name + slash
			var nameParts []string
			isSoaEntry := false
			if name == "-defaults-" { // global defaults
				name = ""
				nameParts = []string{}
			} else { // domain defaults or SOA
				nameParts = splitDomainName(name, !reversedNames)
				if qtype == "SOA" {
					isSoaEntry = true
				} else {
					qtype = strings.TrimPrefix(qtype, "-defaults-/")
				}
			}
			if _, ok := rr2func[qtype]; !ok {
				if qtype != "" {
					log.Printf("unsupported qtype %q, ignoring defaults entry %q", qtype, string(item.Key))
					continue
				}
			}
			data := dataTree
			for _, namePart := range nameParts {
				if childData, ok := data.children[namePart]; ok {
					data = childData
				} else {
					childData = &dataNode{
						parent:   data,
						lname:    namePart,
						defaults: map[string]*map[string]interface{}{},
						children: map[string]*dataNode{},
					}
					data.children[namePart] = childData
					data = childData
				}
			}
			if isSoaEntry {
				data.hasSOA = true
				//log.Println("found SOA:", data.getQname())
				continue
			}
			var defaults *map[string]interface{}
			if v, ok := data.defaults[qtype]; ok {
				defaults = v
			} else {
				v = &map[string]interface{}{}
				data.defaults[qtype] = v
				defaults = v
			}
			err := json.Unmarshal(item.Value, defaults)
			if err != nil {
				return false, fmt.Errorf("failed to parse JSON (as object) for %q: %s", string(item.Key), err)
			}
		}
	}
	data := dataTree
	start := len(q.nameParts)
	add := -1
	end := -1
	if reversedNames {
		end = start
		start = 0
		add = 1
	}
	for i := start; i != end; i += add {
		childLname := q.nameParts[i]
		if childData, ok := data.children[childLname]; ok {
			data = childData
		} else {
			childData = &dataNode{
				parent:   data,
				lname:    childLname,
				defaults: map[string]*map[string]interface{}{},
				children: map[string]*dataNode{},
			}
			data.children[childLname] = childData
			data = childData
		}
	}
	for _, item := range itemResponse.Kvs {
		itemKey := string(item.Key)
		if len(item.Value) == 0 {
			return false, fmt.Errorf("empty value for %q", string(item.Key))
		}
		q := q // clone (needed for ANY requests)
		if q.isANY() {
			q.qtype = strings.TrimPrefix(itemKey, q.recordKey())
			idx := strings.Index(q.qtype, "/")
			if idx >= 0 {
				q.qtype = q.qtype[:idx]
			}
		}
		if q.qtype == "-defaults-" { // this happens for ANY requests
			continue
		}
		var content string
		var meta map[string]interface{}
		if item.Value[0] == '{' {
			values := map[string]interface{}{}
			err = json.Unmarshal(item.Value, &values)
			if err != nil {
				return false, err
			}
			err = nil
			rrFunc, ok := rr2func[q.qtype]
			if !ok {
				return false, fmt.Errorf("unknown/unimplemented qtype %q, but have (JSON) object data for it (%s)", q.qtype, q.recordKey())
			}
			content, meta, err = rrFunc(values, data, response.Header.Revision)
			if err != nil {
				return false, err
			}
		} else {
			// TODO error when records with 'priority' field or SOA (due to 'serial' field) are not JSON objects
			content = string(item.Value)
			ttl, err := getDuration("ttl", nil, q.qtype, data)
			if err != nil {
				return false, err
			}
			meta = map[string]interface{}{
				"ttl": ttl,
			}
		}
		result = append(result, makeResultItem(q.qtype, data, content, meta))
	}
	return result, nil
}

func makeResultItem(qtype string, data *dataNode, content string, meta map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{
		"qname":   data.getQname(),
		"qtype":   qtype,
		"content": content,
		"ttl":     seconds(meta["ttl"].(time.Duration)),
		"auth":    data.getZoneNode() != nil,
	}
	if priority, ok := meta["priority"]; ok {
		result["priority"] = priority
	}
	return result
}

func seconds(dur time.Duration) int64 {
	return int64(dur.Seconds())
}

func findValue(name string, values map[string]interface{}, qtype string, data *dataNode) (interface{}, error) {
	if v, ok := values[name]; ok {
		return v, nil
	}
	for data := data; data != nil; data = data.parent {
		if defaults, ok := data.defaults[qtype]; ok {
			if v, ok := (*defaults)[name]; ok {
				return v, nil
			}
		}
		if defaults, ok := data.defaults[""]; ok {
			if v, ok := (*defaults)[name]; ok {
				return v, nil
			}
		}
	}
	return nil, fmt.Errorf("missing %q", name)
}
