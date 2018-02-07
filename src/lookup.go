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
	name  nameType
	qtype string
}

func (query *queryType) String() string {
	return fmt.Sprintf("%s%s%s", query.name.normal(), keySeparator, query.qtype)
}

func (query *queryType) isANY() bool { return query.qtype == "ANY" }
func (query *queryType) isSOA() bool { return query.qtype == "SOA" }

func (query *queryType) recordKey(withTrailingDot bool) string {
	return recordKey(&query.name, query.name.len(), withTrailingDot, query.qtype)
}

// TODO CNAME and DNAME also single value records?

func recordKey(name *nameType, level int, withTrailingDot bool, qtype string) string {
	key := prefix + name.key(level, withTrailingDot)
	if qtype != "ANY" {
		key += qtype
		if qtype != "SOA" {
			key += keySeparator
		}
	}
	return key
}

func getKeys(name *nameType, onlySOA bool) []keyMultiPair {
	keys := []keyMultiPair(nil)
	for level := name.len(); level >= 0; level-- {
		for _, withTrailingDot := range []bool{true, false} {
			if !onlySOA {
				keys = append(keys, keyMultiPair{recordKey(name, level, withTrailingDot, defaultsKey), true})
			}
			keys = append(keys, keyMultiPair{recordKey(name, level, withTrailingDot, "SOA"), false})
			// TODO versioned keys
		}
	}
	return keys
}

type rrFunc func(values objectType, data *dataNode, revision int64) (content string, meta objectType, err error)

func splitDomainName(name string, reverse bool) []string {
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return []string(nil)
	}
	parts := strings.Split(name, ".")
	if reverse {
		parts = reversed(parts)
	}
	return parts
}

func lookup(params objectType) (interface{}, error) {
	// TODO re-enable zone ids and cache zone answers to reply subsequent requests for same zone (id) fast (without ETCD request)
	query := queryType{
		name:  nameType(splitDomainName(params["qname"].(string), reversedNames)),
		qtype: params["qtype"].(string),
	}
	keys := getKeys(&query.name, false)
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
	for level, maxLevel := 1, query.name.len(); level <= maxLevel; level++ {
		childLname := query.name.part(level)
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
			query.qtype = strings.TrimPrefix(itemKey, query.recordKey(false))
			if query.name.len() > 0 {
				query.qtype = strings.TrimPrefix(query.qtype, ".")
			}
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
				return false, fmt.Errorf("unknown/unimplemented qtype %q, but have (JSON) object data for it (%s)", query.qtype, query.recordKey(false))
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
