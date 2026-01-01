/* Copyright 2016-2026 nix <https://keybase.io/nixn>

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

var (
	// update this when changing data structure (only major/minor, patch is always 0). also change it in docs!
	dataVersion = VersionType{IsDevelopment: true, Major: 1, Minor: 1}
)

type recordType struct {
	content  string
	priority *uint16       // only used when pdnsVersion == 3
	ttl      time.Duration // TODO make TTL an option, not a value
	version  *VersionType
}

type objectValueType objectType[any]
type stringValueType string
type lastFieldValueType any

type valueType struct {
	key     string // the exact ETCD key
	content any
	version *VersionType
}

type dataNode struct {
	mutex     sync.RWMutex
	parent    *dataNode
	lname     string // local name
	keyPrefix string
	defaults  map[string]map[string]valueType  // <QTYPE> or "" → (<id> → value)
	options   map[string]map[string]valueType  // <QTYPE> or "" → (<id> → value)
	values    map[string]map[string]valueType  // <QTYPE> or "" → (<id> → value)
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
		defaults:  map[string]map[string]valueType{},
		options:   map[string]map[string]valueType{},
		values:    map[string]map[string]valueType{},
		records:   map[string]map[string]recordType{},
		children:  map[string]*dataNode{},
		maxRev:    0,
	}
}

func (dn *dataNode) String() string {
	return fmt.Sprintf("%q, hasSOA: %v, #records: %d, #children: %d", dn.getQname(), dn.hasSOA(), len(dn.records), len(dn.children))
}

func (dn *dataNode) getValuesFor(entryType entryType) map[string]map[string]valueType {
	switch entryType {
	case normalEntry:
		return dn.values
	case defaultsEntry:
		return dn.defaults
	case optionsEntry:
		return dn.options
	default:
		log.main().Fatalf("requested values for unknown entrytype %q", entryType)
		return nil
	}
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

func (dn *dataNode) log(fields ...any) *logrus.Entry {
	return log.data(append([]any{"dn", dn.getQname()}, fields...)...)
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
	// TODO use an automatically updated key for latest seen revision, because on deletion of keys the default zoneRev may jump backwards
	// or update the SOA record entry after a deletion to fix the revision
	// TODO for +auto-ptr and potentially +collect: maintain a list of dependent zones (up- and downwards) and take the highest revision as result (for all of them)
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

type domainInfo struct {
	Zone   string `json:"zone"`
	Serial int64  `json:"serial"`
}

func (dn *dataNode) allDomains(result []domainInfo) []domainInfo {
	if _, ok := dn.records["SOA"][""]; ok {
		zone, serial := dn.getQname(), dn.zoneRev()
		dn.log("zone", zone, "serial", serial).Trace("allDomains: found zone")
		result = append(result, domainInfo{zone, serial})
	}
	for _, child := range dn.children {
		result = child.allDomains(result)
	}
	return result
}

func targetString(qname, qtype, id string) string {
	return qname + keySeparator + qtype + idSeparator + id
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
		} else {
			entryType = normalEntry
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
		err = fmt.Errorf("empty qtype (name: %s)", name)
		return
	}
	if entryType == normalEntry && qtype == "SOA" && id != "" {
		err = fmt.Errorf("SOA entry cannot have an id (%q)", id)
		return
	}
	return
}

func parseEntryContent(value []byte, entryType entryType) (any, error) {
	if len(value) == 0 {
		if entryType == normalEntry {
			return stringValueType(""), nil
		}
		return nil, fmt.Errorf("empty")
	}
	switch value[0] {
	case '=': // last-field-value syntax
		if entryType != normalEntry {
			return nil, fmt.Errorf("a non-normal entry (defaults or options) must be an object")
		}
		var content any
		err := json.Unmarshal(value[1:], &content)
		if err != nil {
			return nil, fmt.Errorf("failed to parse as JSON value: %s", err)
		}
		return lastFieldValueType(content), nil
	case '{':
		var values objectType[any]
		err := json.Unmarshal(value, &values)
		if err != nil {
			return nil, fmt.Errorf("failed to parse as JSON object: %s", err)
		}
		return objectValueType(values), nil
	}
	if entryType == normalEntry {
		return stringValueType(value), nil
	}
	return nil, fmt.Errorf("invalid")
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
		name, entryType, qtype, id, itemVersion, err := parseEntryKey(item.Key)
		if name == nil {
			name = nameType{}
		}
		//goland:noinspection GoDfaErrorMayBeNotNil
		dn.log().Tracef("parsed %q into name %q type %q qtype %q id %q version %q err %q", item.Key, name.normal(), entryType, qtype, id, itemVersion, err2str(err))
		// check version first, because a higher version (than our current dataVersion) could change the key syntax (but not prefix and version suffix)
		if itemVersion != nil && !dataVersion.IsCompatibleTo(*itemVersion, false) {
			dn.log("my", dataVersion, "their", *itemVersion).Tracef("ignoring entry %q due to version incompatibility", item.Key)
			continue ITEMS
		}
		if err != nil {
			dn.log().WithError(err).Warnf("failed to parse entry key %q", item.Key)
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
		vals := itemData.getValuesFor(entryType)
		// check version against a possibly already stored value, overwrite value only if it's a "better" version. compatibility is already cleared (above).
		if curr, ok := vals[qtype][id]; ok {
			if itemVersion == nil {
				dn.log("current", curr.key).Tracef("ignoring (new) entry %q, because it is unversioned and cannot replace the current entry", item.Key)
				continue ITEMS
			}
			if curr.version != nil && itemVersion.Minor <= curr.version.Minor {
				dn.log("current", curr.key).Tracef("ignoring (new) entry %q, because its' version's minor is not greater than the current entry's version's minor", item.Key)
				continue ITEMS
			}
			target := targetString(itemData.getQname(), qtype, id)
			dn.log("target", target, "new-entry", item.Key, "current-version", *curr.version, "new-version", *itemVersion).Trace("overriding existing entry due to version constraints")
		}
		// handle content
		content, err := parseEntryContent(item.Value, entryType)
		if err != nil {
			dn.log().WithError(err).Errorf("failed to parse content of %q", item.Key)
			continue ITEMS
		}
		if _, ok := vals[qtype]; !ok {
			vals[qtype] = map[string]valueType{}
		}
		vals[qtype][id] = valueType{item.Key, content, itemVersion}
		itemData.maxRev = maxOf(itemData.maxRev, item.Rev)
		dn.log().Tracef("stored %s for %s: %v", string(entryType), targetString(itemData.getQname(), qtype, id), content)
	}
	dn.processValues()
	dur := time.Since(since)
	dn.log("duration", dur).Trace("reload() finished")
}

func (dn *dataNode) processValues() {
	dn.log().Trace("processing values to records")
	// process SOA first, to have proper zone appending for other entries
	if values, ok := dn.values["SOA"]; ok {
		valid := false
	IDS:
		for id, value := range values {
			if id == "" {
				if _, ok := value.content.(objectValueType); ok {
					rrParams := rrParams{
						qtype:   "SOA",
						id:      id,
						version: value.version,
						data:    dn,
						//logger:  log.data(), // TODO remove?
					}
					processValuesEntry(&rrParams, &value)
					if _, valid = dn.records["SOA"][""]; valid {
						break IDS
					}
				} else {
					// TODO if stringValueType: parse and handle like an objectValueType
					dn.log("id", id).Errorf("Ignoring SOA entry: the content must be of object type!")
				}
			} else {
				dn.log("id", id).Errorf("Ignoring SOA entry: a SOA entry may not have a non-empty ID!")
			}
		}
		if !valid {
			dn.log().Error("No valid SOA entry found, IGNORING WHOLE ZONE!")
			return
		}
	}
	for qtype, values := range dn.values {
		if qtype == "SOA" {
			continue
		}
		for id, value := range values {
			rrParams := rrParams{
				qtype:   qtype,
				id:      id,
				version: value.version,
				data:    dn,
				//logger:  log.data(), // TODO remove?
			}
			processValuesEntry(&rrParams, &value)
		}
	}
	for _, child := range dn.children {
		child.processValues()
	}
}

func processValuesEntry(rrParams *rrParams, value *valueType) {
	// TODO move this to processValues()?
	switch content := value.content.(type) {
	case stringValueType:
		processEntryTTL(rrParams, value)
		rrParams.data.log("content", content).Tracef("found plain string value for %s", rrParams.Target())
		// TODO if possible: parse and handle like an objectValueType
		rrParams.SetContent(string(content), nil)
		return
	case objectValueType:
		rrParams.values = objectType[any](content)
		rrParams.lastFieldValue = nil
	case lastFieldValueType:
		rrParams.values = objectType[any]{}
		rrParams.lastFieldValue = &value.content
	}
	processEntryTTL(rrParams, value)
	rrFunc := rr2func[rrParams.qtype]
	if rrFunc == nil {
		rrParams.data.log("entry", value.key).Errorf("record type %q is not object-supported", rrParams.qtype)
		return
	}
	rrFunc(rrParams)
}

func processEntryTTL(rrParams *rrParams, value *valueType) {
	ttl, vPath, err := getDuration("ttl", rrParams)
	if vPath == nil || err != nil {
		rrParams.data.log("vp", vPath, "error", err).Errorf("failed to get TTL for entry %q, ignoring", value.key)
	}
	rrParams.ttl = ttl
}
