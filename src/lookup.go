/* Copyright 2016-2025 nix <https://keybase.io/nixn>

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
	"strings"
)

type queryType struct {
	name  nameType // normalized (lowercased)
	qtype string
}

func (query *queryType) String() string {
	return fmt.Sprintf("%s%s%s", query.name.normal(), keySeparator, query.qtype)
}

// TODO CNAME and DNAME also single value records?

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

func lookup(params objectType[any], client *pdnsClient) (interface{}, error) {
	qname := params["qname"].(string) // RFC 1035 2.3.3: remember original qname and use it later in the result
	query := queryType{
		name:  nameType(Map(reversed(splitDomainName(strings.ToLower(qname), ".")), func(name string, _ int) namePart { return namePart{name, ""} })), // the keyPrefix from query.name will not be used, so it could be anything
		qtype: params["qtype"].(string),
	}
	data := dataRoot.getChild(query.name, true)
	defer data.rUnlockUpwards(nil)
	if data.depth() < query.name.len() {
		client.log.data().Tracef("search for %q returned %q", query.name.normal(), data.getQname())
		client.log.data().Debugf("no such domain: %q", query.name.normal())
		return false, nil // need to return false to cause NXDOMAIN, returning an empty array causes PDNS error: "Backend reported condition which prevented lookup (Exception caught when receiving: No 'result' field in response from remote process) sending out servfail"
	}
	var result []objectType[any]
	records := map[string]map[string]recordType{}
	if query.qtype == "ANY" {
		records = data.records
	} else {
		records[query.qtype] = data.records[query.qtype]
	}
	for qtype, records := range records {
		for _, record := range records {
			item := makeResultItem(qname, qtype, data, &record, client)
			client.log.pdns().WithField("item", item).Trace("adding result item")
			result = append(result, item)
		}
	}
	client.log.pdns().WithField("#", len(result)).Debug("request result items count")
	if len(result) == 0 {
		return false, nil // see above for reasoning
	}
	return result, nil
}

func makeResultItem(qname, qtype string, data *dataNode, record *recordType, client *pdnsClient) objectType[any] {
	content := record.content
	if record.priority != nil {
		content = priorityRE.ReplaceAllStringFunc(content, func(placeholder string) string {
			if client.PdnsVersion == 3 {
				return ""
			}
			return fmt.Sprintf(priorityRE.FindStringSubmatch(placeholder)[1], *record.priority)
		})
	}
	zoneNode := data.findZone()
	result := objectType[any]{
		"qname":   qname,
		"qtype":   qtype,
		"content": content,
		"ttl":     seconds(record.ttl),
		"auth":    zoneNode != nil,
	}
	if record.priority != nil && client.PdnsVersion == 3 {
		result["priority"] = *record.priority
	}
	return result
}

type searchOrderElement struct {
	qtype, id string
}

type valuePath struct {
	data *dataNode
	soe  *searchOrderElement
}

func (vp *valuePath) String() string {
	return fmt.Sprintf("%s%s%s%s%s", vp.data.getQname(), keySeparator, vp.soe.qtype, idSeparator, vp.soe.id)
}

func searchOrder(qtype, id string) (order []searchOrderElement) {
	q := len(qtype) > 0
	i := len(id) > 0
	if q && i {
		order = append(order, searchOrderElement{qtype, id})
	}
	if i {
		order = append(order, searchOrderElement{"", id})
	}
	if q {
		order = append(order, searchOrderElement{qtype, ""})
	}
	order = append(order, searchOrderElement{"", ""})
	return
}

func findValue[T any](key, qtype, id string, data *dataNode, values func(*dataNode) map[string]map[string]defoptType, valuesArea string, notUpwards bool) (T, *valuePath, error) {
	queryPath := valuePath{data, &searchOrderElement{qtype, id}}
	var zeroValue T
	for dn := data; dn != nil; dn = dn.parent {
		values := values(dn)
		for _, soe := range searchOrder(qtype, id) {
			if values, ok := values[soe.qtype]; ok {
				if values, ok := values[soe.id]; ok {
					if value, ok := values.values[key]; ok {
						valuePath := valuePath{dn, &soe}
						if value, ok := value.(T); ok {
							logFrom(log.data(), "value", value, "area", valuesArea).Tracef("found value for %s:%s in %s", queryPath.String(), key, valuePath.String())
							return value, &valuePath, nil
						}
						logFrom(log.data(), "value", value, "area", valuesArea, "found-in", valuePath.String()).Tracef("invalid type of value for %s.%s: %T", queryPath.String(), key, value)
						return zeroValue, &valuePath, fmt.Errorf("invalid value type: %T", value)
					}
				}
			}
		}
		if notUpwards {
			break
		}
	}
	return zeroValue, nil, nil // not found (and no error)
}

func findValueOrDefault[V any](key string, values objectType[any], qtype, id string, data *dataNode) (V, *valuePath, error) {
	if value, ok := values[key]; ok {
		queryPath := valuePath{data, &searchOrderElement{qtype, id}}
		if value, ok := value.(V); ok {
			logFrom(log.data(), "value", value).Tracef("found value for %s:%s directly", queryPath.String(), key)
			return value, &queryPath, nil
		}
		logFrom(log.data(), "value", value).Tracef("invalid type of value for %s.%s: %T (found directly)", queryPath.String(), key, value)
		var zeroValue V
		return zeroValue, &queryPath, fmt.Errorf("invalid type: %T", value)
	}
	return findValue[V](key, qtype, id, data, func(dn *dataNode) map[string]map[string]defoptType { return dn.defaults }, "defaults", false)
}

func findOptionValue[V any](key, qtype, id string, data *dataNode, notUpwards bool) (V, *valuePath, error) {
	return findValue[V](key, qtype, id, data, func(dn *dataNode) map[string]map[string]defoptType { return dn.options }, "options", notUpwards)
}
