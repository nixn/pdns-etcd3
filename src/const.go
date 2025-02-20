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
	"os"
	"regexp"
	"time"
)

const (
	defaultPdnsVersion  = 4
	defaultEndpointIPv4 = "127.0.0.1:2379"
	defaultEndpointIPv6 = "[::1]:2379"
	defaultDialTimeout  = 2 * time.Second
	minimumDialTimeout  = 10 * time.Millisecond
)

const (
	pdnsVersionParam = "pdns-version"
	prefixParam      = "prefix"
	logParamPrefix   = "log-"
	configFileParam  = "config-file"
	endpointsParam   = "endpoints"
	dialTimeoutParam = "timeout"
)

const (
	defaultsKey      = "-defaults-"
	optionsKey       = "-options-"
	keySeparator     = "/"
	labelPrefix      = "+"
	idSeparator      = "#"
	versionSeparator = "@"
)

type ipMetaT map[int]struct {
	totalOctets int
	partOctets  int
	separator   string
}

var (
	pid        = os.Getpid()
	qtypeRegex = regexp.MustCompile("^[A-Z][A-Z0-9]*$")
	ipMeta     = ipMetaT{
		4: {4, 1, `.`},
		6: {16, 2, `:`},
	}
	ipHexRE    = regexp.MustCompile("^(0[xX])?([0-9a-fA-F]+)$")
	ip4OctetRE = regexp.MustCompile("^[0-9]{1,3}$")
	priorityRE = regexp.MustCompile("{priority:(.*?)}")
)

const (
	autoPtrOption          = "auto-ptr"
	ipPrefixOption         = "ip-prefix"
	zoneAppendDomainOption = "zone-append-domain"
)
