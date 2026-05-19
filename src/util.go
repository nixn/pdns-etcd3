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
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

type objectType[T any] map[string]T

// Reverse reverses the slice in place and returns it.
func Reverse[T any](slice []T) []T {
	slices.Reverse(slice)
	return slice
}

func Concat[T any](slices ...[]T) []T {
	totalLength := 0
	for _, slice := range slices {
		totalLength += len(slice)
	}
	result := make([]T, 0, totalLength)
	for _, slice := range slices {
		result = append(result, slice...)
	}
	return result
}

func PrependT[T any](slice []T, elements ...T) []T {
	return Concat(elements, slice)
}

func Prepend(slice []any, elements ...any) []any {
	return Concat(elements, slice)
}

func seconds(dur time.Duration) int64 {
	return int64(dur.Seconds())
}

func clearMap[K comparable, V any](m map[K]V) {
	for k := range m {
		delete(m, k)
	}
}

func Keys[K comparable, V any](m map[K]V) (ks []K) {
	for k := range m {
		ks = append(ks, k)
	}
	return
}

func splitDomainName(name string, separator string) []string {
	name = strings.TrimSuffix(name, separator)
	if name == "" {
		return []string{}
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

func tn(t reflect.Type) string {
	switch t.Kind() {
	//case reflect.Map:
	//	return fmt.Sprintf("map[%s]%s", tn(t.Key()), tn(t.Elem()))
	case reflect.Ptr:
		return "*" + tn(t.Elem())
	//case reflect.Slice, reflect.Array:
	//	return "[]" + tn(t.Elem())
	default:
		var handle func(n string) string
		handle = func(n string) string {
			i, j := strings.IndexByte(n, '['), strings.LastIndexByte(n, ']')
			if i >= 0 && j > i {
				return fmt.Sprintf("%s[%s]%s", n[:i], handle(n[i+1:j]), handle(n[j+1:]))
			}
			if n == "interface {}" {
				return "any"
			}
			return n
		}
		return handle(t.String())
	}
}

func val2str(value any) string {
	return val2strR(reflect.ValueOf(value), true)
}

func val2strR(value reflect.Value, withType bool) string {
	if value.Kind() == reflect.Interface {
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Invalid:
		return "<nil>"
	case reflect.Bool, reflect.Int:
		return fmt.Sprintf("%v", value)
	case reflect.String:
		return fmt.Sprintf("%q", value.String())
	case reflect.Ptr:
		if value.IsNil() {
			return "*<nil>"
		}
		return "&" + val2strR(value.Elem(), withType)
	case reflect.Map:
		t := value.Type()
		typeStr := ""
		if withType {
			typeStr = tn(t)
		}
		if value.IsNil() {
			return typeStr + "<nil>"
		}
		isAny := t.Elem() == reflect.TypeOf((*any)(nil)).Elem()
		var parts []string
		for _, k := range value.MapKeys() {
			parts = append(parts, val2strR(k, true)+": "+val2strR(value.MapIndex(k), isAny))
		}
		return typeStr + "{" + strings.Join(parts, ", ") + "}"
	case reflect.Struct:
		sType := value.Type()
		var fields []string
		for i, n := 0, value.NumField(); i < n; i++ {
			fields = append(fields, fmt.Sprintf("%s: %s", sType.Field(i).Name, val2strR(value.Field(i), true)))
		}
		str := fmt.Sprintf("{%s}", strings.Join(fields, ", "))
		if withType {
			str = tn(sType) + str
		}
		return str
	case reflect.Slice:
		if value.IsNil() {
			return "[]<nil>"
		}
		fallthrough
	case reflect.Array:
		elemType := value.Type().Elem()
		isAny := elemType == reflect.TypeOf((*any)(nil)).Elem()
		var elements []string
		for i, n := 0, value.Len(); i < n; i++ {
			v := value.Index(i)
			elements = append(elements, val2strR(v, isAny || elemType != v.Type()))
		}
		return fmt.Sprintf("❲%s❳[%s]", tn(value.Type().Elem()), strings.Join(elements, ", "))
	default:
		str := fmt.Sprintf("%v", value)
		if withType {
			str = fmt.Sprintf("❲%s❳", tn(value.Type())) + str
		}
		return str
	}
}

func ptr2str[T any](ptr *T, format string) string {
	if ptr == nil {
		return "<nil>"
	}
	return fmt.Sprintf(format, *ptr)
}

func ptr2strS[T interface{ String() string }](ptr *T) *string {
	if ptr == nil {
		s := "<nil>"
		return &s
	}
	s := (*ptr).String()
	return &s
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

func float2decimal(n float64) string {
	str := fmt.Sprintf("%f", n)
	return strings.TrimRight(str, "0.,")
}

func CutString(str string, n int, suf string) string {
	if len(str) <= n {
		return str
	}
	return str[:n-len(suf)] + suf
}

type WaitGroup struct {
	sync.WaitGroup
	routines *MapSyncAccess[string, uint64]
}

func (wg *WaitGroup) Init() *WaitGroup {
	wg.routines = new(MapSyncAccess[string, uint64]).Init()
	return wg
}

func (wg *WaitGroup) add(name string, f *func(...any), args ...any) {
	wg.routines.Compute(name, func(_ string, count *uint64) *uint64 {
		var n uint64
		if count == nil {
			n = 1
		} else {
			n = *count + 1
		}
		wg.WaitGroup.Add(1) //nolint:staticcheck // I want this clear reference here
		if f != nil {
			go func(args ...any) {
				defer wg.Done(name)
				(*f)(args...)
			}(args...)
		}
		return &n
	})
}

func (wg *WaitGroup) Go(name string, f func(...any), args ...any) {
	wg.add(name, &f, args...)
}

func (wg *WaitGroup) Register(name string) {
	wg.add(name, nil, nil)
}

func (wg *WaitGroup) Done(name string) {
	defer wg.WaitGroup.Done()
	wg.routines.Compute(name, func(_ string, count *uint64) *uint64 {
		if *count == 1 {
			return nil
		} else {
			n := *count - 1
			return &n
		}
	})
}

func (wg *WaitGroup) State(withNames bool) (uint64, []string) {
	return WithRLock2(&wg.routines.SyncAccess, func() (count uint64, names []string) {
		if !withNames {
			count = uint64(len(wg.routines.Map))
			return
		}
		for name, cnt := range wg.routines.Map {
			count += cnt
			if cnt != 1 {
				name = fmt.Sprintf("%s (%d)", name, cnt)
			}
			names = append(names, name)
		}
		return
	})
}

func recoverPanics(f func(any) bool) {
	if r := recover(); r != nil && f != nil && f(r) {
		panic(r)
	}
}

func recoverFunc(v any, name string, exit bool) bool {
	if e, ok := v.(*logrus.Entry); ok {
		if ln, ok := e.Logger.Formatter.(*LogNode); ok {
			ln.Logf(3)(nil, "%s: fatal error", name)(e.Message)
			if exit {
				os.Exit(1)
			}
			return true
		}
	}
	RootLog.Errorf()(nil, "%s panicked", name)(v)
	return false
}

func closeNoError(c io.Closer) {
	_ = c.Close()
}

func slicePrefixed[T comparable](slice []T, prefix ...T) bool {
	l := len(slice)
	for i, t := range prefix {
		if i >= l || slice[i] != t {
			return false
		}
	}
	return true
}

func Supplier1[T1, R any](fn func(T1) R, t1 T1) func() R {
	return func() R { return fn(t1) }
}

func Supplier3[T1, T2, T3, R any](fn func(T1, T2, T3) R, t1 T1, t2 T2, t3 T3) func() R {
	return func() R { return fn(t1, t2, t3) }
}

type SyncAccess struct {
	mutex sync.RWMutex
}

func (sa *SyncAccess) WithLock(fn func()) {
	sa.mutex.Lock()
	defer sa.mutex.Unlock()
	fn()
}

func WithLock[R any](sa *SyncAccess, fn func() R) R {
	sa.mutex.Lock()
	defer sa.mutex.Unlock()
	return fn()
}

func (sa *SyncAccess) WithRLock(fn func()) {
	sa.mutex.RLock()
	defer sa.mutex.RUnlock()
	fn()
}

func WithRLock[R any](sa *SyncAccess, fn func() R) R {
	sa.mutex.RLock()
	defer sa.mutex.RUnlock()
	return fn()
}

func WithRLock2[R1 any, R2 any](sa *SyncAccess, fn func() (R1, R2)) (R1, R2) {
	sa.mutex.RLock()
	defer sa.mutex.RUnlock()
	return fn()
}

type MapSyncAccess[K comparable, V any] struct {
	SyncAccess
	Map map[K]V
}

func (m *MapSyncAccess[K, V]) Init() *MapSyncAccess[K, V] {
	m.WithLock(func() { m.Map = map[K]V{} })
	return m
}

func (m *MapSyncAccess[K, V]) Len() int {
	return WithRLock(&m.SyncAccess, func() int { return len(m.Map) })
}

func (m *MapSyncAccess[K, V]) Put(k K, v V, postFns ...func()) {
	m.WithLock(func() {
		m.Map[k] = v
		for _, fn := range postFns {
			fn()
		}
	})
}

func (m *MapSyncAccess[K, V]) Get(k K) V {
	return WithRLock(&m.SyncAccess, func() V { return m.Map[k] })
}

func (m *MapSyncAccess[K, V]) Delete(k K) {
	m.WithLock(func() { delete(m.Map, k) })
}

func (m *MapSyncAccess[K, V]) ComputeIfAbsent(k K, compute func(k K) V) V {
	m.mutex.RLock()
	if v, ok := m.Map[k]; ok {
		defer m.mutex.RUnlock()
		return v
	}
	m.mutex.RUnlock()
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if v, ok := m.Map[k]; ok {
		return v
	}
	m.Map[k] = compute(k)
	return m.Map[k]
}

func (m *MapSyncAccess[K, V]) Compute(k K, compute func(k K, v *V) *V) *V {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	var nv *V
	v, found := m.Map[k]
	if found {
		nv = compute(k, &v)
	} else {
		nv = compute(k, nil)
	}
	if nv == nil {
		if found {
			delete(m.Map, k)
		}
	} else {
		m.Map[k] = *nv
	}
	return nv
}

func Sleep(ctx context.Context, dur time.Duration) error {
	sleepCtx, cancel := context.WithTimeout(ctx, dur)
	defer cancel()
	<-sleepCtx.Done()
	return ctx.Err()
}

func Some[T any](ts ...*T) *T {
	for _, t := range ts {
		if t != nil {
			return t
		}
	}
	return nil
}

func Ptr[V any](v V) *V {
	return &v
}
