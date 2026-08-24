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
	"hash/fnv"
	"strconv"
	"strings"
)

// domainID derives the PowerDNS domain_id for a zone deterministically from its canonical
// (lowercased, trailing-dot) name. It MUST be stable across processes: in pipe mode PowerDNS
// spawns a separate pe3 process per request thread, so getUpdatedMasters and setNotified can
// run in different processes and must agree on the id↔zone association. A 31-bit FNV-1a hash
// is used — collisions are astronomically unlikely for realistic zone counts, and a collision
// would at worst cause one spurious (harmless) NOTIFY.
func domainID(qname string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(qname))
	return int64(h.Sum32() & 0x7fffffff)
}

// The notified serial (the last serial PowerDNS told the secondaries about) is persisted in
// etcd under a GLOBAL pseudo-entry, keyed by domain id: <prefix>-notified-/<id>. Living outside
// any zone's prefix, it (a) never enters a zone's zoneRev()/serial — so writing it cannot create
// a NOTIFY feedback loop — and (b) is shared by every pe3 process, so automatic NOTIFY works in
// pipe mode too (not only standalone). It is read/written on demand and is skipped by reload and
// handleEvents (like the -tsig- keys). Keying by id (not name) means setNotified — which only
// receives the id — needs no reverse lookup.

func notifiedSerialKey(id int64) string {
	return *args.Prefix + notifiedKey + keySeparator + strconv.FormatInt(id, 10)
}

// getNotifiedSerial reads the notified serial for one domain id (0 if unset or on error).
func getNotifiedSerial(id int64) uint32 {
	resp, err := cli.Get(notifiedSerialKey(id), false, nil, *args.DialTimeout)
	if err != nil {
		RootLog.Errorf("etcd")(nil, "getNotifiedSerial: etcd get failed: %s", err)("id", id)
		return 0
	}
	for item := range resp.DataChan {
		return parseNotifiedSerial(item.Value)
	}
	return 0
}

// getAllNotifiedSerials reads every persisted notified serial, keyed by domain id (empty on error).
func getAllNotifiedSerials() map[int64]uint32 {
	out := map[int64]uint32{}
	prefix := *args.Prefix + notifiedKey + keySeparator
	resp, err := cli.Get(prefix, true, nil, *args.DialTimeout)
	if err != nil {
		RootLog.Errorf("etcd")(nil, "getAllNotifiedSerials: etcd get failed: %s", err)()
		return out
	}
	for item := range resp.DataChan {
		if id, err := strconv.ParseInt(strings.TrimPrefix(item.Key, prefix), 10, 64); err == nil {
			out[id] = parseNotifiedSerial(item.Value)
		}
	}
	return out
}

// putNotifiedSerial persists the notified serial for a domain id.
func putNotifiedSerial(id int64, serial uint32) error {
	_, err := cli.Put(notifiedSerialKey(id), strconv.FormatUint(uint64(serial), 10), *args.DialTimeout)
	return err
}

func parseNotifiedSerial(v []byte) uint32 {
	n, err := strconv.ParseUint(strings.TrimSpace(string(v)), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// filterUpdated returns the zones whose current serial differs from the serial PowerDNS last
// notified secondaries about (so PowerDNS will send NOTIFY for them), filling in NotifiedSerial.
func filterUpdated(domains []domainInfo, notified map[int64]uint32) []domainInfo {
	//goland:noinspection GoPreferNilSlice
	updated := []domainInfo{}
	for _, d := range domains {
		ns := notified[d.ID]
		if uint32(d.Serial) != ns {
			d.NotifiedSerial = int64(ns)
			updated = append(updated, d)
		}
	}
	return updated
}
