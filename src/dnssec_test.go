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
	"testing"
)

// TestDNSSECPassthroughTypes verifies that the DNSSEC record types added for
// pre-signed mode (DNSKEY, RRSIG, NSEC, NSEC3, NSEC3PARAM, DS, CDS, CDNSKEY)
// are registered both as parseable plain-string types and as record builders.
func TestDNSSECPassthroughTypes(t *testing.T) {
	types := []string{"DNSKEY", "RRSIG", "NSEC", "NSEC3", "NSEC3PARAM", "DS", "CDS", "CDNSKEY"}
	for _, qt := range types {
		t.Run(qt, func(t *testing.T) {
			if _, ok := parses[qt]; !ok {
				t.Errorf("qtype %q missing from parses map", qt)
			}
			if _, ok := rrFuncs[qt]; !ok {
				t.Errorf("qtype %q missing from rrFuncs map", qt)
			}
		})
	}
}

// TestDNSSECPlainStringParsing verifies that real-world DNSSEC presentation-
// format strings round-trip through the plain-string parser unchanged.
func TestDNSSECPlainStringParsing(t *testing.T) {
	cases := []struct {
		qtype, content string
	}{
		{"DNSKEY", "257 3 13 mdsswUyr3DPW132mOi8V9xESWE8jTo0dxCjjnopKl+GqJxpVXckHAeF+KkxLbxILfDLUT0rAK9iUzy1L53eKGQ=="},
		{"RRSIG", "SOA 13 2 3600 20260601000000 20260501000000 12345 example.com. abcdef=="},
		{"NSEC", "next.example.com. A NS SOA MX RRSIG NSEC DNSKEY"},
		{"NSEC3", "1 0 100 ABCDEF AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA NS SOA MX RRSIG DNSKEY NSEC3PARAM"},
		{"NSEC3PARAM", "1 0 100 ABCDEF"},
		{"DS", "12345 13 2 ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890"},
		{"CDS", "12345 13 2 ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890"},
		{"CDNSKEY", "257 3 13 mdsswUyr3DPW132mOi8V9xESWE8jTo0dxCjjnopKl+GqJxpVXckHAeF+KkxLbxILfDLUT0rAK9iUzy1L53eKGQ=="},
	}
	for _, c := range cases {
		t.Run(c.qtype, func(t *testing.T) {
			parsed, err := parseContent(parses[c.qtype], c.content)
			if err != nil {
				t.Fatalf("parseContent failed: %v", err)
			}
			got, ok := parsed["content"].(string)
			if !ok {
				t.Fatalf("expected string content, got %T (%v)", parsed["content"], parsed["content"])
			}
			if got != c.content {
				t.Errorf("content not preserved: want %q, got %q", c.content, got)
			}
		})
	}
}

// TestHasDNSKEY exercises the per-node DNSKEY detector.
func TestHasDNSKEY(t *testing.T) {
	dn := newDataNode(nil, "", "TEST/", false)
	if dn.hasDNSKEY() {
		t.Error("empty node should not report DNSKEY")
	}
	dn.records["DNSKEY"] = map[string]recordType{"": {content: "257 3 13 ..."}}
	if !dn.hasDNSKEY() {
		t.Error("node with DNSKEY should report it")
	}
	delete(dn.records, "DNSKEY")
	if dn.hasDNSKEY() {
		t.Error("node after delete should not report DNSKEY")
	}
}

// TestHasDNSKEYForZone exercises the root walker used by collectMetadata.
func TestHasDNSKEYForZone(t *testing.T) {
	root := newDataNode(nil, "", "TEST/", false)
	zone := root.getChildCreate(nameType{{"com", "."}, {"example", "."}})
	zone.records["SOA"] = map[string]recordType{"": {content: "ns. hostmaster. 1 3600 600 86400 60"}}

	for _, name := range []string{"example.com", "example.com.", "Example.COM.", "EXAMPLE.com"} {
		t.Run("no_dnskey/"+name, func(t *testing.T) {
			if root.hasDNSKEYForZone(name) {
				t.Errorf("expected false for %q, got true", name)
			}
		})
	}

	zone.records["DNSKEY"] = map[string]recordType{
		"":  {content: "257 3 13 ksk..."},
		"1": {content: "256 3 13 zsk..."},
	}

	for _, name := range []string{"example.com", "example.com.", "Example.COM.", "EXAMPLE.com"} {
		t.Run("with_dnskey/"+name, func(t *testing.T) {
			if !root.hasDNSKEYForZone(name) {
				t.Errorf("expected true for %q, got false", name)
			}
		})
	}

	t.Run("nonexistent_zone", func(t *testing.T) {
		if root.hasDNSKEYForZone("missing.example.org.") {
			t.Error("expected false for missing zone")
		}
	})

	t.Run("empty_name", func(t *testing.T) {
		if root.hasDNSKEYForZone("") {
			t.Error("expected false for empty zone name")
		}
	})

	t.Run("root_dot", func(t *testing.T) {
		if root.hasDNSKEYForZone(".") {
			t.Error("expected false for root with no DNSKEY")
		}
	})
}

// TestCollectMetadataPresigned verifies the metadata produced for pdns auth.
func TestCollectMetadataPresigned(t *testing.T) {
	prevRoot := dataRoot
	defer func() { dataRoot = prevRoot }()
	dataRoot = newDataNode(nil, "", "TEST/", false)
	zone := dataRoot.getChildCreate(nameType{{"es", "."}, {"signed", "."}})
	zone.records["SOA"] = map[string]recordType{"": {content: "ns. hostmaster. 1 3600 600 86400 60"}}
	zone.records["DNSKEY"] = map[string]recordType{"": {content: "257 3 13 ..."}}

	unsignedZone := dataRoot.getChildCreate(nameType{{"es", "."}, {"unsigned", "."}})
	unsignedZone.records["SOA"] = map[string]recordType{"": {content: "ns. hostmaster. 1 3600 600 86400 60"}}

	t.Run("signed_zone_has_PRESIGNED", func(t *testing.T) {
		md := collectMetadata("signed.es.")
		v, ok := md["PRESIGNED"]
		if !ok {
			t.Fatalf("PRESIGNED missing from metadata: %#v", md)
		}
		if len(v) != 1 || v[0] != "1" {
			t.Errorf("PRESIGNED expected [\"1\"], got %v", v)
		}
	})

	t.Run("unsigned_zone_has_no_PRESIGNED", func(t *testing.T) {
		md := collectMetadata("unsigned.es.")
		if _, ok := md["PRESIGNED"]; ok {
			t.Errorf("PRESIGNED should not be set on unsigned zone, got %v", md)
		}
	})

	t.Run("missing_zone_has_no_metadata", func(t *testing.T) {
		md := collectMetadata("missing.es.")
		if len(md) != 0 {
			t.Errorf("expected empty metadata for missing zone, got %v", md)
		}
	})

	t.Run("empty_name_safe", func(t *testing.T) {
		md := collectMetadata("")
		if len(md) != 0 {
			t.Errorf("expected empty metadata for empty name, got %v", md)
		}
	})
}
