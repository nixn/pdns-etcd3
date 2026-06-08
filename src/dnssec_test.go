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

// TestFixedSerial: MetaFixedSerial metadata overrides zoneRev() in the SOA serial.
func TestFixedSerial(t *testing.T) {
	type meta = []string
	for i, spec := range []test[meta, int64]{
		// no metadata at all → zoneRev fallback (maxRev=42 set below)
		{nil, ve[int64]{v: 42}},
		// empty list → zoneRev fallback
		{meta{}, ve[int64]{v: 42}},
		// valid override
		{meta{"123456"}, ve[int64]{v: 123456}},
		// whitespace is trimmed
		{meta{"  2026010101  "}, ve[int64]{v: 2026010101}},
		// uint32 boundary still accepted
		{meta{"4294967295"}, ve[int64]{v: 4294967295}},
		// out of uint32 range → zoneRev fallback
		{meta{"4294967296"}, ve[int64]{v: 42}},
		// negative / unparseable → zoneRev fallback
		{meta{"-1"}, ve[int64]{v: 42}},
		{meta{"abc"}, ve[int64]{v: 42}},
		// extra entries are ignored — first one wins
		{meta{"7", "8", "9"}, ve[int64]{v: 7}},
	} {
		tf := func(_ *testing.T, in meta) (int64, error) {
			dn := newDataNode(nil, "", "TEST/", false)
			dn.maxRev = 42
			if in != nil {
				dn.metadata[MetaFixedSerial] = in
			}
			return soaSerial(dn), nil
		}
		checkRun(t, fmt.Sprintf("(%d)%v", i+1, spec.input), tf, spec.input, spec.expected, false)
	}
}

// TestSOAFixedSerialThroughProcessValues: the override actually lands in the served SOA content.
func TestSOAFixedSerialThroughProcessValues(t *testing.T) {
	RootLog.ChildLog("data").SetLevel(10)
	root := newDataNode(nil, "", "TEST/", false)
	zone := root.getChildCreate([]namePart{{"tld", ""}})
	zone.metadata[MetaFixedSerial] = []string{"2026051901"}
	soaValues := objectValueType{
		"primary": "ns",
		"mail":    "horst.master",
		"refresh": "1h",
		"retry":   "30m",
		"expire":  604800,
		"neg-ttl": "10m",
	}
	zone.processValuesEntry("SOA", "", &valueType{key: "SOA", content: soaValues})
	got, ok := zone.records["SOA"][""]
	if !ok {
		t.Fatalf("expected an SOA record, got none")
	}
	want := `ns.tld. horst\.master.tld. 2026051901 3600 1800 604800 600`
	if got.content != want {
		t.Errorf("SOA content mismatch:\n got: %q\nwant: %q", got.content, want)
	}
}
