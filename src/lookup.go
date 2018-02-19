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

var dataCache *dataCacheType

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
	return recordKeys(&query.name, query.name.len(), withTrailingDot, query.qtype, true)[0].key
}

// TODO CNAME and DNAME also single value records?

func recordKeys(name *nameType, level int, withTrailingDot bool, qtype string, multi bool) []keyMultiPair {
	key := prefix + name.key(level, withTrailingDot) + keySeparator
	if qtype != "ANY" {
		key += qtype
		if qtype != "SOA" {
			key += keySeparator
		}
	}
	keys := []keyMultiPair(nil)
	// get the versioned first, they should be parsed first for structure upgrade procedure
	if !multi {
		keys = append(keys, keyMultiPair{key + versionSeparator, true})
	}
	keys = append(keys, keyMultiPair{key, multi})
	return keys
}

func getSingleLevelKeys(name *nameType, level int, qtypes ...keyMultiPair) []keyMultiPair {
	keys := []keyMultiPair(nil)
	for _, withTrailingDot := range []bool{true, false} {
		if level == 0 && !withTrailingDot {
			// skip the second root domain entry, b/c it's the same as first (the dot is always present)
			continue
		}
		for _, qtype := range qtypes {
			keys = append(keys, recordKeys(name, level, withTrailingDot, qtype.key, qtype.multi)...)
		}
	}
	return keys
}

func getMultiLevelKeys(name *nameType, qtypes ...keyMultiPair) []keyMultiPair {
	keys := []keyMultiPair(nil)
	for level := name.len(); level >= 0; level-- {
		keys = append(keys, getSingleLevelKeys(name, level, qtypes...)...)
	}
	return keys
}

func getDomainKeys(name *nameType) []keyMultiPair {
	// TODO this works only for reversed-names == true!
	// "com.example/"
	keys := recordKeys(name, name.len(), false, "ANY", true)
	// "com.example." (note the missing trailing slash, omitted to get the subdomains! that's why reversed-names must be true)
	keys = append(keys, keyMultiPair{strings.TrimSuffix(recordKeys(name, name.len(), true, "ANY", true)[0].key, keySeparator), true})
	// all parent defaults // TODO start at parent domain // TODO get also {<prefix><domain>/-defaults-, false} (no trailing slash)
	keys = append(keys, getMultiLevelKeys(name, keyMultiPair{defaultsKey, true})...)
	return keys
}

type rrFunc func(values objectType, id string, data *dataNode, revision int64) (content string, meta objectType, err error)

type entryType string // enum

const (
	normalEntry   entryType = "normal"
	defaultsEntry entryType = "defaults"
)

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

func parseEntryValue(value []byte) (interface{}, error) {
	if len(value) == 0 || value[0] != '{' {
		return string(value), nil
	}
	values := objectType{}
	err := json.Unmarshal(value, &values)
	if err != nil {
		return nil, fmt.Errorf("failed to parse as JSON object: %s", err)
	}
	return values, nil
}

func parseEntryKey(key string) (err error, name nameType, entryType entryType, qtype, id string, version *versionType) {
	key = strings.TrimPrefix(key, prefix)
	// version
	idx := strings.Index(key, versionSeparator)
	if idx >= 0 {
		version, err = parseEntryVersion(key[idx+len(versionSeparator):])
		if err != nil {
			err = fmt.Errorf("failed to parse version: %s", err)
			return
		}
		key = key[:idx]
	}
	sepLen := len(keySeparator)
	// name
	idx = strings.Index(key, keySeparator)
	if idx < 0 {
		err = fmt.Errorf("no separator %q", keySeparator)
		return
	}
	name = splitDomainName(key[:idx], false)
	key = key[idx+sepLen:] // strip name + separator
	// domain defaults (without trailing keySeparator)
	if key == defaultsKey {
		entryType = defaultsEntry
		return
	}
	// special entry "SOA"
	if key == "SOA" {
		entryType = normalEntry
		qtype = key
		return
	}
	idx = strings.Index(key, keySeparator)
	if idx < 0 {
		err = fmt.Errorf("missing separator after qtype %q", key)
		return
	}
	qtype = key[:idx]
	key = key[idx+sepLen:]
	// domain+QTYPE defaults
	if qtype == defaultsKey {
		entryType = defaultsEntry
		idx = strings.Index(key, keySeparator)
		if idx < 0 {
			qtype = key
			return
		}
		qtype = key[:idx]
		key = key[idx+sepLen:]
	} else {
		entryType = normalEntry
	}
	if entryType == normalEntry && qtype == "" {
		err = fmt.Errorf("empty qtype")
		return
	}
	id = key
	if entryType == normalEntry && qtype == "SOA" && id != "" {
		err = fmt.Errorf("SOA cannot have an id (%q)", id)
		return
	}
	return
}

