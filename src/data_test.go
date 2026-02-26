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
	"time"

	"github.com/sirupsen/logrus"
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
	tf := func(_ *testing.T, key string) (parsedKey, error) {
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
		checkRun(t, fmt.Sprintf("(%d)%q", i+1, spec.input), tf, spec.input, spec.expected, false)
	}
}

type contentInput struct {
	content   string
	entryType entryType
}
type ci = contentInput

func TestParseEntryContent(t *testing.T) {
	tf := func(_ *testing.T, in contentInput) (any, error) {
		return parseEntryContent([]byte(in.content), entryType(in.entryType))
	}
	prefix := ""
	args = programArgs{Prefix: &prefix}
	for i, spec := range []test[contentInput, any]{
		{ci{"", normalEntry}, ve[any]{v: stringValueType{s: ""}}},
		{ci{"", defaultsEntry}, ve[any]{e: "empty"}},
		{ci{"", optionsEntry}, ve[any]{e: "empty"}},
		{ci{`plain`, normalEntry}, ve[any]{v: stringValueType{s: "plain"}}},
		{ci{`plain`, defaultsEntry}, ve[any]{e: "invalid"}},
		{ci{`plain`, optionsEntry}, ve[any]{e: "invalid"}},
		{ci{"=0", normalEntry}, ve[any]{v: lastFieldValueType(float64(0))}},
		{ci{"=0", defaultsEntry}, ve[any]{e: "must be an object"}},
		{ci{"=0", optionsEntry}, ve[any]{e: "must be an object"}},
		{ci{"=/*comment*/[1]", normalEntry}, ve[any]{v: lastFieldValueType([]any{float64(1)})}},
		{ci{"{a: 1}", normalEntry}, ve[any]{v: objectValueType{"a": float64(1)}}},
		{ci{`{"a": 1}`, normalEntry}, ve[any]{v: objectValueType{"a": float64(1)}}},
		{ci{`{not-valid: 1}`, normalEntry}, ve[any]{e: "failed to parse as JSON"}},
		{ci{"`", normalEntry}, ve[any]{v: stringValueType{s: ""}}},
		{ci{"`", defaultsEntry}, ve[any]{e: "must be an object"}},
		{ci{"`", optionsEntry}, ve[any]{e: "must be an object"}},
		{ci{"`{}", normalEntry}, ve[any]{v: stringValueType{s: "{}"}}},
		{ci{"`{}", defaultsEntry}, ve[any]{e: "must be an object"}},
		{ci{"`{}", optionsEntry}, ve[any]{e: "must be an object"}},
		{ci{"!`{}", normalEntry}, ve[any]{v: stringValueType{s: "{}", noParsing: true}}},
		{ci{"-", normalEntry}, ve[any]{v: stringValueType{s: "-"}}},
		{ci{"--", normalEntry}, ve[any]{v: stringValueType{s: "--"}}},
		{ci{"---", normalEntry}, ve[any]{v: stringValueType{s: "---"}}},
		{ci{"--- ", normalEntry}, ve[any]{v: stringValueType{s: "--- "}}},
		{ci{"---\n", normalEntry}, ve[any]{v: objectValueType{}}},
		{ci{"---\r", normalEntry}, ve[any]{v: objectValueType{}}},
		{ci{"---\ra: 1", normalEntry}, ve[any]{v: objectValueType{"a": 1}}},
		{ci{"---\r\n", normalEntry}, ve[any]{v: objectValueType{}}},
		{ci{"---\na: 1\nb: two", normalEntry}, ve[any]{v: objectValueType{"a": 1, "b": "two"}}},
		{ci{"---\na: 1\rb: two", defaultsEntry}, ve[any]{v: objectValueType{"a": 1, "b": "two"}}},
		{ci{"---\ra: 1\nb: two", optionsEntry}, ve[any]{v: objectValueType{"a": 1, "b": "two"}}},
		{ci{"---\r\na: 1\r\nb: two", normalEntry}, ve[any]{v: objectValueType{"a": 1, "b": "two"}}},
	} {
		checkRun(t, fmt.Sprintf("(%d)%q", i+1, spec.input), tf, spec.input, spec.expected, false)
	}
}

func ptr[V any](v V) *V {
	w := struct{ v V }{v}
	return &w.v
}

type mapMapOfValue[K1 comparable, K2 comparable, V any] struct {
	k1 K1
	k2 K2
	v  V
}
type qic = mapMapOfValue[string, string, any]
type qir = mapMapOfValue[string, string, recordType]

func mapMapOf[K1 comparable, K2 comparable, V any](values ...mapMapOfValue[K1, K2, V]) map[K1]map[K2]V {
	m1 := map[K1]map[K2]V{}
	for _, v := range values {
		m2 := m1[v.k1]
		if m2 == nil {
			m2 = map[K2]V{}
			m1[v.k1] = m2
		}
		m2[v.k2] = v.v
	}
	return m1
}

