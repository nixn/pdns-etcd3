/* Copyright 2016-2020 nix <https://keybase.io/nixn>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. */

package pdns_etcd3

import (
	"fmt"
	"time"
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
	parent   *dataNode
	lname    string                           // local name
	loaded   bool                             // defaults, options and records (beside SOA)
	defaults map[string]map[string]valuesType // <QTYPE> or "" → (<id> → values)
	options  map[string]map[string]valuesType // <QTYPE> or "" → (<id> → values)
	records  map[string]map[string]recordType // <QTYPE> → (<id> → record)
	children map[string]*dataNode             // key = <lname of subdomain>. if children[lname] == nil, the subdomain is present, but the data is not loaded (would be a subzone?)
}

func newDataNode(parent *dataNode, lname string) *dataNode {
	return &dataNode{
		parent:   parent,
		lname:    lname,
		defaults: map[string]map[string]valuesType{},
		options:  map[string]map[string]valuesType{},
		records:  map[string]map[string]recordType{},
		children: map[string]*dataNode{},
	}
}

func (dn *dataNode) String() string {
	return fmt.Sprintf("%q, loaded: %v, hasSOA: %v", dn.getQname(), dn.loaded, dn.hasSOA())
}

func (dn *dataNode) getQname() string {
	qname := dn.lname + "."
	first := true
	for dn := dn.parent; dn != nil; dn = dn.parent {
		if first {
			first = false
		} else {
			qname += "."
		}
		qname += dn.lname
	}
	return qname
}

func (dn *dataNode) hasSOA() bool {
	if records, ok := dn.records["SOA"]; ok {
		return len(records) > 0
	}
	return false
}

func (dn *dataNode) findUpwards(pred func(*dataNode) bool) *dataNode {
	for data := dn; data != nil; data = data.parent {
		if pred(data) {
			return data
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
	for dn.parent != nil {
		dn = dn.parent
	}
	return dn
}

func (dn *dataNode) isLoaded() bool {
	if zone := dn.findZone(); zone != nil {
		return zone.loaded
	}
	return dn.loaded
}

func (dn *dataNode) getName() *nameType {
	parts := []string(nil)
	for dn := dn; dn.lname != ""; dn = dn.parent {
		parts = append(parts, dn.lname)
	}
	if reversedNames {
		parts = reversed(parts)
	}
	name := nameType(parts)
	return &name
}

func (dn *dataNode) getChild(nameParts []string, create bool) (data *dataNode, depth int) {
	data = dn
	for _, lname := range nameParts {
		childData, ok := data.children[lname]
		if !ok || childData == nil {
			if create {
				childData = newDataNode(data, lname)
				data.children[lname] = childData
			} else {
				return
			}
		}
		data = childData
		depth++
	}
	return
}

type dataCacheType struct {
	rootData  *dataNode
	revision  int64
	expiresAt time.Time
}

func newDataCache(revision int64, expiresAt time.Time) *dataCacheType {
	return &dataCacheType{
		rootData:  newDataNode(nil, ""),
		revision:  revision,
		expiresAt: expiresAt,
	}
}
