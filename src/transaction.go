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
	"strings"
	"time"
)

func newTransaction(caller string, qname string, client *pdnsClient, dataGet func(*dataNode) error, timeout time.Duration) (*Transaction, error) {
	name := ParseDomainName(strings.ToLower(qname))
	txn, err := func() (*Transaction, error) {
		lockDebug := client.Logf(4, "data", "locking")
		lockDebug("%s: RLocking up to %q", caller, Supplier1(name.asKey, true))()
		data, found := dataRoot.getChild(name, true)
		defer func() {
			lockDebug("%s: RUnlocking %q", caller, data.prefixKey)(data.LockCounts)
			data.rUnlockUpwards(nil, true)
		}()
		lockDebug("%s: RLocked %q", caller, data.prefixKey)(data.LockCounts)
		client.Logf(2, "data")("%s: search result: %q", caller, data.getQname)(name.normal)
		if !found {
			return nil, fmt.Errorf("no such domain")
		}
		if err := dataGet(data); err != nil {
			return nil, err
		}
		return NewTransaction(*args.Prefix+Some(data.findZone(), dataRoot).prefixKey()+lockKey, *args.Prefix+data.prefixKey()), nil
	}()
	if err != nil {
		return nil, err
	}
	client.Logf(3, "main")("%s: starting new transaction", caller)("qname", qname, "timeout", timeout)
	if _, err := txn.Start(timeout); err != nil {
		return nil, fmt.Errorf("failed to start transaction: %s", err)
	}
	return txn, nil
}

func waitForReload(ctx context.Context, caller string, name Name, rev int64) {
	since := time.Now()
	for func() bool {
		data, found := dataRoot.getChild(name, true)
		defer data.rUnlockUpwards(nil, true)
		return found && data.zoneRev() < rev
	}() {
		select {
		case <-ctx.Done():
			after := time.Since(since)
			data, _ := dataRoot.getChild(name, true)
			//goland:noinspection GoDeferInLoop // this case breaks the loop, so that defer is not a problem
			defer data.rUnlockUpwards(nil, true)
			data.Logf(2)("%s: waitForReload was interrupted by context after %s", caller, after)("rev", rev, "zoneRev", data.zoneRev)
			return
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