func loadStructure(name *nameType) (*getResponseType, error) {
	keys := getMultiLevelKeys(name, keyMultiPair{"SOA", false})
	response, err := getall(keys, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get %v: %s", keys, err)
	}
	return response, nil
}

func readStructure(dataChan <-chan keyValuePair) error {
	for item := range dataChan {
		err, name, entryType, qtype, id, version := parseEntryKey(item.Key)
		// check version first, because a new version could change the key syntax (but not prefix and version suffix)
		if version != nil && !dataVersion.IsCompatibleTo(version) {
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to parse entry key %q: %s", item.Key, err)
		}
		if entryType != normalEntry || qtype != "SOA" || id != "" {
			return fmt.Errorf("not a normal SOA (no id) entry, but %v %q (%q)", entryType, qtype, id)
		}
		value, err := parseEntryValue(item.Value)
		if err != nil {
			return fmt.Errorf("failed to parse entry value for %q: %s", item.Key, err)
		}
		data, _ := dataCache.rootData.getChild(name.parts(), true)
		if oldEntries, ok := data.records[qtype]; ok {
			if oldEntry, ok := oldEntries[id]; ok {
				if version == nil && oldEntry.version != nil {
					continue
				}
				if version != nil && oldEntry.version != nil && version.minor < oldEntry.version.minor {
					continue
				}
			}
		} else {
			data.records[qtype] = map[string]recordType{}
		}
		data.records[qtype][id] = recordType{value, version}
		log.Printf("stored %q for %q: %#v @ %v", qtype, name.normal(), value, version)
	}
	return nil
}

func loadDomain(data *dataNode) error {
	keys := getDomainKeys(data.getName())
	response, err := getall(keys, &dataCache.revision)
	if err != nil {
		return fmt.Errorf("failed to get %v: %s", keys, err)
	}
	for item := range response.DataChan {
		// TODO refactor, duplicated code (readStructure)
		err, name, entryType, qtype, id, version := parseEntryKey(item.Key)
		// check version first, because a new version could change the key syntax (but not prefix and version suffix)
		if version != nil && !dataVersion.IsCompatibleTo(version) {
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to parse entry key %q: %s", item.Key, err)
		}
		data, _ := dataCache.rootData.getChild(name.parts(), true)
		switch entryType {
		case normalEntry:
			value, err := parseEntryValue(item.Value)
			if err != nil {
				return fmt.Errorf("failed to parse entry value for %q: %s", item.Key, err)
			}
			if oldEntries, ok := data.records[qtype]; ok {
				if oldEntry, ok := oldEntries[id]; ok {
					if version == nil && oldEntry.version != nil {
						continue
					}
					if version != nil && oldEntry.version != nil && version.minor < oldEntry.version.minor {
						continue
					}
				}
			} else {
				data.records[qtype] = map[string]recordType{}
			}
			data.records[qtype][id] = recordType{value, version}
			log.Printf("stored record: %s/%s/%s: %v", name.normal(), qtype, id, value)
		case defaultsEntry:
			values := objectType{}
			err := json.Unmarshal(item.Value, &values)
			if err != nil {
				return fmt.Errorf("failed to parse entry value as JSON object for %q: %s", item.Key, err)
			}
			if oldEntries, ok := data.defaults[qtype]; ok {
				if oldEntry, ok := oldEntries[id]; ok {
					if version == nil && oldEntry.version != nil {
						continue
					}
					if version != nil && oldEntry.version != nil && version.minor < oldEntry.version.minor {
						continue
					}
				}
			} else {
				data.defaults[qtype] = map[string]defaultsType{}
			}
			data.defaults[qtype][id] = defaultsType{values, version}
			log.Printf("stored defaults for %q%s%s%s%s: %v", name.normal(), keySeparator, qtype, keySeparator, id, values)
		default:
			log.Printf("unsupported entry type %q, ignoring entry %q", entryType, item.Key)
		}
	}
	data.loaded = true
	return nil
}

func refreshCache(name *nameType, now time.Time) error {
	log.Printf("loading structure data for %q", name)
	response, err := loadStructure(name)
	if err != nil {
		return fmt.Errorf("failed to load structure: %s", err)
	}
	newExpiresAt := now.Add(minCacheTime)
	if dataCache.revision != response.Revision {
		log.Printf("data changed to revision %d (from %d), dropping cache", response.Revision, dataCache.revision)
		dataCache = newDataCache(response.Revision, newExpiresAt)
	} else {
		log.Printf("cache revision still valid (%d), updating expiry time to %s", response.Revision, newExpiresAt)
		dataCache.expiresAt = newExpiresAt
	}
	// TODO should this be moved to 'cache dropped' block?
	err = readStructure(response.DataChan) // readStructure must be idempotent for existing entries
	if err != nil {
		dataCache = newDataCache(response.Revision, time.Time{})
		return fmt.Errorf("failed to read structure: %s", err)
	}
	return nil
}

