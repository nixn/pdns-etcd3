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
	"cmp"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type objectType[T any] map[string]T

func reversed[T any](a []T) []T {
	n := len(a)
	r := make([]T, n)
	for i := 0; i < n; i++ {
		r[n-i-1] = a[i]
	}
	return r
}

func seconds(dur time.Duration) int64 {
	return int64(dur.Seconds())
}

func clearMap[K comparable, V any](m map[K]V) {
	for k := range m {
		delete(m, k)
	}
}

func splitDomainName(name string, separator string) []string {
	name = strings.TrimSuffix(name, separator)
	if name == "" {
		return []string(nil)
	}
	return strings.Split(name, separator)
}

// Map takes a slice of type T, maps every element of it to type R through the mapper function and returns the mapped elements in a new slice of type R
func Map[T any, R any](slice []T, mapper func(T, int) R) []R {
	l := len(slice)
	r := make([]R, l)
	for i := 0; i < l; i++ {
		r[i] = mapper(slice[i], i)
	}
	return r
}

func ptr2str[T any](ptr *T) string {
	if ptr == nil {
		return "<nil>"
	}
	return fmt.Sprintf("&%v", *ptr)
}

func err2str(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func float2int(n float64) (int64, error) {
	return strconv.ParseInt(fmt.Sprintf("%.0f", n), 10, 64)
}

func maxOf[T cmp.Ordered](first T, more ...T) T {
	result := first
	for _, item := range more {
		if item > result {
			result = item
		}
	}
	return result
}
