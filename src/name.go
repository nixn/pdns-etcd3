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

type namePart struct {
	name      string
	keyPrefix string
}

type nameType []namePart // in reversed form (storage form)

func (name *nameType) String() string {
	return name.normal()
}

func (name *nameType) len() int {
	return len(*name)
}

func (name *nameType) name(depth int) string {
	if depth == 0 {
		return ""
	}
	return (*name)[depth-1].name
}

func (name *nameType) keyPrefix(depth int) string {
	if depth == 0 {
		return ""
	}
	return (*name)[depth-1].keyPrefix
}

func (name *nameType) fromDepth(depth int) nameType {
	if depth == 0 {
		return *name
	}
	var parts []namePart
	for ; depth <= name.len(); depth++ {
		parts = append(parts, (*name)[depth-1])
	}
	return nameType(parts)
}

// get the domain in normal form (with trailing dot)
func (name *nameType) normal() string {
	if name.len() == 0 {
		return "."
	}
	ret := ""
	for depth := name.len(); depth > 0; depth-- {
		ret += name.name(depth) + "."
	}
	return ret
}

// get the domain in storage form
func (name *nameType) asKey(withTrailingKeySeparator bool) string {
	if name.len() == 0 {
		return ""
	}
	key := ""
	for depth := 1; depth <= name.len(); depth++ {
		key += name.keyPrefix(depth) + name.name(depth)
	}
	if withTrailingKeySeparator {
		key += keySeparator
	}
	return key
}
