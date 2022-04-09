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

import (
	"fmt"
	"time"
)

type queryType struct {
	name  nameType
	qtype string
}

func (query *queryType) String() string {
	return fmt.Sprintf("%s%s%s", query.name.normal(), keySeparator, query.qtype)
}

func (query *queryType) isANY() bool { return query.qtype == "ANY" }
func (query *queryType) isSOA() bool { return query.qtype == "SOA" }

// TODO CNAME and DNAME also single value records?

type rrFunc func(values objectType, id string, data *dataNode, revision int64) (content string, meta objectType, err error)

type entryType string // enum

const (
	normalEntry   entryType = "normal"
	defaultsEntry entryType = "defaults"
	optionsEntry  entryType = "options"
)

var (
	key2entryType = map[string]entryType{
		defaultsKey: defaultsEntry,
		optionsKey:  optionsEntry,
	}
	entryType2key = map[entryType]string{
		defaultsEntry: defaultsKey,
		optionsEntry:  optionsKey,
	}
)

func lookup(params objectType) (interface{}, error) {
	query := queryType{
		name:  nameType(Map(reversed(splitDomainName(params["qname"].(string), ".")), func(name string, _ int) namePart { return namePart{name, ""} })), // the keyPrefix from query.name will not be used, so it could be anything
		qtype: params["qtype"].(string),
	}
	data := dataRoot.getChild(query.name, false)
	if data.depth() < query.name.len() {
		log.data.Tracef("search for %q returned %q", query.name.normal(), data.getQname())
		log.data.Debugf("no such domain: %q", query.name.normal())
		return false, nil // need to return false to cause NXDOMAIN, returning an empty array causes PDNS error: "Backend reported condition which prevented lookup (Exception caught when receiving: No 'result' field in response from remote process) sending out servfail"
	}
	var result []objectType
	records := map[string]map[string]recordType{}
	if query.isANY() {
		records = data.records
	} else {
		records[query.qtype] = data.records[query.qtype]
	}
	for qtype, records := range records {
		rrFunc := rr2func[qtype]
		for id, record := range records {
			var content string
			var meta objectType
			var err error
			// TODO read/handle TTL only here, not in rrFunc (as for plain string)
			switch record.value.(type) {
			case objectType:
				if rrFunc == nil {
					return false, fmt.Errorf("unsupported QTYPE %q, but have JSON data for it in %q%s%s%s%s", qtype, data.getQname(), keySeparator, qtype, idSeparator, id)
				}
				content, meta, err = rrFunc(record.value.(objectType), id, data, dataRevision)
				if err != nil {
					return false, fmt.Errorf("failed to get content and TTL for %s%s%s%s%s: %s", data.getQname(), keySeparator, qtype, idSeparator, id, err)
				}
			case string:
				// TODO error when records with 'priority' field or SOA (due to 'serial' field) are not of objectType
				content = record.value.(string)
				ttl, err := getDuration("ttl", nil, qtype, id, data)
				if err != nil {
					return false, fmt.Errorf("failed to get TTL for %s%s%s%s%s: %s", data.getQname(), keySeparator, qtype, keySeparator, id, err)
				}
				meta = objectType{
					"ttl": ttl,
				}
			default:
				log.data.WithField("record", record).Fatalf("invalid record type: %T", record)
			}
			log.data.Tracef("%s%s%s%s%s: %v → content: %v, meta: %v", data.getQname(), keySeparator, qtype, idSeparator, id, record, content, meta)
			result = append(result, makeResultItem(qtype, data, content, meta))
		}
	}
	if len(result) == 0 {
		return false, nil // see above for reasoning
	}
	return result, nil
}

func makeResultItem(qtype string, data *dataNode, content string, meta objectType) objectType {
	zoneNode := data.findZone()
	result := objectType{
		"qname":   data.getQname(),
		"qtype":   qtype,
		"content": content,
		"ttl":     seconds(meta["ttl"].(time.Duration)),
		"auth":    zoneNode != nil,
	}
	if priority, ok := meta["priority"]; ok {
		result["priority"] = priority
	}
	return result
}

func findValue(name string, values objectType, qtype, id string, data *dataNode) (interface{}, error) {
	if v, ok := values[name]; ok {
		return v, nil
	}
	for dn := data; dn != nil; dn = dn.parent {
		for _, path := range []struct{ qtype, id string }{{qtype, id}, {"", id}, {qtype, ""}, {"", ""}} {
			if defaults, ok := dn.defaults[path.qtype]; ok {
				if defaults, ok := defaults[path.id]; ok {
					if v, ok := defaults.values[name]; ok {
						log.data.Tracef("found default value for %s:%s (%v) in %s%s%s%s%s", data.getQname(), name, v, dn.getQname(), keySeparator, path.qtype, idSeparator, path.id)
						return v, nil
					}
				}
			}
		}
	}
	log.data.Debugf("default value not found for %s:%s", data.getQname(), name)
	return nil, fmt.Errorf("missing %q", name)
}
