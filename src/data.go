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

type dataNode struct {
	parent      *dataNode
	hasSOA      bool
	lname       string                // local name
	defaults    map[string]objectType // <QTYPE> or "" → values
	children    map[string]*dataNode  // key = <lname of subdomain>. if children[lname] == nil, the subdomain is present, but the data is not loaded (would be a subzone)
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
