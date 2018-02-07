/* Copyright 2016-2018 nix <https://github.com/nixn>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. */

package main

import "time"

const (
	defaultPdnsVersion   = 3
	defaultPrefix        = ""
	defaultReversedNames = false
)

const (
	defaultEndpointIPv4 = "127.0.0.1:2379"
	defaultEndpointIPv6 = "[::1]:2379"
	defaultDialTimeout  = 2 * time.Second
)

const (
	defaultsKey  = "-defaults-"
	keySeparator = "/"
)
