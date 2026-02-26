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

import (
	"fmt"
	"testing"
)

type ip struct {
	ver int
	pos int
	in  any
}
type bytes []byte
type bs = bytes

func (bs bytes) String() string {
	return fmt.Sprintf("%v", []byte(bs))
}

func TestParseOctets(t *testing.T) {
	for i, spec := range []test[ip, bs]{
		{ip{0, 0, nil}, ve[bs]{e: "type"}},
		{ip{0, 0, true}, ve[bs]{e: "type"}},
		{ip{0, 0, false}, ve[bs]{e: "type"}},
		{ip{0, 0, float64(-1)}, ve[bs]{e: "0-255"}},
		{ip{0, 0, float64(0)}, ve[bs]{v: bs{0}}},
		{ip{0, 0, float64(255)}, ve[bs]{v: bs{255}}},
		{ip{0, 0, float64(256)}, ve[bs]{e: "0-255"}},
		{ip{0, 0, 2.9}, ve[bs]{v: bs{3}}},
		{ip{0, 0, 3.1}, ve[bs]{v: bs{3}}},
		{ip{0, 0, ""}, ve[bs]{e: "empty"}},
		{ip{4, 0, "2.9"}, ve[bs]{v: bs{2, 9}}},
		{ip{6, 0, "2.9"}, ve[bs]{e: "syntax"}},
		{ip{0, 0, "1"}, ve[bs]{v: bs{1}}},
		{ip{0, 0, "f"}, ve[bs]{v: bs{0x0f}}},
		{ip{4, 0, "12"}, ve[bs]{v: bs{12}}},
		{ip{0, 0, "0x12"}, ve[bs]{v: bs{0x12}}},
		{ip{6, 0, "12"}, ve[bs]{v: bs{0x12}}},
		{ip{0, 0, "ef"}, ve[bs]{v: bs{0xef}}},
		{ip{4, 0, "123"}, ve[bs]{v: bs{123}}},
		{ip{6, 0, "123"}, ve[bs]{v: bs{0x01, 0x23}}},
		{ip{4, 0, "999"}, ve[bs]{e: "range"}},
		{ip{6, 0, "999"}, ve[bs]{v: bs{0x09, 0x99}}},
		{ip{0, 0, "1234"}, ve[bs]{v: bs{0x12, 0x34}}},
		{ip{0, 0, "123456"}, ve[bs]{v: bs{0x12, 0x34, 0x56}}},
		{ip{0, 0, "0X123456"}, ve[bs]{v: bs{0x12, 0x34, 0x56}}},
		{ip{0, 0, "12345678"}, ve[bs]{v: bs{0x12, 0x34, 0x56, 0x78}}},
		{ip{4, 0, "123456789"}, ve[bs]{e: "1 - 4"}},
		{ip{6, 0, "123456789"}, ve[bs]{v: bs{0x01, 0x23, 0x45, 0x67, 0x89}}},
		{ip{4, 0, "1234567890"}, ve[bs]{e: "1 - 4"}},
		{ip{6, 0, "1234567890"}, ve[bs]{v: bs{0x12, 0x34, 0x56, 0x78, 0x90}}},
		{ip{6, 0, "1234567890abcdef1234567890abcdef"}, ve[bs]{v: bs{0x12, 0x34, 0x56, 0x78, 0x90, 0xab, 0xcd, 0xef, 0x12, 0x34, 0x56, 0x78, 0x90, 0xab, 0xcd, 0xef}}},
		{ip{6, 0, "1234567890abcdef1234567890abcdef12"}, ve[bs]{e: "1 - 16"}},
		{ip{0, 0, "A"}, ve[bs]{v: bs{0x0a}}},
		{ip{0, 0, "Ab"}, ve[bs]{v: bs{0xab}}},
		{ip{0, 0, "Abc"}, ve[bs]{v: bs{0x0a, 0xbc}}},
		{ip{0, 0, "Abcd"}, ve[bs]{v: bs{0xab, 0xcd}}},
		{ip{0, 0, "Abcdef"}, ve[bs]{v: bs{0xab, 0xcd, 0xef}}},
		{ip{0, 0, "Abcdefgh"}, ve[bs]{e: "syntax"}},
		{ip{0, 0, "1A2b3c4d"}, ve[bs]{v: bs{0x1a, 0x2b, 0x3c, 0x4d}}},
		{ip{4, 0, []any{}}, ve[bs]{e: "1 - 4"}},
		{ip{6, 0, []any{}}, ve[bs]{e: "1 - 16"}},
		{ip{0, 0, []any{1.9}}, ve[bs]{v: bs{2}}},
		{ip{0, 0, []any{2.1}}, ve[bs]{v: bs{2}}},
		{ip{0, 0, []any{float64(10), "20"}}, ve[bs]{v: bs{10, 20}}},
		{ip{0, 0, []any{float64(10), "20", "0x30"}}, ve[bs]{v: bs{10, 20, 0x30}}},
		{ip{0, 0, []any{float64(10), "20", "0x30", "040"}}, ve[bs]{v: bs{10, 20, 0x30, 0o40}}},
		{ip{4, 0, []any{float64(10), "20", "0x30", "040", "50"}}, ve[bs]{e: "1 - 4"}},
		{ip{6, 0, []any{float64(10), "20", "0x30", "040", "50"}}, ve[bs]{v: bs{10, 20, 0x30, 0o40, 50}}},
		{ip{6, 0, []any{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16"}}, ve[bs]{v: bs{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}}},
		{ip{6, 0, []any{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17"}}, ve[bs]{e: "1 - 16"}},
		{ip{6, 0, "1:2:3:4:5:6:7:8"}, ve[bs]{v: bs{0, 1, 0, 2, 0, 3, 0, 4, 0, 5, 0, 6, 0, 7, 0, 8}}},
		{ip{4, -1, "1."}, ve[bs]{v: bs{1}}},
		{ip{4, +1, "1."}, ve[bs]{e: "separator last"}},
		{ip{4, -1, ".1."}, ve[bs]{e: "separator first"}},
		{ip{4, +1, ".1"}, ve[bs]{v: bs{1}}},
		{ip{4, 0, "1.2.3"}, ve[bs]{v: bs{1, 2, 3}}},
		{ip{4, -1, ".1.2.3"}, ve[bs]{e: "separator first"}},
		{ip{4, +1, ".1.2.3"}, ve[bs]{v: bs{1, 2, 3}}},
		{ip{4, -1, "1.2.3."}, ve[bs]{v: bs{1, 2, 3}}},
		{ip{4, +1, "1.2.3."}, ve[bs]{e: "separator last"}},
		{ip{4, -1, "1.2.3.4."}, ve[bs]{e: "1 - 4"}},
		{ip{4, +1, ".1.2.3.4"}, ve[bs]{e: "1 - 4"}},
		{ip{4, 0, "1.2.3.4.5"}, ve[bs]{e: "1 - 4"}},
		{ip{6, 0, "1.2.3.4.5"}, ve[bs]{e: "syntax"}},
		{ip{6, 0, ":"}, ve[bs]{e: "separator"}},
		{ip{4, 0, "::"}, ve[bs]{e: "IPv4"}},
		{ip{6, 0, "::"}, ve[bs]{v: bs{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}}},
		{ip{6, 0, ":::"}, ve[bs]{e: "IP"}},
		{ip{6, 0, "::a::"}, ve[bs]{e: "IP"}},
		{ip{4, 0, "123.45.6.0"}, ve[bs]{v: bs{123, 45, 6, 0}}},
		{ip{6, 0, "123.45.6.0"}, ve[bs]{e: "syntax"}},
		// IPv6-mapped IPv4
		{ip{4, 0, "::ffff:123.45.6.0"}, ve[bs]{v: bs{123, 45, 6, 0}}},
		{ip{6, 0, "::ffff:123.45.6.0"}, ve[bs]{v: bs{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255, 255, 123, 45, 6, 0}}},
		{ip{4, 0, "::ffff:1.2.3.4.5"}, ve[bs]{e: "IPv4"}},
		{ip{6, 0, "::ffff:1.2.3.4.5"}, ve[bs]{e: "IPv6"}},
		{ip{4, 0, "::ffff:7B2d:0600"}, ve[bs]{v: bs{123, 45, 6, 0}}},
		{ip{6, 0, "::ffff:7B2d:0600"}, ve[bs]{v: bs{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255, 255, 123, 45, 6, 0}}},
		{ip{4, 0, "::ffff:7B2d0600"}, ve[bs]{e: "IPv4"}},
		{ip{6, 0, "::ffff:7B2d0600"}, ve[bs]{e: "IPv6"}},
		// with double colon
		{ip{6, 0, "1000::1"}, ve[bs]{v: bs{16, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}}},
		{ip{6, 0, "::10:20"}, ve[bs]{v: bs{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x10, 0, 0x20}}},
		{ip{6, -1, "1020::30:"}, ve[bs]{v: bs{0x10, 0x20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x30}}},
		{ip{6, +1, "1020::30:"}, ve[bs]{e: "separator last"}},
		{ip{6, -1, ":1020::30"}, ve[bs]{e: "separator first"}},
		{ip{6, +1, ":1020::30"}, ve[bs]{v: bs{0x10, 0x20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x30}}},
		{ip{6, 0, "0:1020::30"}, ve[bs]{v: bs{0, 0, 0x10, 0x20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x30}}},
		{ip{6, 0, "1020::30:0"}, ve[bs]{v: bs{0x10, 0x20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x30, 0, 0}}},
		// prefix+suffix handling, v4 // TODO this is different to IPv6. here the last octet of a prefix IP is not shifted. is it ok?
		{ip{4, -1, "1."}, ve[bs]{v: bs{1}}},
		{ip{4, +1, ".1"}, ve[bs]{v: bs{1}}},
		{ip{4, 0, "1.1"}, ve[bs]{v: bs{1, 1}}},
		{ip{4, -1, "1.1."}, ve[bs]{v: bs{1, 1}}},
		{ip{4, +1, ".1.1"}, ve[bs]{v: bs{1, 1}}},
		{ip{4, 0, "12.12"}, ve[bs]{v: bs{12, 12}}},
		{ip{4, -1, "12.12."}, ve[bs]{v: bs{12, 12}}},
		{ip{4, +1, ".12.12"}, ve[bs]{v: bs{12, 12}}},
		{ip{4, 0, "123.123"}, ve[bs]{v: bs{123, 123}}},
		{ip{4, -1, "123.123."}, ve[bs]{v: bs{123, 123}}},
		{ip{4, +1, ".123.123"}, ve[bs]{v: bs{123, 123}}},
		// prefix+suffix handling, v6
		{ip{6, -1, "1:"}, ve[bs]{v: bs{0x00, 0x01}}},
		{ip{6, +1, ":1"}, ve[bs]{v: bs{0x00, 0x01}}},
		{ip{6, -1, "1:2"}, ve[bs]{v: bs{0x00, 0x01, 0x20}}},
		{ip{6, +1, "1:2"}, ve[bs]{v: bs{0x01, 0x00, 0x02}}},
		{ip{6, -1, "1:2:"}, ve[bs]{v: bs{0x00, 0x01, 0x00, 0x02}}},
		{ip{6, +1, ":1:2"}, ve[bs]{v: bs{0x00, 0x01, 0x00, 0x02}}},
		{ip{6, -1, "12:34"}, ve[bs]{v: bs{0x00, 0x12, 0x34}}},
		{ip{6, +1, "12:34"}, ve[bs]{v: bs{0x12, 0x00, 0x34}}},
		{ip{6, -1, "12:34:"}, ve[bs]{v: bs{0x00, 0x12, 0x00, 0x34}}},
		{ip{6, +1, ":12:34"}, ve[bs]{v: bs{0x00, 0x12, 0x00, 0x34}}},
		{ip{6, -1, "123:456"}, ve[bs]{v: bs{0x01, 0x23, 0x45, 0x60}}},
		{ip{6, +1, "123:456"}, ve[bs]{v: bs{0x01, 0x23, 0x04, 0x56}}},
		{ip{6, -1, "123:456:"}, ve[bs]{v: bs{0x01, 0x23, 0x04, 0x56}}},
		{ip{6, +1, ":123:456"}, ve[bs]{v: bs{0x01, 0x23, 0x04, 0x56}}},
		{ip{6, -1, "1234:5678"}, ve[bs]{v: bs{0x12, 0x34, 0x56, 0x78}}},
		{ip{6, +1, "1234:5678"}, ve[bs]{v: bs{0x12, 0x34, 0x56, 0x78}}},
		{ip{6, -1, "1234:5678:"}, ve[bs]{v: bs{0x12, 0x34, 0x56, 0x78}}},
		{ip{6, +1, ":1234:5678"}, ve[bs]{v: bs{0x12, 0x34, 0x56, 0x78}}},
		// invalid
		{ip{6, 0, "10:20fg"}, ve[bs]{e: "failed to parse as uint16"}},
		{ip{6, 0, "12345:6789"}, ve[bs]{e: "range"}},
		{ip{6, -1, "12345:6789:"}, ve[bs]{e: "range"}},
		{ip{6, +1, ":12345:6789"}, ve[bs]{e: "range"}},
		{ip{4, -1, ".1."}, ve[bs]{e: "separator first"}},
		{ip{4, +1, ".1."}, ve[bs]{e: "separator last"}},
		{ip{6, -1, ":1:"}, ve[bs]{e: "separator first"}},
		{ip{6, +1, ":1:"}, ve[bs]{e: "separator last"}},
		{ip{4, -1, ".."}, ve[bs]{e: "separator first"}},
		{ip{4, +1, ".."}, ve[bs]{e: "separator last"}},
		{ip{4, 0, "1..3"}, ve[bs]{e: "empty"}},
		{ip{6, 0, "1:::3"}, ve[bs]{e: "IPv6"}},
	} {
		pf := func(ipVer int, asPrefix bool) testFunc[any, bs] {
			return func(_ *testing.T, in any) (bs, error) {
				return parseOctets(in, ipVer, asPrefix)
			}
		}
		for _, ipVer := range []int{4, 6} {
			if (spec.input.ver == 0 || spec.input.ver == ipVer) && spec.input.pos < 1 {
				checkRun[any, bs](t, fmt.Sprintf("(%d)v%d,prefix:%#v", i+1, ipVer, spec.input.in), pf(ipVer, true), spec.input.in, spec.expected, true)
			}
			if (spec.input.ver == 0 || spec.input.ver == ipVer) && spec.input.pos > -1 {
				checkRun[any, bs](t, fmt.Sprintf("(%d)v%d,suffix:%#v", i+1, ipVer, spec.input.in), pf(ipVer, false), spec.input.in, spec.expected, true)
			}
		}
	}
}
