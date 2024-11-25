/* Copyright 2016-2024 nix <https://keybase.io/nixn>

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

import P "github.com/nixn/pdns-etcd3/src"

var (
	programVersion = P.VersionType{IsDevelopment: true, Major: 2, Minor: 0} // update this in a release branch
	gitVersion     = "GIT_VERSION_UNSET"
)

func main() {
	P.Main(programVersion, gitVersion)
}
