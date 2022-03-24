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

import "strings"

type nameType []string // in reversed form (storage form)

func (name *nameType) String() string {
	return name.normal()
}

func (name *nameType) len() int {
	return len(*name)
}

func (name *nameType) parts() []string {
	return *name
}

func (name *nameType) part(depth int) string {
	if depth == 0 {
		return ""
	}
	return name.parts()[depth-1]
}

// get the domain in normal form (with trailing dot)
func (name *nameType) normal() string {
	return strings.Join(reversed(name.parts()), ".") + "."
}

// get the domain in storage form
func (name *nameType) asKey(depth int, withTrailingSeparator bool) string {
	key := strings.Join((*name)[:depth], keySeparator)
	if withTrailingSeparator {
		key += keySeparator
	}
	return key
}
