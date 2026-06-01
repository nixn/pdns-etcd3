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

func withRLock[R any](method string, client *pdnsClient, name Name, notFound R, body func(*dataNode) (R, error)) (R, error) {
	lockDebug := client.Logf(4, "data", "locking")
	lockDebug("%s: RLocking up to %q", method, Supplier1(name.asKey, true))()
	data, found := dataRoot.getChild(name, true)
	lockDebug("%s: RLocked %q", method, data.prefixKey)(data.LockCounts)
	defer data.rUnlockUpwards(nil, true)
	defer lockDebug("%s: RUnlocking %q", method, data.prefixKey)(data.LockCounts)
	client.Logf(2, "data")("%s: search returned %q", method, data.getQname)(name.normal)
	if !found {
		client.Logf(1, "data")("%s: no such domain", method)(name.normal)
		return notFound, nil
	}
	return body(data)
}

func (cr *pdnsClientRequest) getDomainInfo() (any, error) {
	name := ParseDomainName(strings.ToLower(cr.Request.Parameters["name"].(string)))
	return withRLock("getDomainInfo", cr.Client, name, false, func(data *dataNode) (any, error) {
		if !data.hasSOA() {
			cr.Logf(1, "data")("getDomainInfo: not a zone")(name.normal)
			return false, nil
		}
		return objectType[any]{
			"zone":   cr.Request.Parameters["name"],
			"serial": data.zoneRev(),
		}, nil
	})
}

func (cr *pdnsClientRequest) getDomainMetadata() ([]string, error) {
	name := ParseDomainName(strings.ToLower(cr.Request.Parameters["name"].(string)))
	return withRLock("getDomainMetadata", cr.Client, name, []string{}, func(data *dataNode) ([]string, error) {
		metadata := data.metadata[strings.ToUpper(cr.Request.Parameters["kind"].(string))]
		if metadata == nil {
			metadata = []string{}
		}
		return metadata, nil
	})
}

func (cr *pdnsClientRequest) getAllDomainMetadata() (map[string][]string, error) {
	name := ParseDomainName(strings.ToLower(cr.Request.Parameters["name"].(string)))
	return withRLock("getAllDomainMetadata", cr.Client, name, map[string][]string{}, func(data *dataNode) (map[string][]string, error) {
		return data.metadata, nil
	})
}

func (cr *pdnsClientRequest) setDomainMetadata(ctx context.Context) (bool, error) {
	// TODO possibly use optimistic locking
	var name Name
	var zoneRev int64
	timeout := 15 * time.Second
	txn, err := cr.Client.newTransaction("setDomainMetadata", cr.Request.Parameters["name"].(string), func(data *dataNode) error {
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
	kind := strings.ToUpper(cr.Request.Parameters["kind"].(string))
	values := cr.Request.Parameters["value"].([]any)
	keyPrefix := metadataKey + keySeparator + kind + idSeparator
	txn.Del(keyPrefix, true)
	for i, value := range values {
		txn.Put(keyPrefix+strconv.FormatInt(int64(i+1), 10), value.(string))
	}
	if txn.PutsCount() == 0 {
		cr.Logf(3, "main")("setDomainMetadata: no puts, adding metadata put for new minimum serial")("rev", zoneRev)
		txn.Put(metadataKey+keySeparator+MetaMinimumSerial, strconv.FormatInt(zoneRev+1, 10))
	} else {
		txn.Del(metadataKey+keySeparator+MetaMinimumSerial, false)
	}
	cr.Logf(2, "main")("setDomainMetadata: committing transaction")("puts", txn.puts, "dels", txn.dels)
	rev, err := txn.Commit(timeout)
	if err != nil {
		return false, fmt.Errorf("transaction commit failed: %s", err)
	}
	cr.Logf(2, "main")("setDomainMetadata successful, waiting for reload")("rev", rev)
	waitForReload(ctx, "setDomainMetadata", name, rev)
	cr.Logf(3, "main")("setDomainMetadata finished")("kind", kind, "values", values)
	return true, nil
}
