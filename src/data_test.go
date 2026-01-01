//go:build unit

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
	"testing"
)

type parsedKey struct {
	name      nameType
	entryType entryType
	qtype, id string
	version   *VersionType
}
type pk = parsedKey

func (pk parsedKey) String() string {
	return fmt.Sprintf("name: %v, entry type: %q, qtype: %q, id: %q, version: %s", pk.name, pk.entryType, pk.qtype, pk.id, ptr2str(pk.version, "s"))
}

func TestParseEntryKey(t *testing.T) {
	tf := func(key string) (parsedKey, error) {
		name, entryType, qtype, id, itemVersion, err := parseEntryKey(key)
		return parsedKey{name, entryType, qtype, id, itemVersion}, err
	}
	prefix := ""
	args = programArgs{Prefix: &prefix}
	for i, spec := range []test[string, parsedKey]{
		{"", ve[pk]{e: "empty qtype"}},
		{"@0", ve[pk]{e: "empty qtype"}},
		{"-defaults-", ve[pk]{v: pk{nil, "defaults", "", "", nil}}},
		{"-defaults-@0", ve[pk]{v: pk{nil, "defaults", "", "", &VersionType{false, 0, 0, 0}}}},
		{"-options-", ve[pk]{v: pk{nil, "options", "", "", nil}}},
		{"ABC", ve[pk]{v: pk{nil, "normal", "ABC", "", nil}}},
		{"./ABC", ve[pk]{v: pk{nil, "normal", "ABC", "", nil}}},
		{"ABC@-1", ve[pk]{e: "invalid version"}},
		{"ABC##hb", ve[pk]{e: "empty qtype"}},
		{"ABC#@at", ve[pk]{e: "invalid version"}},
		{"ABC#/sl@1.2", ve[pk]{v: pk{nil, "normal", "ABC", "/sl", &VersionType{false, 1, 2, 0}}}},
		{"com.example/dept.fin/NS#1@2.3", ve[pk]{v: pk{[]namePart{{"com", ""}, {"example", "."}, {"dept", "/"}, {"fin", "."}}, "normal", "NS", "1", &VersionType{false, 2, 3, 0}}}},
		{"com.example/dept.fin/-defaults-/NS#1@2.3", ve[pk]{v: pk{[]namePart{{"com", ""}, {"example", "."}, {"dept", "/"}, {"fin", "."}}, "defaults", "NS", "1", &VersionType{false, 2, 3, 0}}}},
		{"SOA#id", ve[pk]{e: "SOA entry cannot have an id"}},
	} {
		check(t, fmt.Sprintf("(%d)%q", i+1, spec.input), tf, spec.input, spec.expected)
	}
}

type contentInput struct {
	content, entryType string
}
type ci = contentInput

func TestParseEntryContent(t *testing.T) {
	tf := func(in contentInput) (any, error) {
		return parseEntryContent([]byte(in.content), entryType(in.entryType))
	}
	prefix := ""
	args = programArgs{Prefix: &prefix}
	for i, spec := range []test[contentInput, any]{
		{ci{"", "normal"}, ve[any]{v: stringValueType("")}},
		{ci{"=0", "normal"}, ve[any]{v: lastFieldValueType(float64(0))}},
		{ci{"=[1]", "normal"}, ve[any]{v: lastFieldValueType([]any{float64(1)})}},
		{ci{"{a: 1}", "normal"}, ve[any]{e: "failed to parse as JSON"}},
		{ci{`{"a": 1}`, "normal"}, ve[any]{v: objectValueType{"a": float64(1)}}},
		{ci{"---\na: 1", "normal"}, ve[any]{v: stringValueType("---\na: 1")}},
	} {
		check(t, fmt.Sprintf("(%d)%q", i+1, spec.input), tf, spec.input, spec.expected)
	}
}
