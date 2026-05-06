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

type namePart struct {
	name      string
	keyPrefix string
}

type Name []namePart // in reversed form (storage form)

func (name Name) String() string {
	return name.normal()
}

func (name Name) len() int {
	return len(name)
}

func (name Name) lname(depth int) string {
	if depth == 0 {
		return ""
	}
	return name[depth-1].name
}

func (name Name) keyPrefix(depth int) string {
	if depth == 0 {
		return ""
	}
	return name[depth-1].keyPrefix
}

func (name Name) fromDepth(depth int) Name {
	if depth == 0 {
		return name
	}
	parts := make([]namePart, 0, name.len())
	for ; depth <= name.len(); depth++ {
		parts = append(parts, name[depth-1])
	}
	return parts
}

// get the domain in normal form (with trailing dot)
func (name Name) normal() string {
	if name.len() == 0 {
		return "."
	}
	ret := ""
	for depth := name.len(); depth > 0; depth-- {
		ret += name.lname(depth) + "."
	}
	return ret
}

// get the domain in storage form
func (name Name) asKey(withTrailingKeySeparator bool) string {
	if name.len() == 0 {
		return ""
	}
	key := ""
	for depth := 1; depth <= name.len(); depth++ {
		key += name.keyPrefix(depth) + name.lname(depth)
	}
	if withTrailingKeySeparator {
		key += keySeparator
	}
	return key
}

func ParseDomainName(name string) Name {
	return Map(Reversed(splitDomainName(name, ".")), func(lname string, i int) namePart {
		if i == 0 {
			return namePart{lname, ""}
		}
		return namePart{lname, "."}
	})
}