func getData(name *nameType) (data *dataNode, depth int, err error) {
	now := time.Now()
	if dataCache.expiresAt.Before(now) {
		log.Printf("cache expired at %s, refreshing", dataCache.expiresAt)
		if err := refreshCache(name, now); err != nil {
			// TODO check if requested data is present and still valid by TTL. if yes, return it
			return nil, 0, fmt.Errorf("failed to refresh cache: %s", err)
		}
	}
	data, depth = dataCache.rootData.getChild(name.parts(), false)
	load := data.findUpwards(func(data *dataNode) bool {
		return data.loaded
	})
	if load == nil {
		load = data.findZone()
		if load == nil {
			load = data
		}
	}
	if !load.loaded {
		log.Printf("loading domain %q", load.getQname())
		if err := loadDomain(load); err != nil {
			return nil, 0, fmt.Errorf("failed to load domain %q: %s", load.getQname(), err)
		}
	}
	if depth < name.len() {
		data, depth = dataCache.rootData.getChild(name.parts(), false)
	}
	return data, depth, nil
}

func lookup(params objectType) (interface{}, error) {
	query := queryType{
		name:  nameType(splitDomainName(params["qname"].(string), reversedNames)),
		qtype: params["qtype"].(string),
	}
	data, depth, err := getData(&query.name)
	if err != nil {
		return false, fmt.Errorf("failed to get data: %s", err)
	}
	var result []objectType
	if depth < query.name.len() {
		log.Printf("no such domain: %q", query.name)
		if data.findZone() == nil {
			return false, nil
		} else {
			return result, nil
		}
	}
	records := map[string]map[string]recordType{}
	if query.isANY() {
		records = data.records
	} else {
		records[query.qtype] = data.records[query.qtype]
	}
	for qtype, records := range records {
		rrFunc := rr2func[qtype]
		for id, record := range records {
			var content string
			var meta objectType
			// TODO read/handle TTL only here, not in rrFunc (as for plain string)
			switch record.value.(type) {
			case objectType:
				if rrFunc == nil {
					return false, fmt.Errorf("unsupported QTYPE %q, but have JSON data for it in %q%s%s%s%s", qtype, data.getQname(), keySeparator, qtype, keySeparator, id)
				}
				content, meta, err = rrFunc(record.value.(objectType), id, data, dataCache.revision)
				if err != nil {
					return false, fmt.Errorf("failed to get content and TTL for %s%s%s%s%s: %s", data.getQname(), keySeparator, qtype, keySeparator, id, err)
				}
			case string:
				// TODO error when records with 'priority' field or SOA (due to 'serial' field) are not of objectType
				content = record.value.(string)
				ttl, err := getDuration("ttl", nil, qtype, id, data)
				if err != nil {
					return false, fmt.Errorf("failed to get TTL for %s%s%s%s%s: %s", data.getQname(), keySeparator, qtype, keySeparator, id, err)
				}
				meta = objectType{
					"ttl": ttl,
				}
			default:
				log.Fatalf("invalid record type: %T (%v)", record, record)
			}
			log.Printf("%s%s%s%s%s: %v → content: %v, meta: %v", data.getQname(), keySeparator, qtype, keySeparator, id, record, content, meta)
			result = append(result, makeResultItem(qtype, data, content, meta))
		}
	}
	return result, nil
}

func makeResultItem(qtype string, data *dataNode, content string, meta objectType) objectType {
	zoneNode := data.findZone()
	result := objectType{
		"qname":   data.getQname(),
		"qtype":   qtype,
		"content": content,
		"ttl":     seconds(meta["ttl"].(time.Duration)),
		"auth":    zoneNode != nil,
	}
	if priority, ok := meta["priority"]; ok {
		result["priority"] = priority
	}
	return result
}

func findValue(name string, values objectType, qtype, id string, data *dataNode) (interface{}, error) {
	if v, ok := values[name]; ok {
		return v, nil
	}
	for data := data; data != nil; data = data.parent {
		for _, path := range []struct{ qtype, id string }{{qtype, id}, {"", id}, {qtype, ""}, {"", ""}} {
			if defaults, ok := data.defaults[path.qtype]; ok {
				if defaults, ok := defaults[path.id]; ok {
					if v, ok := defaults.values[name]; ok {
						return v, nil
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("missing %q", name)
}
