/* Copyright 2016-2022 nix <https://keybase.io/nixn>

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
	"encoding/json"
	"fmt"
	"strings"
)

// TODO use more object-oriented style
type recordType struct {
	value   interface{} // objectType (from JSON) or string (plain entry)
	version *versionType
}

type valuesType struct {
	values  objectType
	version *versionType
}

type dataNode struct {
	parent    *dataNode
	lname     string // local name
	keyPrefix string
	defaults  map[string]map[string]valuesType // <QTYPE> or "" → (<id> → values)
	options   map[string]map[string]valuesType // <QTYPE> or "" → (<id> → values)
	records   map[string]map[string]recordType // <QTYPE> → (<id> → record)
	children  map[string]*dataNode             // key = <lname of subdomain>. if children[lname] == nil, the subdomain is present, but the data is not loaded (would be a subzone?)
}

func newDataNode(parent *dataNode, lname, keyPrefix string) *dataNode {
	return &dataNode{
		parent:    parent,
		lname:     lname,
		keyPrefix: keyPrefix,
		defaults:  map[string]map[string]valuesType{},
		options:   map[string]map[string]valuesType{},
		records:   map[string]map[string]recordType{},
		children:  map[string]*dataNode{},
	}
}

func (dn *dataNode) String() string {
	return fmt.Sprintf("%q, hasSOA: %v, #records: %d, #children: %d", dn.getQname(), dn.hasSOA(), len(dn.records), len(dn.children))
}

func (dn *dataNode) getQname() string {
	qname := dn.lname + "."
	for dn := dn.parent; dn != nil && len(dn.lname) > 0; dn = dn.parent {
		qname += dn.lname + "."
	}
	return qname
}

func (dn *dataNode) prefixKey() string {
	if dn.isRoot() {
		return ""
	}
	return dn.getName().asKey(true)
}

func (dn *dataNode) isRoot() bool {
	return dn.parent == nil
}

// 0 = root, 1 = TLD, ...
func (dn *dataNode) depth() int {
	if dn.isRoot() {
		return 0
	}
	return dn.parent.depth() + 1
}

func (dn *dataNode) hasSOA() bool {
	records, ok := dn.records["SOA"]
	return ok && len(records) > 0
}

func (dn *dataNode) findUpwards(pred func(*dataNode) bool) *dataNode {
	for dn := dn; dn != nil; dn = dn.parent {
		if pred(dn) {
			return dn
		}
	}
	return nil
}

func (dn *dataNode) findZone() *dataNode {
	return dn.findUpwards(func(data *dataNode) bool {
		return data.hasSOA()
	})
}

func (dn *dataNode) getRoot() *dataNode {
	for !dn.isRoot() {
		dn = dn.parent
	}
	return dn
}

func (dn *dataNode) getName() *nameType {
	var parts []namePart
	for dn := dn; dn.lname != ""; dn = dn.parent {
		parts = append(parts, namePart{dn.lname, dn.keyPrefix})
	}
	name := nameType(reversed(parts))
	return &name
}

func (dn *dataNode) getChild(name nameType, create bool) *dataNode {
	if name.len() == 0 {
		return dn
	}
	data := dn
	for depth := 1; depth <= name.len(); depth++ {
		lname := name.name(depth)
		childData, ok := data.children[lname]
		if !ok || childData == nil {
			if create {
				childData = newDataNode(data, lname, name.keyPrefix(depth))
				data.children[lname] = childData
			} else {
				return data
			}
		}
		data = childData
	}
	return data
}

func cutKey(key, separator string) (string, string) {
	idx := strings.LastIndex(key, separator)
	if idx < 0 {
		return key, ""
	}
	return key[:idx], key[idx+len(separator):]
}

func cutParts(parts []string, predicate func(string) bool) ([]string, string) {
	idx := len(parts) - 1
	if idx < 0 {
		return parts, ""
	}
	if predicate(parts[idx]) {
		return parts[:idx], parts[idx]
	}
	return parts, ""
}

func parseEntryKey(key string) (name nameType, entryType entryType, qtype, id string, version *versionType, err error) {
	key = strings.TrimPrefix(key, prefix)
	// note: qtype is also used as temp variable until it is set itself
	// version
	key, qtype = cutKey(key, versionSeparator)
	if qtype != "" {
		version, err = parseEntryVersion(qtype)
		if err != nil {
			err = fmt.Errorf("failed to parse version: %s", err)
			return
		}
	}
	// id
	key, id = cutKey(key, idSeparator)
	// name+entryType+qtype
	parts := splitDomainName(key, keySeparator)
	// qtype
	parts, qtype = cutParts(parts, qtypeRegex.MatchString)
	// entryType
	{
		idx := len(parts) - 1
		if idx >= 0 {
			if entryT, ok := key2entryType[parts[idx]]; ok {
				entryType = entryT
				parts = parts[:idx]
			} else {
				entryType = normalEntry
			}
		}
	}
	// name
	var nameParts []namePart
	for _, part := range parts {
		subParts := splitDomainName(part, ".")
		for i := 0; i < len(subParts); i++ {
			var keyPrefix string
			if len(nameParts) == 0 { // first part has no prefix
				keyPrefix = ""
			} else if i == 0 { // otherwise first sub-part was separated by keySeparator (splitted earlier)
				keyPrefix = keySeparator
			} else { // other sub-parts were separated by a dot
				keyPrefix = "."
			}
			nameParts = append(nameParts, namePart{subParts[i], keyPrefix})
		}
	}
	name = nameType(nameParts)
	// validation
	if entryType == normalEntry && qtype == "" {
		err = fmt.Errorf("empty qtype")
		return
	}
	if entryType == normalEntry && qtype == "SOA" && id != "" {
		err = fmt.Errorf("SOA cannot have an id (%q)", id)
		return
	}
	return
}

func parseEntryContent(value []byte, allowString bool) (interface{}, error) {
	if allowString && (len(value) == 0 || value[0] != '{') {
		return string(value), nil
	}
	values := objectType{}
	err := json.Unmarshal(value, &values)
	if err != nil {
		return nil, fmt.Errorf("failed to parse as JSON object: %s", err)
	}
	return values, nil
}

type counts struct {
	zones   uint64
	records uint64
}

func (dn *dataNode) reload(dataChan <-chan keyValuePair) (counts counts) {
	clearMap(dn.defaults)
	clearMap(dn.options)
	clearMap(dn.records)
	clearMap(dn.children)
ITEMS:
	for item := range dataChan {
		name, entryType, qtype, id, version, err := parseEntryKey(item.Key)
		log.data.Tracef("parsed %q into name %q type %q qtype %q id %q version %q err %q", item.Key, name.normal(), entryType, qtype, id, version, err)
		// check version first, because a higher version (than our current dataVersion) could change the key syntax (but not prefix and version suffix)
		if version != nil && !dataVersion.IsCompatibleTo(version) {
			log.data.Infof("ignoring %q due to version incompatibility (my: %s, their: %s)", item.Key, dataVersion.String(), version.String())
			continue ITEMS
		}
		if err != nil {
			log.data.WithError(err).Errorf("failed to parse entry key %q: %s", item.Key, err)
			continue ITEMS
		}
		// check if the entry belongs to this domain
		if name.len() < dn.depth() {
			continue ITEMS
		}
		for dn := dn; dn != nil; dn = dn.parent {
			if name.name(dn.depth()) != dn.lname {
				continue ITEMS
			}
		}
		itemData := dn.getChild(name.fromDepth(dn.depth()+1), true)
		if version != nil {
			// check version against a possibly already stored value, overwrite value only if it's a "better" version
			var currVersion *versionType
			switch entryType {
			case normalEntry:
				if curr, ok := itemData.records[qtype]; ok {
					if curr, ok := curr[id]; ok {
						currVersion = curr.version
					}
				}
			case defaultsEntry:
				fallthrough
			case optionsEntry:
				var vals map[string]map[string]valuesType
				if entryType == defaultsEntry {
					vals = itemData.defaults
				} else {
					vals = itemData.options
				}
				if curr, ok := vals[qtype]; ok {
					if curr, ok := curr[id]; ok {
						currVersion = curr.version
					}
				}
			}
			if currVersion != nil && version.minor <= currVersion.minor {
				continue ITEMS
			}
		}
		// handle content
		value, err := parseEntryContent(item.Value, entryType == normalEntry)
		if err != nil {
			log.data.WithError(err).Errorf("failed to parse content of %q", item.Key)
			continue ITEMS
		}
		switch entryType {
		case normalEntry:
			// if entry already present, only overwrite it if version dictates it, otherwise ignore
			if curr, ok := itemData.records[qtype]; ok {
				if curr, ok := curr[id]; ok {
					if version != nil && curr.version != nil && version.minor <= curr.version.minor {
						continue ITEMS
					}
				}
			} else {
				itemData.records[qtype] = map[string]recordType{}
				if qtype == "SOA" {
					counts.zones++
				}
			}
			itemData.records[qtype][id] = recordType{value, version}
			counts.records++
			log.data.Tracef("stored record %s%s%s%s%s: %v", name.normal(), keySeparator, qtype, idSeparator, id, value)
		case defaultsEntry:
			fallthrough
		case optionsEntry:
			var vals map[string]map[string]valuesType
			if entryType == defaultsEntry {
				vals = itemData.defaults
			} else {
				vals = itemData.options
			}
			if curr, ok := vals[qtype]; ok {
				if curr, ok := curr[id]; ok {
					if version != nil && curr.version != nil && version.minor <= curr.version.minor {
						continue ITEMS
					}
				}
			} else {
				vals[qtype] = map[string]valuesType{}
			}
			vals[qtype][id] = valuesType{value.(objectType), version}
			log.data.Tracef("stored %s for %s%s%s%s%s: %v", entryType2key[entryType], name.normal(), keySeparator, qtype, idSeparator, id, value)
		default:
			log.data.Warnf("unsupported entry type %q, ignoring entry %q", entryType, item.Key)
		}
	}
	return
}
