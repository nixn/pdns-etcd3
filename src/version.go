/* Copyright 2016-2020 nix <https://keybase.io/nixn>

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
	"regexp"
	"strconv"
	"strings"
)

type versionType struct {
	isDevelopment bool
	major, minor  uint64
}

func (version *versionType) String() string {
	var prefix string
	if version.isDevelopment {
		prefix = "0."
	}
	return fmt.Sprintf("%s%d.%d", prefix, version.major, version.minor)
}

func (version *versionType) IsCompatibleTo(otherVersion *versionType) bool {
	if version.isDevelopment != otherVersion.isDevelopment {
		return false
	}
	if version.major != otherVersion.major {
		return false
	}
	if version.minor < otherVersion.minor {
		return false
	}
	return true
}

func parseEntryVersion(string string) (*versionType, error) {
	version := versionType{}
	developmentPrefix := "0."
	if strings.HasPrefix(string, developmentPrefix) {
		version.isDevelopment = true
		string = string[len(developmentPrefix):]
	}
	if parts := regexp.MustCompile("^([0-9]+)(?:\\.([0-9]+))?$").FindStringSubmatch(string); parts != nil {
		var err error
		version.major, err = strconv.ParseUint(parts[1], 10, 8)
		if err != nil {
			return nil, fmt.Errorf("failed to parse major: %s", err)
		}
		if len(parts) == 3 {
			version.minor, err = strconv.ParseUint(parts[2], 10, 8)
			if err != nil {
				return nil, fmt.Errorf("failed to parse minor: %s", err)
			}
		}
		return &version, nil
	}
	return nil, fmt.Errorf("invalid version string")
}
