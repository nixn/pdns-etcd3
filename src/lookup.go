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
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)



type queryType struct {
	nameParts []string
	qtype     string
}

// 'fqdn' is for getting the domain name in normal form (with a trailing dot)
func (query *queryType) fqdn() string {
	parts := query.nameParts
	if reversedNames {
		parts = reversed(parts)
	}
	return strings.Join(parts, ".") + "."
}

// 'name' is for getting the domain name in storage form
func (query *queryType) name(partsCount int) string {
	last := len(query.nameParts)
	if reversedNames {
		last = partsCount
	}
	name := strings.Join(query.nameParts[last-partsCount:last], ".")
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

func (query *queryType) isANY() bool { return query.qtype == "ANY" }
func (query *queryType) isSOA() bool { return query.qtype == "SOA" }

// TODO CNAME and DNAME also single value records?

func (query *queryType) nameKey(partsCount int) string {
	return prefix + query.name(partsCount) + keySeparator
}

func (query *queryType) recordKey() string {
	key := query.nameKey(len(query.nameParts))
	if !query.isANY() {
		key += query.qtype
		if !query.isSOA() {
			key += keySeparator
		}
	}
	return key
}

func (query *queryType) getKeys() []keyMultiPair {
	keys := []keyMultiPair{{query.recordKey(), !query.isSOA()}} // record
	// defaults
	for i := len(query.nameParts); i >= 0; i-- {
		keys = append(keys, keyMultiPair{query.nameKey(i) + defaultsKey + keySeparator, true})
		keys = append(keys, keyMultiPair{query.nameKey(i) + "SOA", false})
	}
	keys = append(keys, keyMultiPair{prefix + defaultsKey + keySeparator, true}) // global defaults
	return keys
}

type rrFunc func(values objectType, data *dataNode, revision int64) (content string, meta objectType, err error)

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

func lookup(params objectType) (interface{}, error) {
	// TODO re-enable zone ids and cache zone answers to reply subsequent requests for same zone (id) fast (without ETCD request)
	query := queryType{
		nameParts: splitDomainName(params["qname"].(string), reversedNames),
		qtype:     params["qtype"].(string),
	}
	keys := query.getKeys()
	response, err := getall(keys, nil)
	if err != nil {
		return false, fmt.Errorf("failed to load %v +: %s", keys[0], err)
	}
	if !response.Succeeded {
		return false, fmt.Errorf("query not succeeded (%v +)", keys[0])
	}
	result := []objectType(nil)
	itemResponse := response.Responses[0].GetResponseRange()
	if itemResponse.Count == 0 {
		return result, nil // 'result' is empty yet!
	}
	dataTree := &dataNode{
		lname:    "",
		defaults: map[string]*objectType{},
		children: map[string]*dataNode{},
	}
	for _, treeResponseOp := range response.Responses[1:] {
		for _, item := range treeResponseOp.GetResponseRange().Kvs {
			qtype := strings.TrimPrefix(string(item.Key), prefix)
			idx := strings.Index(qtype, keySeparator)
			if idx < 0 {
				log.Fatal("should never happen: idx < 0 for", string(item.Key))
			}
			name := qtype[:idx]
			qtype = qtype[idx+len(keySeparator):] // name + separator
			var nameParts []string
			isSoaEntry := false
			if name == defaultsKey { // global defaults
				name = ""
				nameParts = []string(nil)
			} else { // domain defaults or SOA
				nameParts = splitDomainName(name, !reversedNames)
				if qtype == "SOA" {
					isSoaEntry = true
				} else {
					qtype = strings.TrimPrefix(qtype, defaultsKey+keySeparator)
				}
			}
			if _, ok := rr2func[qtype]; !ok {
				if qtype != "" {
					log.Printf("unsupported qtype %q, ignoring entry %q", qtype, string(item.Key))
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
						defaults: map[string]*objectType{},
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
			var defaults *objectType
			if v, ok := data.defaults[qtype]; ok {
				defaults = v
			} else {
				v = &objectType{}
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
	start := len(query.nameParts)
	add := -1
	end := -1
	if reversedNames {
		end = start
		start = 0
		add = 1
	}
	for i := start; i != end; i += add {
		childLname := query.nameParts[i]
		if childData, ok := data.children[childLname]; ok {
			data = childData
		} else {
			childData = &dataNode{
				parent:   data,
				lname:    childLname,
				defaults: map[string]*objectType{},
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
		query := query // clone (needed for ANY requests)
		if query.isANY() {
			query.qtype = strings.TrimPrefix(itemKey, query.recordKey())
			idx := strings.Index(query.qtype, keySeparator)
			query.qtype = query.qtype[:idx]
		}
		if query.qtype == defaultsKey { // this happens for ANY requests
			continue
		}
		var content string
		var meta objectType
		if item.Value[0] == '{' {
			values := objectType{}
			err = json.Unmarshal(item.Value, &values)
			if err != nil {
				return false, err
			}
			err = nil
			rrFunc, ok := rr2func[query.qtype]
			if !ok {
				return false, fmt.Errorf("unknown/unimplemented qtype %q, but have (JSON) object data for it (%s)", query.qtype, query.recordKey())
			}
			content, meta, err = rrFunc(values, data, response.Header.Revision)
			if err != nil {
				return false, err
			}
		} else {
			// TODO error when records with 'priority' field or SOA (due to 'serial' field) are not JSON objects
			content = string(item.Value)
			ttl, err := getDuration("ttl", nil, query.qtype, data)
			if err != nil {
				return false, err
			}
			meta = objectType{
				"ttl": ttl,
			}
		}
		result = append(result, makeResultItem(query.qtype, data, content, meta))
	}
	return result, nil
}

func makeResultItem(qtype string, data *dataNode, content string, meta objectType) objectType {
	result := objectType{
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

func findValue(name string, values objectType, qtype string, data *dataNode) (interface{}, error) {
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
