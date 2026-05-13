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
	"strings"
)

func getDomainMetadata(params objectType[any], client *pdnsClient) ([]string, error) {
	name := ParseDomainName(strings.ToLower(params["name"].(string)))
	client.log.main().Tracef("getDomainMetadata: RLocking up to %q", name.asKey(true))
	data, found := dataRoot.getChild(name, true)
	client.log.main().Tracef("getDomainMetadata: RLocked %q", data.getQname())
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
	client.log.main().Tracef("getDomainMetadata: RLocked %q", data.getQname())
	defer data.rUnlockUpwards(nil, true)
	defer client.log.main().Tracef("getDomainMetadata: RUnlocking %q", data.getQname())
	client.log.data(name).Tracef("search returned %q", data.getQname())
	if !found {
		client.log.data(name).Debug("no such domain")
		return map[string][]string{}, nil
	}
	return data.metadata, nil
}
