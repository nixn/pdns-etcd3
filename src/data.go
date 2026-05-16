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
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/titanous/json5"
	"go.yaml.in/yaml/v4"
)

var (
	// update this when changing data structure (only major/minor, patch is always 0). also change it in docs!
	dataVersion = VersionType{IsDevelopment: true, Major: 2, Minor: 0}
)

type recordType struct {
	content  string
	priority *uint16       // only used when pdnsVersion == 3
	ttl      time.Duration // TODO make TTL an option, not a value
}

type objectValueType objectType[any]
type stringValueType struct {
	s         string
	noParsing bool
}
type lastFieldValueType any

type valueType struct {
	key     string // the exact ETCD key
	content any
	version *VersionType
}

// TODO store defaults, options and values only while reloading a zone, for answering PDNS we only need records and metadata
type dataNode struct {
	mutex   RWMutexCounted
	readers *struct {
		cur, max atomic.Int32
	}
	parent    *dataNode
	lname     string // local name
	keyPrefix string
	defaults  map[string]map[string]valueType  // <QTYPE> or "" → (<id> → value)
	options   map[string]map[string]valueType  // <QTYPE> or "" → (<id> → value)
	values    map[string]map[string]valueType  // <QTYPE> or "" → (<id> → value)
	records   map[string]map[string]recordType // <QTYPE> → (<id> → record) // processed
	metadata  map[string][]string
	children  map[string]*dataNode // key = <lname of subdomain>
	maxRev    int64                // the maximum of Rev of all ETCD items
}

func newDataNode(parent *dataNode, lname, keyPrefix string, trackReaders bool) *dataNode {
	dn := &dataNode{
		mutex:     RWMutexCounted{},
		parent:    parent,
		lname:     lname,
		keyPrefix: keyPrefix,
		defaults:  map[string]map[string]valueType{},
		options:   map[string]map[string]valueType{},
		values:    map[string]map[string]valueType{},
		records:   map[string]map[string]recordType{},
		metadata:  map[string][]string{},
		children:  map[string]*dataNode{},
		maxRev:    0,
	}
	if trackReaders {
		dn.readers = &struct{ cur, max atomic.Int32 }{}
	}
	return dn
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
		dn.Fatalf("values")("requested values for unknown entrytype")(entryType)
		return nil
	}
}

