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
)

func lookup(params objectType[any], client *pdnsClient) (interface{}, error) {
	// RFC 1035 2.3.3: remember original qname and use it later in the result
	qname := nameType(Map(reversed(splitDomainName(params["qname"].(string), ".")), func(name string, _ int) namePart { return namePart{name, ""} }))
	query := queryType{
		name:  nameType(Map(qname, func(qnamePart namePart, _ int) namePart { return namePart{strings.ToLower(qnamePart.name), "."} })),
		qtype: params["qtype"].(string),
	}
	data, found := dataRoot.getChild(query.name, true)
	defer data.rUnlockUpwards(nil)
	if !found {
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
			client.log.pdns(item).Trace("adding result item")
			result = append(result, item)
		}
	}
	client.log.pdns("#", len(result)).Debug("request result items count")
	if len(result) == 0 {
		return false, nil // see above for reasoning
	}
	return result, nil
}

func makeResultItem(qname nameType, qtype string, data *dataNode, record *recordType, client *pdnsClient) objectType[any] {
	zoneNode := data.findZone()
	result := objectType[any]{
		"qname":   qname.normal(),
		"qtype":   qtype,
		"content": record.content,
		"ttl":     seconds(record.ttl),
		"auth":    zoneNode != nil,
	}
	if record.priority != nil {
		result["content"] = priorityRE.ReplaceAllStringFunc(result["content"].(string), func(placeholder string) string {
			if client.PdnsVersion == 3 {
				return ""
			}
			return fmt.Sprintf(priorityRE.FindStringSubmatch(placeholder)[1], *record.priority)
		})
		if client.PdnsVersion == 3 {
			result["priority"] = *record.priority
		}
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

func findValue[T any](key, qtype, id string, data *dataNode, valuesArea entryType, notUpwards bool) (T, *valuePath, error) {
	queryPath := valuePath{data, &searchOrderElement{qtype, id}}
	for dn := data; dn != nil; dn = dn.parent {
		values := dn.getValuesFor(valuesArea)
		for _, soe := range searchOrder(qtype, id) {
			if value, ok := values[soe.qtype][soe.id]; ok {
				if value, ok := value.content.(objectValueType)[key]; ok {
					valuePath := valuePath{dn, &soe}
					if value, ok := value.(T); ok {
						log.data("value", value, "area", valuesArea).Tracef("found value for %s:%s in %s", queryPath.String(), key, valuePath.String())
						return value, &valuePath, nil
					}
					log.data("value", value, "area", valuesArea, "found-in", valuePath.String()).Tracef("invalid type of value for %s.%s: %T", queryPath.String(), key, value) // TODO use warning level?
					var zero T
					return zero, &valuePath, fmt.Errorf("invalid value type: %T", value)
				}
			}
		}
		if notUpwards {
			break
		}
	}
	var zero T
	return zero, nil, nil // not found (and no error)
}

func findValueOrDefault[V any](key string, values objectType[any], qtype, id string, data *dataNode) (V, *valuePath, error) {
	if value, ok := values[key]; ok {
		queryPath := valuePath{data, &searchOrderElement{qtype, id}}
		if value, ok := value.(V); ok {
			log.data("value", value).Tracef("found value for %s:%s directly", queryPath.String(), key)
			return value, &queryPath, nil
		}
		log.data("value", value).Tracef("invalid type of value for %s.%s: %T (found directly)", queryPath.String(), key, value)
		var zeroValue V
		return zeroValue, &queryPath, fmt.Errorf("invalid type: %T", value)
	}
	return findValue[V](key, qtype, id, data, defaultsEntry, false)
}

// TODO remove, just call findValue() directly
func findOptionValue[V any](key, qtype, id string, data *dataNode, notUpwards bool) (V, *valuePath, error) {
	return findValue[V](key, qtype, id, data, optionsEntry, notUpwards)
}