func TestProcessValues(t *testing.T) {
	log.logger("data").SetLevel(logrus.TraceLevel)
	checkRecordsFn := func(data *dataNode) func(*testing.T, map[string]map[string]any) (any, error) {
		return func(t *testing.T, values map[string]map[string]any) (any, error) {
			clearMap(data.records)
			for qtype, content := range values {
				for id, content := range content {
					data.processValuesEntry(qtype, id, &valueType{key: fmt.Sprintf("%s#%s", qtype, id), content: content})
				}
			}
			return data.records, nil
		}
	}
	root := newDataNode(nil, "", "TEST/", false)
	root.defaults = map[string]map[string]valueType{
		"":    {"": {content: objectValueType{"ttl": "1h"}}},
		"SOA": {"": {content: objectValueType{"refresh": "1h", "retry": "30m", "expire": 604800, "neg-ttl": "10m", "primary": "ns", "mail": "horst.master"}}},
		"MX":  {"": {content: objectValueType{"priority": 10}}},
		"SRV": {"": {content: objectValueType{"priority": 10, "weight": 1}}},
	}
	//root.processValues() // currently not needed
	zone := root.getChildCreate([]namePart{{"tld", ""}})
	zone.options = map[string]map[string]valueType{
		"A":    {"": {content: objectValueType{"ip-prefix": []int{192, 0, 2}}}},
		"AAAA": {"": {content: objectValueType{"ip-prefix": "2001:db8:"}}},
	}
	defaultTTL, _ := time.ParseDuration(root.defaults[""][""].content.(objectValueType)["ttl"].(string))
	in := func(vs ...mapMapOfValue[string, string, any]) map[string]map[string]any {
		return mapMapOf[string, string, any](vs...)
	}
	out := func(vs ...mapMapOfValue[string, string, recordType]) map[string]map[string]recordType {
		return mapMapOf[string, string, recordType](vs...)
	}
	conditions := map[string]Condition{
		`:\w*:\w*>ttl`: OtherDefault[time.Duration]{Value: defaultTTL},
	}
	t.Run("tld", func(t *testing.T) {
		for _, spec := range []struct {
			values   map[string]map[string]any
			expected map[string]map[string]recordType
		}{
			{in(qic{"SOA", "", stringValueType{s: `ns1.example.org. horst.master@example.org. _ 3601 1801 604801 601`}}),
				out(qir{"SOA", "", recordType{content: `ns1.example.org. horst\.master.example.org. 0 3601 1801 604801 601`}})},
			{in(qic{"SOA", "", stringValueType{s: `_ _ _ _ _ _ _`}}),
				out(qir{"SOA", "", recordType{content: `ns.tld. horst\.master.tld. 0 3600 1800 604800 600`}})},
			{in(qic{"SOA", "", stringValueType{s: `ns1.example.org. horst\.master.example.org. 2 3602 1802 604802 602`, noParsing: true}}),
				out(qir{"SOA", "", recordType{content: `ns1.example.org. horst\.master.example.org. 2 3602 1802 604802 602`}})},
		} {
			name := fmt.Sprintf("%v", spec.values)
			checkRun[map[string]map[string]any, any](t, name, checkRecordsFn(zone), spec.values, ve[any]{
				v: spec.expected,
				c: conditions,
			}, false)
		}
	})
	zone.values = map[string]map[string]valueType{
		"SOA": {"": {content: objectValueType{}}},
	}
	zone.processValues()
	t.Run("sub", func(t *testing.T) {
		subd := zone.getChildCreate([]namePart{{"sub", "."}})
		for _, spec := range []struct {
			values   map[string]map[string]any
			expected map[string]map[string]recordType
		}{
			{in(qic{"A", "", stringValueType{s: "_"}}), out()},
			{in(qic{"A", "", stringValueType{s: "1"}}), out(qir{"A", "", recordType{content: "192.0.2.1"}})},
			{in(qic{"A", "", stringValueType{s: "1.2"}}), out(qir{"A", "", recordType{content: "192.0.1.2"}})},
			{in(qic{"A", "", stringValueType{s: "1.2.3"}}), out(qir{"A", "", recordType{content: "192.1.2.3"}})},
			{in(qic{"A", "", stringValueType{s: "1.2.3.4"}}), out(qir{"A", "", recordType{content: "1.2.3.4"}})},
			{in(qic{"A", "", stringValueType{s: "1.2.3.4.5"}}), out()},
			{in(qic{"AAAA", "", stringValueType{s: "1:2"}}), out(qir{"AAAA", "", recordType{content: "2001:db8::1:2"}})},
			{in(qic{"AAAA", "", stringValueType{s: "1::2"}}), out(qir{"AAAA", "", recordType{content: "1::2"}})},
			{in(qic{"CNAME", "", stringValueType{s: "target."}}), out(qir{"CNAME", "", recordType{content: "target."}})},
			{in(qic{"CNAME", "", stringValueType{s: "target"}}), out(qir{"CNAME", "", recordType{content: "target.tld."}})},
			{in(qic{"MX", "", stringValueType{s: "20 target."}}), out(qir{"MX", "", recordType{content: "{priority:%d }target.", priority: ptr[uint16](20)}})},
			{in(qic{"MX", "", stringValueType{s: "_ target"}}), out(qir{"MX", "", recordType{content: "{priority:%d }target.tld.", priority: ptr[uint16](10)}})},
			{in(qic{"SRV", "", stringValueType{s: "20 5 88 target."}}), out(qir{"SRV", "", recordType{content: "{priority:%d }5 88 target.", priority: ptr[uint16](20)}})},
			{in(qic{"SRV", "", stringValueType{s: "_ _ 88 target"}}), out(qir{"SRV", "", recordType{content: "{priority:%d }1 88 target.tld.", priority: ptr[uint16](10)}})},
		} {
			name := fmt.Sprintf("%v", spec.values)
			checkRun[map[string]map[string]any, any](t, name, checkRecordsFn(subd), spec.values, ve[any]{
				v: spec.expected,
				c: conditions,
			}, false)
		}
	})
}
