//go:build unit

package src

import (
	"testing"
	"time"
)

// builds: apex (example.) with SOA + A; child "www" with A; child "child" that is a
// separate zone (has SOA) and must be EXCLUDED from the parent's walk.
func buildTestZone() *dataNode {
	rec := func(content string) map[string]recordType {
		return map[string]recordType{"": {content: content, ttl: time.Hour}}
	}
	// root the tree at an empty-lname node, matching how the real dataRoot is built
	// (newDataNode(nil, "", "", ...)); getName() relies on that terminator when
	// walking parents, so an apex with a nil parent and a non-empty lname would panic.
	root := newDataNode(nil, "", "", false)
	apex := newDataNode(root, "example", "", false)
	root.children["example"] = apex
	apex.records["SOA"] = map[string]recordType{"": {content: "ns1.example. hostmaster.example. 1 2 3 4 5", ttl: time.Hour}}
	apex.records["A"] = rec("192.0.2.1")
	www := newDataNode(apex, "www", ".", false)
	www.records["A"] = rec("192.0.2.2")
	apex.children["www"] = www
	deleg := newDataNode(apex, "child", ".", false)
	deleg.records["SOA"] = map[string]recordType{"": {content: "ns1.child.example. hostmaster.child.example. 1 2 3 4 5", ttl: time.Hour}}
	deleg.records["A"] = rec("192.0.2.9")
	apex.children["child"] = deleg
	return apex
}

func TestListNotOurZone(t *testing.T) {
	// dataRoot has no zones; list of anything returns false (refused), not an empty slice.
	savedRoot := dataRoot
	defer func() { dataRoot = savedRoot }()
	dataRoot = newDataNode(nil, "", "", false)
	cr := &pdnsClientRequest{Client: testClient(t), Request: &pdnsRequest{
		Method: "list", Parameters: objectType[any]{"zonename": "absent.example.", "domain_id": float64(-1)},
	}}
	res, err := cr.list()
	if err != nil {
		Errorf(t, "unexpected error: %s", err)
	}
	if res != false {
		Errorf(t, "want false for unknown zone, got %#v", res)
	}
}

func TestListServedZone(t *testing.T) {
	// dataRoot holds the example. zone; list returns its full record set (not false).
	savedRoot := dataRoot
	defer func() { dataRoot = savedRoot }()
	apex := buildTestZone()
	dataRoot = apex.parent // the empty-lname root buildTestZone rooted the tree at
	cr := &pdnsClientRequest{Client: testClient(t), Request: &pdnsRequest{
		Method: "list", Parameters: objectType[any]{"zonename": "example.", "domain_id": float64(0)},
	}}
	res, err := cr.list()
	if err != nil {
		Errorf(t, "unexpected error: %s", err)
	}
	items, ok := res.([]objectType[any])
	if !ok {
		Fatalf(t, "want []objectType[any] for served zone, got %#v", res)
	}
	// apex SOA + apex A + www A = 3; the child sub-zone's records are excluded.
	if len(items) != 3 {
		Errorf(t, "got %d items, want 3: %v", len(items), items)
	}
}

func TestWalkZoneRecords(t *testing.T) {
	apex := buildTestZone()
	var result []objectType[any]
	apex.RLock(false)
	apex.walkZoneRecords(4, &result)
	apex.RUnlock(false)

	counts := map[string]int{}
	for _, item := range result {
		counts[item["qtype"].(string)]++
	}
	// apex SOA + apex A + www A = 3; the child zone's SOA/A are excluded.
	if len(result) != 3 {
		Errorf(t, "got %d items, want 3: %v", len(result), result)
	}
	if counts["SOA"] != 1 || counts["A"] != 2 {
		Errorf(t, "qtype counts wrong: %v", counts)
	}
}

func TestWalkZoneAuthFlags(t *testing.T) {
	root := newDataNode(nil, "", "", false)
	apex := newDataNode(root, "example", "", false)
	root.children["example"] = apex
	apex.records["SOA"] = map[string]recordType{"": {content: "ns1 host 1 2 3 4 5"}}
	apex.records["NS"] = map[string]recordType{"": {content: "ns1.example."}} // apex NS → auth
	deleg := newDataNode(apex, "sub", ".", false)
	deleg.records["NS"] = map[string]recordType{"1": {content: "ns1.sub.example."}} // delegation NS (non-empty id) → non-auth
	deleg.records["A"] = map[string]recordType{"": {content: "192.0.2.50"}}         // glue → non-auth
	deleg.records["DS"] = map[string]recordType{"": {content: "12345 8 2 abcd"}}    // DS at delegation → stays auth
	apex.children["sub"] = deleg
	below := newDataNode(deleg, "host", ".", false)
	below.records["A"] = map[string]recordType{"": {content: "192.0.2.51"}} // below delegation → non-auth
	deleg.children["host"] = below

	var result []objectType[any]
	apex.RLock(false)
	apex.walkZoneRecords(4, &result)
	apex.RUnlock(false)

	authByContent := map[string]bool{}
	for _, it := range result {
		authByContent[it["content"].(string)] = it["auth"].(bool)
	}
	if authByContent["ns1.example."] != true {
		Errorf(t, "apex NS must be auth")
	}
	if authByContent["ns1.sub.example."] != false {
		Errorf(t, "delegation NS must be non-auth")
	}
	if authByContent["192.0.2.50"] != false {
		Errorf(t, "glue A must be non-auth")
	}
	if authByContent["12345 8 2 abcd"] != true {
		Errorf(t, "DS at delegation must stay auth")
	}
	if authByContent["192.0.2.51"] != false {
		Errorf(t, "record below delegation must be non-auth")
	}
}
