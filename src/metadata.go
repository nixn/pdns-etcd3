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
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func getDomainInfo(params objectType[any], client *pdnsClient) (any, error) {
	name := ParseDomainName(strings.ToLower(params["name"].(string)))
	client.log.main().Tracef("getDomainInfo: RLocking up to %q", name.asKey(true))
	data, found := dataRoot.getChild(name, true)
	client.log.main(data.LockCounts()).Tracef("getDomainInfo: RLocked %q", data.prefixKey())
	defer data.rUnlockUpwards(nil, true)
	defer client.log.main(data.LockCounts()).Tracef("getDomainInfo: RUnlocking %q", data.prefixKey())
	client.log.data(name).Tracef("getDomainInfo: search returned %q", data.getQname())
	if !found {
		client.log.data(name).Debug("no such domain")
		return false, nil
	}
	if !data.hasSOA() {
		client.log.data(name).Debug("not a zone")
		return false, nil
	}
	return objectType[any]{
		"zone":   params["name"],
		"serial": data.zoneRev(),
	}, nil
}

func getDomainMetadata(params objectType[any], client *pdnsClient) ([]string, error) {
	name := ParseDomainName(strings.ToLower(params["name"].(string)))
	client.log.main().Tracef("getDomainMetadata: RLocking up to %q", name.asKey(true))
	data, found := dataRoot.getChild(name, true)
	client.log.main(data.LockCounts()).Tracef("getDomainMetadata: RLocked %q", data.getQname())
	defer data.rUnlockUpwards(nil, true)
	defer client.log.main().Tracef("getDomainMetadata: RUnlocking %q", data.getQname())
	client.log.data(name).Tracef("search returned %q", data.getQname())
	if !found {
		client.log.data(name).Debug("no such domain")
		return []string{}, nil
	}
	metadata := data.metadata[strings.ToUpper(params["kind"].(string))]
	if metadata == nil {
		metadata = []string{}
	}
	return metadata, nil
}

func getAllDomainMetadata(params objectType[any], client *pdnsClient) (map[string][]string, error) {
	name := ParseDomainName(strings.ToLower(params["name"].(string)))
	client.log.main().Tracef("getDomainMetadata: RLocking up to %q", name.asKey(true))
	data, found := dataRoot.getChild(name, true)
	client.log.main(data.LockCounts()).Tracef("getDomainMetadata: RLocked %q", data.getQname())
	defer data.rUnlockUpwards(nil, true)
	defer client.log.main().Tracef("getDomainMetadata: RUnlocking %q", data.getQname())
	client.log.data(name).Tracef("search returned %q", data.getQname())
	if !found {
		client.log.data(name).Debug("no such domain")
		return map[string][]string{}, nil
	}
	return data.metadata, nil
}

func setDomainMetadata(ctx context.Context, params objectType[any], client *pdnsClient) (bool, error) {
	// TODO possibly use optimistic locking
	var name Name
	var zoneRev int64
	timeout := 15 * time.Second
	txn, err := newTransaction("setDomainMetadata", params["name"].(string), client, func(data *dataNode) error {
		name = data.getName()
		zoneData := data.findZone()
		if zoneData == nil {
			return fmt.Errorf("not within a zone")
		}
		zoneRev = zoneData.zoneRev()
		return nil
	}, timeout)
	if err != nil {
		return false, fmt.Errorf("failed to create transaction: %s", err)
	}
	kind := strings.ToUpper(params["kind"].(string))
	values := params["value"].([]any)
	keyPrefix := metadataKey + keySeparator + kind + idSeparator
	txn.Del(keyPrefix, true)
	for i, value := range values {
		txn.Put(keyPrefix+strconv.FormatInt(int64(i+1), 10), value.(string))
	}
	if txn.PutsCount() == 0 {
		client.log.main("rev", zoneRev).Trace("setDomainMetadata: no puts, adding metadata put for new minimum serial")
		txn.Put(metadataKey+keySeparator+MetaMinimumSerial, strconv.FormatInt(zoneRev+1, 10))
	} else {
		txn.Del(metadataKey+keySeparator+MetaMinimumSerial, false)
	}
	client.log.main("puts", txn.puts, "dels", txn.dels).Debug("setDomainMetadata: committing transaction")
	rev, err := txn.Commit(timeout)
	if err != nil {
		return false, fmt.Errorf("transaction commit failed: %s", err)
	}
	client.log.main("rev", rev).Trace("setDomainMetadata successful, waiting for reload")
	waitForReload(ctx, "setDomainMetadata", name, rev)
	client.log.data("kind", kind, "values", values).Debug("setDomainMetadata finished")
	return true, nil
}
