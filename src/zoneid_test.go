//go:build unit

package src

import "testing"

func TestDomainID(t *testing.T) {
	// deterministic: same name → same id. This is what makes setNotified work in pipe mode,
	// where getUpdatedMasters and setNotified may run in different processes.
	first := domainID("example.net.")
	if again := domainID("example.net."); again != first {
		Errorf(t, "domainID is not deterministic: %d vs %d", first, again)
	}
	// distinct names → distinct ids
	if domainID("example.net.") == domainID("example.org.") {
		Errorf(t, "domainID collided for distinct zones")
	}
	// always a non-negative int (PowerDNS domain_id)
	if domainID("example.net.") < 0 {
		Errorf(t, "domainID must be non-negative, got %d", domainID("example.net."))
	}
}

func TestFilterUpdated(t *testing.T) {
	domains := []domainInfo{
		{ID: 1, Zone: "a.", Serial: 10, Kind: kindMaster}, // notified==serial → NOT updated
		{ID: 2, Zone: "b.", Serial: 20, Kind: kindMaster}, // notified!=serial → updated
		{ID: 3, Zone: "c.", Serial: 30, Kind: kindMaster}, // no notified entry (0) → updated
	}
	notified := map[int64]uint32{1: 10, 2: 15}

	got := filterUpdated(domains, notified)
	if len(got) != 2 {
		Fatalf(t, "want 2 updated, got %d: %v", len(got), got)
	}
	ids := map[int64]bool{}
	for _, d := range got {
		ids[d.ID] = true
		if d.ID == 2 && d.NotifiedSerial != 15 {
			Errorf(t, "zone 2 NotifiedSerial = %d, want 15", d.NotifiedSerial)
		}
	}
	if ids[1] {
		Errorf(t, "zone 1 (serial==notified) must not be reported as updated")
	}
	if !ids[2] || !ids[3] {
		Errorf(t, "zones 2 and 3 must be reported as updated")
	}
}
