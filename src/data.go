/* Copyright 2016-2024 nix <https://keybase.io/nixn>

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
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// TODO use more object-oriented style

type recordType struct {
	content  string
	priority *uint16       // only used when pdnsVersion == 3
	ttl      time.Duration // TODO make TTL an option, not a value
	version  *VersionType
}

type valuesType struct {
	key              string // the exact ETCD key
	value            interface{}
	isLastFieldValue bool
	version          *VersionType
}

type defoptType struct {
	values  objectType[any]
	version *VersionType
}

type dataNode struct {
	mutex     sync.RWMutex
	parent    *dataNode
	lname     string // local name
	keyPrefix string
	defaults  map[string]map[string]defoptType // <QTYPE> or "" → (<id> → values)
	options   map[string]map[string]defoptType // <QTYPE> or "" → (<id> → values)
	values    map[string]map[string]valuesType // <QTYPE> or "" → (<id> → values) // unprocessed, key "" means lastFieldValue
	records   map[string]map[string]recordType // <QTYPE> → (<id> → record) // processed
	children  map[string]*dataNode             // key = <lname of subdomain>
	maxRev    int64                            // the maximum of Rev of all ETCD items
}

func newDataNode(parent *dataNode, lname, keyPrefix string) *dataNode {
	return &dataNode{
		mutex:     sync.RWMutex{},
		parent:    parent,
		lname:     lname,
		keyPrefix: keyPrefix,
		defaults:  map[string]map[string]defoptType{},
		options:   map[string]map[string]defoptType{},
		values:    map[string]map[string]valuesType{},
		records:   map[string]map[string]recordType{},
		children:  map[string]*dataNode{},
		maxRev:    0,
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

func (dn *dataNode) log(args ...any) *logrus.Entry {
	return logFrom(log.data(), append([]any{"dn", dn.getQname()}, args...)...)
}

func (dn *dataNode) getName() *nameType {
	var parts []namePart
	for dn := dn; dn.lname != ""; dn = dn.parent {
		parts = append(parts, namePart{dn.lname, dn.keyPrefix})
	}
	name := nameType(reversed(parts))
	return &name
}

// this method is only called from reload(), which itself is called under writer lock, so no locking needed here
func (dn *dataNode) getChildCreate(name nameType) *dataNode {
	if name.len() == 0 {
		return dn
	}
	childLName := name.lname(1)
	lChild, ok := dn.children[childLName]
	if !ok || lChild == nil {
		lChild = newDataNode(dn, childLName, name.keyPrefix(1))
		dn.children[childLName] = lChild
	}
	return lChild.getChildCreate(name.fromDepth(2))
}

func (dn *dataNode) getChild(name nameType, rLock bool) *dataNode {
	if rLock {
		dn.mutex.RLock()
	}
	if name.len() == 0 {
		return dn
	}
	childLName := name.lname(1)
	lChild, ok := dn.children[childLName]
	if !ok || lChild == nil {
		return dn
	}
	return lChild.getChild(name.fromDepth(2), rLock)
}

func (dn *dataNode) rUnlockUpwards(stopAt *dataNode) {
	for dn := dn; dn != stopAt; dn = dn.parent {
		dn.mutex.RUnlock()
	}
}

func (dn *dataNode) zoneRev() int64 {
	rev := dn.maxRev
	for _, dn := range dn.children {
		if dn.hasSOA() {
			continue
		}
		rev = maxOf(rev, dn.zoneRev())
	}
	return rev
}

func (dn *dataNode) recordsCount() int {
	count := len(dn.records)
	for _, child := range dn.children {
		count += child.recordsCount()
	}
	return count
}

func (dn *dataNode) zonesCount() int {
	count := 0
	if records, ok := dn.records["SOA"]; ok {
		if _, ok := records[""]; ok {
			count++
		}
	}
	for _, child := range dn.children {
		count += child.zonesCount()
	}
	return count
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

func parseEntryKey(key string) (name nameType, entryType entryType, qtype, id string, version *VersionType, err error) {
	key = strings.TrimPrefix(key, *args.Prefix)
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
		err = fmt.Errorf("SOA entry cannot have an id (%q)", id)
		return
	}
	return
}

func parseEntryContent(value []byte, allowString bool) (interface{}, bool, error) {
	if len(value) == 0 {
		if allowString {
			return "", false, nil
		}
		return nil, false, fmt.Errorf("empty")
	}
	switch value[0] {
	case '=': // last-field-value syntax
		var content interface{}
		err := json.Unmarshal(value[1:], &content)
		if err != nil {
			return nil, true, fmt.Errorf("failed to parse as JSON value: %s", err)
		}
		return content, true, nil
	case '{':
		values := objectType[any](nil)
		err := json.Unmarshal(value, &values)
		if err != nil {
			return nil, false, fmt.Errorf("failed to parse as JSON object: %s", err)
		}
		return values, false, nil
	}
	if allowString {
		return string(value), false, nil
	}
	return nil, false, fmt.Errorf("invalid")
}

func (dn *dataNode) reload(dataChan <-chan etcdItem) {
	since := time.Now()
	clearMap(dn.defaults)
	clearMap(dn.options)
	clearMap(dn.values)
	clearMap(dn.records)
	clearMap(dn.children)
	dn.log().Debug("processing entry items from ETCD")
	depth := dn.depth()
ITEMS:
	for item := range dataChan {
		name, entryType, qtype, id, version, err := parseEntryKey(item.Key)
		dn.log().Tracef("parsed %q into name %q type %q qtype %q id %q version %q err %q", item.Key, name.normal(), entryType, qtype, id, version, err2str(err))
		// check version first, because a higher version (than our current dataVersion) could change the key syntax (but not prefix and version suffix)
		if version != nil && !dataVersion.isCompatibleTo(version) {
			dn.log("my", dataVersion, "their", *version).Tracef("ignoring entry %q due to version incompatibility", item.Key)
			continue ITEMS
		}
		if err != nil {
			dn.log().Warnf("failed to parse entry key %q: %s", item.Key, err)
			continue ITEMS
		}
		// check if the entry belongs to this domain
		if name.len() < depth {
			continue ITEMS
		}
		for dn := dn; dn != nil; dn = dn.parent {
			if name.lname(dn.depth()) != dn.lname {
				continue ITEMS
			}
		}
		itemData := dn.getChildCreate(name.fromDepth(depth + 1))
		if version != nil {
			// check version against a possibly already stored value, overwrite value only if it's a "better" version
			var currVersion *VersionType
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
				var vals map[string]map[string]defoptType
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
			if currVersion != nil && version.Minor <= currVersion.Minor {
				dn.log("new", *version, "old", *currVersion).Tracef("ignoring entry %q, because its' version's minor (new) is less than the current entry's version's minor (old)", item.Key)
				continue ITEMS
			}
		}
		// handle content
		value, isLastFieldValue, err := parseEntryContent(item.Value, entryType == normalEntry)
		if err != nil {
			dn.log().Errorf("failed to parse content of %q: %s", item.Key, err)
			continue ITEMS
		}
		rrParams := rrParams{
			qtype:   qtype,
			id:      id,
			data:    itemData,
			version: version,
		}
		switch entryType {
		case normalEntry:
			// if entry already present, only overwrite it if version dictates it, otherwise ignore
			if curr, ok := itemData.values[qtype]; ok {
				if curr, ok := curr[id]; ok {
					if version == nil && curr.version == nil {
						dn.log().Errorf("ignoring entry %q due to duplication", item.Key)
						continue ITEMS
					}
					if version != nil && curr.version != nil && version.Minor <= curr.version.Minor {
						dn.log("old", curr.version, "new", version).Tracef("ignoring entry %q due to version constraints", item.Key)
						continue ITEMS
					}
					dn.log("target", rrParams.Target(), "entry", item.Key, "old-version", curr.version).Trace("overriding existing entry due to version constraints")
				}
			} else {
				itemData.values[qtype] = map[string]valuesType{}
			}
			itemData.values[qtype][id] = valuesType{item.Key, value, isLastFieldValue, version}
		case defaultsEntry:
			fallthrough
		case optionsEntry:
			var vals map[string]map[string]defoptType
			if entryType == defaultsEntry {
				vals = itemData.defaults
			} else {
				vals = itemData.options
			}
			if curr, ok := vals[qtype]; ok {
				if curr, ok := curr[id]; ok {
					if version != nil && curr.version != nil && version.Minor <= curr.version.Minor {
						continue ITEMS
					}
				}
			} else {
				vals[qtype] = map[string]defoptType{}
			}
			vals[qtype][id] = defoptType{value.(objectType[any]), version}
			dn.log().Tracef("stored %s for %s: %v", entryType2key[entryType], rrParams.Target(), value)
		default:
			dn.log().Warnf("unsupported entry type %q, ignoring entry %q", entryType, item.Key)
		}
		// now we are sure this entry was stored => update maxRev
		itemData.maxRev = maxOf(itemData.maxRev, item.Rev)
	}
	dn.processValues()
	dur := time.Since(since)
	dn.log("duration", dur).Trace("reload() finished")
}

func (dn *dataNode) processValues() {
	dn.log().Trace("processing values to records")
	dn.records = map[string]map[string]recordType{}
	// process SOA first, to have proper zone appending for other entries
	if values, ok := dn.values["SOA"]; ok {
		for id, values := range values {
			rrParams := rrParams{
				qtype:   "SOA",
				id:      id,
				version: values.version,
				data:    dn,
			}
			processValuesEntry(&rrParams, &values)
		}
	}
	for qtype, values := range dn.values {
		if qtype == "SOA" {
			continue
		}
		for id, values := range values {
			rrParams := rrParams{
				qtype:   qtype,
				id:      id,
				version: values.version,
				data:    dn,
			}
			processValuesEntry(&rrParams, &values)
		}
	}
	for _, child := range dn.children {
		child.processValues()
	}
}

func processValuesEntry(rrParams *rrParams, values *valuesType) {
	ttl, vPath, err := getDuration("ttl", rrParams)
	if vPath == nil || err != nil {
		logFrom(log.data(), "vp", vPath, "error", err).Errorf("failed to get TTL for entry %q, ignoring", values.key)
		return
	}
	rrParams.ttl = ttl
	if values.isLastFieldValue {
		rrFunc := rr2func[rrParams.qtype]
		if rrFunc == nil {
			log.data().WithField("entry", values.key).Errorf("record type %q is not object-supported (tried to use last-field-value syntax)", rrParams.qtype)
			return
		}
		rrParams.values = objectType[any]{}
		rrParams.lastFieldValue = &values.value
		rrFunc(rrParams)
	} else {
		switch value := values.value.(type) {
		case string:
			if rrParams.qtype == "SOA" {
				log.data().Errorf("ignoring plain string entry %q, because it is a SOA record, which must be of object type", values.key)
				return
			}
			logFrom(log.data(), "value", value).Tracef("found plain string value for %s", rrParams.Target())
			rrParams.SetContent(value, nil)
		case objectType[any]:
			rrFunc := rr2func[rrParams.qtype]
			if rrFunc == nil {
				log.data().WithField("entry", values.key).Errorf("record type %q is not object-supported", rrParams.qtype)
				return
			}
			rrParams.values = value
			rrParams.lastFieldValue = nil
			rrFunc(rrParams)
		default:
			log.data().Errorf("ignoring entry %q, has unhandled content data type %T", values.key, value)
		}
	}
}
