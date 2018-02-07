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

import "strings"

type nameType []string // in storage form (so take care of reversedNames)

func (name *nameType) String() string {
	return name.normal()
}

func (name *nameType) len() int {
	return len(*name)
}

func (name *nameType) parts() []string {
	return *name
}

func (name *nameType) part(level int) string {
	if level == 0 {
		return ""
	}
	i := level - 1
	if !reversedNames {
		i = name.len() - level
	}
	return name.parts()[i]
}

// get the domain in normal form (with trailing dot)
func (name *nameType) normal() string {
	parts := name.parts()
	if reversedNames {
		parts = reversed(parts)
	}
	return strings.Join(parts, ".") + "."
}

// get the domain in storage form
func (name *nameType) key(level int, withTrailingDot bool) string {
	if level == 0 {
		return "."
	}
	end := level
	if !reversedNames {
		end = name.len()
	}
	key := strings.Join(name.parts()[end-level:end], ".")
	if withTrailingDot {
		key += "."
	}
	return key
}
