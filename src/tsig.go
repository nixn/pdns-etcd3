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

// parseTSIGValue splits a TSIG entry value of the form "<algorithm> <base64-secret>"
// into its two fields. The base64 secret is returned verbatim (PowerDNS expects it
// base64-encoded in the "content" field).
func parseTSIGValue(raw []byte) (algorithm, secret string, err error) {
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return "", "", fmt.Errorf("TSIG value must be '<algorithm> <base64-secret>'")
	}
	return fields[0], fields[1], nil
}

// getTSIGKey reads a single TSIG key by name from etcd (<prefix>-tsig-/<name>) and
// returns it in the PowerDNS remote-backend shape, or false if the key is unknown.
func (cr *pdnsClientRequest) getTSIGKey() (any, error) {
	name := cr.Request.Parameters["name"].(string)
	key := *args.Prefix + tsigKey + keySeparator + name
	resp, err := cli.Get(key, false, nil, *args.DialTimeout)
	if err != nil {
		return false, fmt.Errorf("etcd get failed: %s", err)
	}
	item, ok := <-resp.DataChan
	if !ok {
		return false, nil // unknown key
	}
	algo, secret, perr := parseTSIGValue(item.Value)
	if perr != nil {
		return false, perr
	}
	return objectType[any]{"name": name, "algorithm": algo, "content": secret}, nil
}

// getTSIGKeys reads all TSIG keys under <prefix>-tsig-/ from etcd and returns them in
// the PowerDNS remote-backend shape. Malformed entries are logged and skipped.
func (cr *pdnsClientRequest) getTSIGKeys() (any, error) {
	prefix := *args.Prefix + tsigKey + keySeparator
	resp, err := cli.Get(prefix, true, nil, *args.DialTimeout)
	if err != nil {
		return false, fmt.Errorf("etcd get failed: %s", err)
	}
	//goland:noinspection GoPreferNilSlice
	keys := []objectType[any]{}
	for item := range resp.DataChan {
		name := strings.TrimPrefix(item.Key, prefix)
		algo, secret, perr := parseTSIGValue(item.Value)
		if perr != nil {
			cr.Errorf("data")("skipping malformed TSIG key %q: %s", name, perr)()
			continue
		}
		keys = append(keys, objectType[any]{"name": name, "algorithm": algo, "content": secret})
	}
	return keys, nil
}
