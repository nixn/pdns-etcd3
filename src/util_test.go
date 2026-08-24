//go:build unit

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

import "testing"

func TestParamInt64(t *testing.T) {
	for _, c := range []struct {
		in     any
		want   int64
		errSub string
	}{
		{float64(7), 7, ""},
		{"42", 42, ""},
		{int64(5), 5, ""},
		{true, 0, "not a number"},
	} {
		got, err := paramInt64(c.in)
		if c.errSub != "" {
			if err == nil {
				Errorf(t, "%#v: expected error", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			Errorf(t, "%#v -> %d,%v want %d", c.in, got, err, c.want)
		}
	}
}