func (dn *dataNode) getQname() string {
	qname := dn.lname + "."
	for dn := dn.parent; dn != nil && dn.lname != ""; dn = dn.parent {
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
	_, ok := dn.records["SOA"][""]
	return ok
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

func (dn *dataNode) Logf(level int, component ...string) func(string, ...any) func(...any) {
	component = PrependT(component, "data")
	return func(format string, args ...any) func(...any) {
		return func(fields ...any) {
			fields = Prepend(fields, "dn", dn.getQname)
			RootLog.Logf(level, component...)(nil, format, args...)(fields...)
		}
	}
}

func (dn *dataNode) Fatalf(component ...string) func(string, ...any) func(...any) {
	return dn.Logf(FatalLevel, component...)
}

func (dn *dataNode) Errorf(component ...string) func(string, ...any) func(...any) {
	return dn.Logf(ErrorLevel, component...)
}

func (dn *dataNode) getName() Name {
	var parts []namePart
	for dn := dn; dn.lname != ""; dn = dn.parent {
		parts = append(parts, namePart{dn.lname, dn.keyPrefix})
	}
	return Reversed(parts)
}

// this method is only called from reload(), which itself is called under writer lock, so no locking needed here
func (dn *dataNode) getChildCreate(name Name) *dataNode {
	if name.len() == 0 {
		return dn
	}
	childLName := name.lname(1)
	lChild, ok := dn.children[childLName]
	if !ok || lChild == nil {
		lChild = newDataNode(dn, childLName, name.keyPrefix(1), dn.readers != nil)
		dn.children[childLName] = lChild
	}
	return lChild.getChildCreate(name.fromDepth(2))
}

func (dn *dataNode) RLock(countReader bool) {
	dn.mutex.RLock()
	if dn.readers != nil && countReader {
		cur := dn.readers.cur.Add(1)
		dn.readers.max.CompareAndSwap(cur-1, cur)
	}
}

func (dn *dataNode) RUnlock(countReader bool) {
	if dn.readers != nil && countReader {
		dn.readers.cur.Add(-1)
	}
	dn.mutex.RUnlock()
}

func (dn *dataNode) getChild(name Name, countReader bool) (*dataNode, bool) {
	dn.RLock(countReader)
	if name.len() == 0 {
		return dn, true
	}
	childLName := name.lname(1)
	lChild, ok := dn.children[childLName]
	if !ok || lChild == nil {
		return dn, false
	}
	return lChild.getChild(name.fromDepth(2), countReader)
}

// subdomainDepth returns a positive int (the sublevel), if the receiver is a subdomain of 'ancestor', 0 when both are the same domain, -1 otherwise ('ancestor' is not an ancestor)
func (dn *dataNode) subdomainDepth(ancestor *dataNode) int {
	for dn, n := dn, 0; dn != nil; dn, n = dn.parent, n+1 {
		if dn == ancestor {
			return n
		}
	}
	return -1
}

func (dn *dataNode) rUnlockUpwards(stopAt *dataNode, countReader bool) {
	for dn := dn; dn != stopAt; dn = dn.parent {
		dn.RUnlock(countReader)
	}
}

func (dn *dataNode) LockCounts() []int32 {
	if dn.parent == nil {
		return []int32{dn.mutex.Count()}
	}
	return append(dn.parent.LockCounts(), dn.mutex.Count())
}

func (dn *dataNode) zoneRev() int64 {
	// TODO for +auto-ptr and potentially +collect: maintain a list of dependent zones (up- and downwards) and take the highest revision as result (for all of them)
	rev := dn.maxRev
	for _, dn := range dn.children {
		if dn.hasSOA() {
			continue
		}
		rev = max(rev, dn.zoneRev())
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
		dn.Logf(3)("allDomains: found zone %q", zone)("serial", serial)
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

func parseEntryKey(key string) (name Name, entryType entryType, qtype, id string, version *VersionType, err error) {
	key = strings.TrimPrefix(key, *args.Prefix)
	var temp string
	// version
	key, temp = cutKey(key, versionSeparator)
	if temp != "" {
		version, err = parseEntryVersion(temp) // TODO do not allow a patch version
		if err != nil {
			err = fmt.Errorf("failed to parse version: %s", err)
			return
		}
	}
	// name
	temp = ""
	for m := nameRegex.FindStringSubmatch(key); m != nil; m = nameRegex.FindStringSubmatch(key) {
		name = append(name, namePart{m[1], temp})
		temp = m[2]
		key = key[len(m[0]):]
	}
	if len(name) > 0 && temp != keySeparator {
		err = fmt.Errorf("a non-empty name must end with the key separator %q", keySeparator)
		return
	}
	if m := entryRegex.FindStringSubmatch(key); m != nil {
		if et, ok := key2entryType[m[1]]; !ok {
			err = fmt.Errorf("invalid entry type keyword %q", m[1])
			return
		} else {
			entryType = et
		}
		key = key[len(m[0]):]
	} else {
		entryType = normalEntry
	}
	// TODO use own types for each entry type
	switch entryType {
	case normalEntry, defaultsEntry, optionsEntry:
		if m := valsRegex.FindStringSubmatch(key); m != nil {
			qtype = m[1]
			id = m[2]
			if entryType == normalEntry {
				if qtype == "" {
					err = fmt.Errorf("empty qtype (name: %s)", name)
					return
				}
				if qtype == "SOA" && id != "" {
					err = fmt.Errorf("SOA entry cannot have an id (%q)", id)
					return
				}
				switch qtype {
				case "A", "AAAA", "ALIAS", "CNAME", "DNAME", "MX", "NS", "PTR", "SOA": // TODO add others, even not-supported ones?
					for _, lname := range name {
						if strings.IndexRune(lname.name, '_') >= 0 {
							err = fmt.Errorf("records for hostnames may not have underscores: %q", lname.name)
							return
						}
					}
				}
			}
			return
		}
	case metadataEntry:
		if m := metaRegex.FindStringSubmatch(key); m != nil {
			qtype = m[1]
			id = m[2]
			return
		}
	case lockEntry:
		id = key
		return
	default:
		err = fmt.Errorf("unhandled entry type: %q", entryType)
		return
	}
	err = fmt.Errorf("(%s) invalid key: %q", entryType, key)
	return
}

func parseEntryContent(value []byte, entryType entryType) (any, error) {
	l := len(value)
	if l == 0 {
		if entryType == normalEntry {
			return stringValueType{s: ""}, nil
		}
		return nil, fmt.Errorf("empty")
	}
	switch {
	case value[0] == '`':
		if entryType != normalEntry {
			return nil, fmt.Errorf("a non-normal entry must be an object")
		}
		return stringValueType{s: string(value[1:])}, nil
	case l >= 2 && slicePrefixed(value, '!', '`'):
		if entryType != normalEntry {
			return nil, fmt.Errorf("a non-normal entry must be an object")
		}
		return stringValueType{s: string(value[2:]), noParsing: true}, nil
	case value[0] == '=': // last-field-value syntax
		if entryType != normalEntry {
			return nil, fmt.Errorf("a non-normal entry must be an object")
		}
		var content any
		if err := json5.Unmarshal(value[1:], &content); err != nil {
			return nil, fmt.Errorf("failed to parse as JSON value: %s", err)
		}
		return lastFieldValueType(content), nil
	case value[0] == '{':
		var values objectValueType
		if err := json5.Unmarshal(value, &values); err != nil {
			return nil, fmt.Errorf("failed to parse as JSON object: %s", err)
		}
		return values, nil
	case l >= 4 && slicePrefixed(value, '-', '-', '-') && (value[3] == '\n' || value[3] == '\r'):
		var values objectValueType
		if err := yaml.Unmarshal(value, &values); err != nil {
			return nil, fmt.Errorf("failed to parse as YAML object: %s", err)
		}
		return values, nil
	}
	if entryType == normalEntry {
		return stringValueType{s: string(value)}, nil
	}
	return nil, fmt.Errorf("invalid")
}

func (dn *dataNode) reload(dataChan <-chan etcdItem) {
	since := time.Now()
	clearMap(dn.defaults)
	clearMap(dn.options)
	clearMap(dn.values)
	clearMap(dn.records)
	clearMap(dn.metadata)
	clearMap(dn.children)
	dn.Logf(1, "reload")("processing entry items from ETCD")()
	debug2 := dn.Logf(2, "reload", "collect")
	debug3 := dn.Logf(3, "reload", "collect")
	debug3values := dn.Logf(3, "values")
	depth := dn.depth()
ITEMS:
	for item := range dataChan {
		debug2("processing entry")(item.Key)
		name, entryType, qtype, id, itemVersion, err := parseEntryKey(item.Key)
		if name == nil {
			name = Name{}
		}
		debug3values("parsed %q into name %q, type %q, qtype %q, id %q, version %q, err: %s", item.Key, name.normal, entryType, qtype, id, itemVersion, err2str(err))()
		// check version first, because a higher version (than our current dataVersion) could change the key syntax (but not prefix and version suffix)
		if itemVersion != nil && !dataVersion.IsCompatibleTo(*itemVersion, false) {
			debug2("ignoring entry %q due to version incompatibility", item.Key)("my", dataVersion, "their", *itemVersion)
			continue ITEMS
		}
		if err != nil {
			dn.Errorf()("failed to parse entry key %q: %s", item.Key, err)()
			continue ITEMS
		}
		if entryType == lockEntry {
			debug3("ignoring lock entry")(item.Key)
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
		target := func() string { return targetString(itemData.getQname(), qtype, id) }
		switch entryType {
		case normalEntry, defaultsEntry, optionsEntry:
			vals := itemData.getValuesFor(entryType)
			// check version against a possibly already stored value, overwrite value only if it's a "better" version. compatibility is already cleared (above).
			if curr, ok := vals[qtype][id]; ok {
				if itemVersion == nil {
					debug3("ignoring (new) entry %q, because it is unversioned and cannot replace the current entry", item.Key)("current", curr.key)
					continue ITEMS
				}
				if curr.version != nil && itemVersion.Minor <= curr.version.Minor {
					debug3("ignoring (new) entry %q, because its' version's minor is not greater than the current entry's version's minor", item.Key)("current", curr.key)
					continue ITEMS
				}
				debug3("overriding existing entry %q due to version constraints", target)("new-entry", item.Key, "current-version", curr.version, "new-version", itemVersion)
			}
			// handle content
			content, err := parseEntryContent(item.Value, entryType)
			if err != nil {
				dn.Errorf()("failed to parse content of %q: %s", item.Key, err)()
				continue ITEMS
			}
			if _, ok := vals[qtype]; !ok {
				vals[qtype] = map[string]valueType{}
			}
			vals[qtype][id] = valueType{item.Key, content, itemVersion}
			debug3values("stored %v for %s", entryType, target)(content)
		case metadataEntry:
			value := string(item.Value)
			dn.metadata[qtype] = append(dn.metadata[qtype], value)
			debug3values("stored %v for %s", entryType, target)(value)
		default:
			dn.Errorf()("unhandled entry type")(entryType)
			continue ITEMS
		}
		itemData.maxRev = max(itemData.maxRev, item.Rev())
	}
	dn.processValues()
	dur := time.Since(since)
	dn.Logf(2, "reload")("reload finished")("dur", dur)
}

func (dn *dataNode) processValues() {
	dn.Logf(2, "reload", "process")("processing values to records")()
	// process SOA first, to have proper zone appending for other entries
	if values, ok := dn.values["SOA"]; ok {
		valid := false
	IDS:
		for id, value := range values {
			if id != "" {
				dn.Errorf("reload", "process")("ignoring SOA entry %q: a SOA entry may not have a non-empty ID", value.key)("id", id)
				continue
			}
			dn.processValuesEntry("SOA", "", &value)
			if _, valid = dn.records["SOA"][""]; valid {
				break IDS
			}
		}
		if !valid {
			dn.Errorf("reload", "process")("no valid SOA entry found, IGNORING WHOLE ZONE!")()
			return
		}
	}
	for qtype, values := range dn.values {
		if qtype == "SOA" {
			continue
		}
		for id, value := range values {
			dn.processValuesEntry(qtype, id, &value)
		}
	}
	for _, child := range dn.children {
		child.processValues()
	}
}

func (dn *dataNode) processValuesEntry(qtype, id string, value *valueType) {
	if content, ok := value.content.(stringValueType); ok && !content.noParsing {
		if parse := parses[qtype]; parse != nil {
			values, err := parseContent(parse, content.s)
			if err != nil {
				dn.Errorf("values")("failed to parse plain string content for %s#%s, ignoring entry: %s", qtype, id, err)("content", content.s, "regexp", parse.re.String)
				return
			}
			dn.Logf(3, "values")("parsed plain string content for %s#%s into values", qtype, id)("content", content, "values", values)
			value.content = values
		}
	}
	params := &rrParams{qtype: qtype, id: id, data: dn}
	processEntryTTL(params, value)
	if content, ok := value.content.(stringValueType); ok {
		dn.Logf(3, "values")("found plain string value for %s", params.Target)(Supplier3(CutString, content.s, 25, "..."))
		params.SetContent(content.s, nil)
		return
	}
	rrFunc := rrFuncs[qtype]
	if rrFunc == nil {
		dn.Errorf("values")("record type %q is not object-supported", qtype)("entry", value.key)
		return
	}
	switch content := value.content.(type) {
	case objectValueType:
		params.values = objectType[any](content)
	case lastFieldValueType:
		params.values = objectType[any]{}
		params.lastFieldValue = &value.content
	}
	rrFunc(params)
}

func processEntryTTL(rrParams *rrParams, value *valueType) {
	ttl, vPath, err := getDuration("ttl", rrParams)
	if vPath == nil || err != nil {
		rrParams.data.Errorf("values")("failed to get TTL for entry %q, ignoring", value.key)("vp", vPath, "error", err)
	}
	rrParams.ttl = ttl
}
