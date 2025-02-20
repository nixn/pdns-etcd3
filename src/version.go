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
	"regexp"
	"strconv"
	"strings"
)

const (
	developmentPrefix = "0."
)

var (
	versionRegex = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?$`)
)

// VersionType is the type for program and data version, resp.
type VersionType struct {
	IsDevelopment       bool
	Major, Minor, Patch uint64
}

func (v *VersionType) String() string {
	if v.IsDevelopment && v.Major == 0 && v.Minor == 0 && v.Patch == 0 {
		return "develop"
	}
	var vs string
	if v.IsDevelopment {
		vs = developmentPrefix
	}
	vs += fmt.Sprintf("%d.%d", v.Major, v.Minor)
	if v.Patch > 0 {
		vs += fmt.Sprintf(".%d", v.Patch)
	}
	return vs
}

func (v *VersionType) isCompatibleTo(otherVersion *VersionType) bool {
	if v.IsDevelopment == otherVersion.IsDevelopment && v.Major == otherVersion.Major && v.Minor >= otherVersion.Minor {
		return true
	}
	return false
}

func parseEntryVersion(string string) (*VersionType, error) {
	version := VersionType{}
	if strings.HasPrefix(string, developmentPrefix) {
		version.IsDevelopment = true
		string = string[len(developmentPrefix):]
	}
	if parts := versionRegex.FindStringSubmatch(string); parts != nil {
		var err error
		version.Major, err = strconv.ParseUint(parts[1], 10, 8)
		if err != nil {
			return nil, fmt.Errorf("failed to parse major: %s", err)
		}
		if len(parts) > 2 && len(parts[2]) > 0 {
			version.Minor, err = strconv.ParseUint(parts[2], 10, 8)
			if err != nil {
				return nil, fmt.Errorf("failed to parse minor: %s", err)
			}
		}
		return &version, nil
	}
	return nil, fmt.Errorf("invalid version string")
}
