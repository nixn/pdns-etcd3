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
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

type ve[Value any] struct {
	v Value
	e string
	c map[string]Condition
}

type test[Input any, Value any] struct {
	input    Input
	expected ve[Value]
}

type testFunc[Input any, Value any] func(*testing.T, Input) (Value, error)

func Logf(t *testing.T, format string, args ...any) {
	t.Helper()
	now := time.Now()
	t.Logf("[%s] "+format, append([]any{now.Format("2006-01-02 15:04:05.000")}, args...)...)
}

func Errorf(t *testing.T, format string, args ...any) {
	t.Helper()
	Logf(t, format, args...)
	t.Fail()
}

func Fatalf(t *testing.T, format string, args ...any) {
	t.Helper()
	Logf(t, format, args...)
	t.FailNow()
}

func checkT[Input any, Value any](t *testing.T, f testFunc[Input, Value], in Input, expected ve[Value], quiet bool) {
	t.Helper()
	got, err := f(t, in)
	if expected.e != "" {
		if err == nil {
			Errorf(t, `%#+v -> expected error with %q, got value: %s`, in, expected.e, val2str(got))
		} else if !strings.Contains(err.Error(), expected.e) {
			Errorf(t, `%#+v -> expected error with %q, got error: %s`, in, expected.e, err)
		} else if !quiet {
			Logf(t, "got expected error")
		}
	} else {
		if err != nil {
			Errorf(t, `%#+v -> expected value: %s, got error: %s`, in, val2str(expected.v), err)
		} else {
			if unequal := testEqual(t, expected.v, got, expected.c, ""); unequal != nil {
				Errorf(t, `%s -> expected: %s (conditions: %s), got: %s (%s)`, val2str(in), val2str(expected.v), val2str(expected.c), val2str(got), unequal)
			} else if !quiet {
				Logf(t, "got expected value")
			}
		}
	}
}

func checkRun[Input any, Value any](t *testing.T, id string, f testFunc[Input, Value], in Input, expected ve[Value], quiet bool) bool {
	t.Helper()
	return t.Run(id, func(t *testing.T) {
		checkT(t, f, in, expected, quiet)
	})
}

type DeepError struct {
	Error error
	Cause *DeepError
}

func DeepErrorf(format string, args ...any) *DeepError {
	return &DeepError{fmt.Errorf(format, args...), nil}
}

func (de DeepError) String() string {
	str := fmt.Sprintf("%v", de.Error)
	if de.Cause != nil {
		str = fmt.Sprintf("%s: %s", str, de.Cause)
	}
	return str
}

func (de DeepError) Depth() int {
	depth := 0
	if de.Cause != nil {
		depth += de.Cause.Depth()
	}
	return depth
}

type Condition interface {
	Test(t *testing.T, conditions map[string]Condition, path string, b reflect.Value, a ...reflect.Value) *DeepError
}

type Ignore struct {
}

func (i Ignore) Test(t *testing.T, _ map[string]Condition, _ string, _ reflect.Value, _ ...reflect.Value) *DeepError {
	t.Helper()
	return nil
}

type OtherDefault[T any] struct {
	Value, InsteadOf T
}

func (od OtherDefault[T]) Test(t *testing.T, conditions map[string]Condition, path string, b reflect.Value, a ...reflect.Value) *DeepError {
	t.Helper()
	defR := reflect.ValueOf(od.Value)
	if len(a) == 0 { // same as CompareWith
		return testEqualR(t, defR, b, conditions, "!"+path)
	}
	if testEqualR(t, a[0], reflect.ValueOf(od.InsteadOf), nil, "") != nil {
		defR = a[0]
	}
	return testEqualR(t, defR, b, conditions, "!"+path)
}

// WhenDefault returns equality, when `a` is equal to Value (the default value), otherwise it compares normally with `a`
type WhenDefault[T any] struct {
	Value T
}

func (wd WhenDefault[T]) Test(t *testing.T, conditions map[string]Condition, path string, b reflect.Value, a ...reflect.Value) *DeepError {
	t.Helper()
	defR := reflect.ValueOf(wd.Value)
	if len(a) == 0 { // same as CompareWith
		return testEqualR(t, defR, b, conditions, "!"+path)
	}
	if testEqualR(t, a[0], reflect.ValueOf(wd.Value), nil, "") == nil {
		return nil
	}
	return testEqualR(t, a[0], b, conditions, "!"+path)
}

type CompareWith[T any] struct {
	Value T
}

func (cw CompareWith[T]) Test(t *testing.T, conditions map[string]Condition, path string, b reflect.Value, _ ...reflect.Value) *DeepError {
	t.Helper()
	a := reflect.ValueOf(cw.Value)
	return testEqualR(t, a, b, conditions, "!"+path)
}

type Matches struct {
	Regex string
}

func (m Matches) Test(t *testing.T, _ map[string]Condition, _ string, b reflect.Value, _ ...reflect.Value) *DeepError {
	t.Helper()
	if b.Kind() == reflect.String {
		re := regexp.MustCompile(m.Regex)
		if !re.MatchString(b.Interface().(string)) {
			return DeepErrorf("does not match regex (%s)", re)
		}
		return nil
	}
	return DeepErrorf("not a string (%T)", b)
}

type SliceContains struct {
	Ordered, All, Only bool
	Elements           []any
}

func (sc SliceContains) Test(t *testing.T, conditions map[string]Condition, path string, have reflect.Value, a ...reflect.Value) *DeepError {
	t.Helper()
	if have.Kind() != reflect.Slice {
		return DeepErrorf("not a slice (%s)", have.Type().String())
	}
	var need reflect.Value // reflecting the elements slice
	if len(a) > 0 {
		need = a[0]
	} else {
		need = reflect.ValueOf(sc.Elements)
	}
	haveN, needN := have.Len(), need.Len()
	need2have := map[int]int{}
	have2need := map[int]int{}
NEED:
	for i, j := 0, 0; j < needN; j++ {
		if !sc.Ordered {
			i = 0
		}
		var causes []*DeepError
		for ; i < haveN; i++ {
			if err := testEqualR(t, need.Index(j), have.Index(i), conditions, fmt.Sprintf("%s@%d", path, j)); err == nil {
				need2have[j] = i
				have2need[i] = j
				continue NEED
			} else if sc.Ordered && sc.All {
				return DeepErrorf("unequal element @%d", i)
			} else {
				causes = append(causes, err)
			}
		}
		if sc.All {
			if len(causes) > 1 {
				causesString := ""
				for _, cause := range causes {
					causesString += fmt.Sprintf(" [%s]", cause)
				}
				return DeepErrorf("missing needed element @%d, possible causes:%s", j, causesString)
			} else if len(causes) == 1 {
				return &DeepError{fmt.Errorf("missing needed element @%d", j), causes[0]}
			}
			return DeepErrorf("missing needed element @%d, empty slice", j)
		}
	}
	if sc.All && len(need2have) != needN {
		return DeepErrorf("missing elements with 'All' set (need %d, found %d)", needN, len(need2have))
	}
	if sc.Only && len(have2need) != haveN {
		return DeepErrorf("unexpected elements with 'Only' set (%d)", haveN-len(have2need))
	}
	return nil
}

func testEqual(t *testing.T, a, b any, conditions map[string]Condition, path string) *DeepError {
	t.Helper()
	return testEqualR(t, reflect.ValueOf(a), reflect.ValueOf(b), conditions, path)
}

var compiledConditionsCache = map[string]*regexp.Regexp{}

func testEqualR(t *testing.T, a, b reflect.Value, conditions map[string]Condition, path string) *DeepError {
	t.Helper()
	if a.Kind() == reflect.Interface {
		a = a.Elem()
	}
	if b.Kind() == reflect.Interface {
		b = b.Elem()
	}
	vt := a.Type()
	for re, cond := range conditions {
		var rec *regexp.Regexp
		if rec = compiledConditionsCache[re]; rec == nil {
			rec = regexp.MustCompile("^" + re + "$")
			compiledConditionsCache[re] = rec
		}
		if rec.MatchString(path) {
			if strings.HasPrefix(path, "!") {
				path = path[1:]
				goto NORMAL
			}
			return cond.Test(t, conditions, path, b, a)
		}
	}
	if conditionType := reflect.TypeOf((*Condition)(nil)).Elem(); vt.Implements(conditionType) {
		cond := a.Interface().(Condition)
		return cond.Test(t, conditions, path, b)
	}
NORMAL:
	if vt != b.Type() {
		return DeepErrorf("different types (%s ≠ %s)", tn(vt), tn(b.Type()))
	}
	switch vt.Kind() {
	case reflect.Pointer:
		if an, bn := a.IsNil(), b.IsNil(); !an && !bn {
			if err := testEqualR(t, a.Elem(), b.Elem(), conditions, path+"-"); err != nil {
				return &DeepError{fmt.Errorf("unequal (*)"), err}
			}
		} else if !an || !bn {
			return DeepErrorf("one <nil>, one not (%v ≠ %v)", a, b)
		}
	case reflect.Struct:
		for i, n := 0, vt.NumField(); i < n; i++ {
			if unequal := testEqualR(t, a.Field(i), b.Field(i), conditions, path+">"+vt.Field(i).Name); unequal != nil {
				return &DeepError{fmt.Errorf("unequal value for %q", vt.Field(i).Name), unequal}
			}
		}
	case reflect.Slice, reflect.Array:
		n := a.Len()
		if n != b.Len() {
			return DeepErrorf("different lengths (%d ≠ %d)", n, b.Len())
		}
		for i := 0; i < n; i++ {
			if unequal := testEqualR(t, a.Index(i), b.Index(i), conditions, fmt.Sprintf("%s@%d", path, i)); unequal != nil {
				return &DeepError{fmt.Errorf("unequal elements at index %d", i), unequal}
			}
		}
	case reflect.Map:
		var missing []string
		for _, k := range a.MapKeys() {
			if bv := b.MapIndex(k); bv.IsValid() && !bv.IsZero() {
				if unequal := testEqualR(t, a.MapIndex(k), bv, conditions, fmt.Sprintf("%s:%s", path, k)); unequal != nil {
					return &DeepError{fmt.Errorf("unequal value for %q", k), unequal}
				}
			} else /*if _, ok := v.(Ignore); !ok*/ {
				missing = append(missing, val2strR(k, true))
			}
		}
		if len(missing) > 0 {
			return DeepErrorf("missing key(s): %s", strings.Join(missing, ", "))
		}
		var extra []string
		for _, k := range b.MapKeys() {
			if av := a.MapIndex(k); !av.IsValid() || av.IsZero() {
				extra = append(extra, val2strR(k, true))
			}
		}
		if len(extra) > 0 {
			return DeepErrorf("extra key(s): %s", strings.Join(extra, ", "))
		}
	case reflect.Bool, reflect.String, reflect.Int, reflect.Uint, reflect.Int8, reflect.Uint8, reflect.Int16, reflect.Uint16, reflect.Int32, reflect.Uint32, reflect.Int64, reflect.Uint64, reflect.Float32, reflect.Float64:
		if !a.Equal(b) {
			return DeepErrorf("%s ≠ %s (%s)", val2strR(a, false), val2strR(b, false), vt)
		}
	default:
		if !a.Equal(b) {
			return DeepErrorf("%v ≠ %v (type: %s, kind: %s)", val2strR(a, false), val2strR(b, false), vt, vt.Kind())
		}
	}
	return nil
}

type exitErr struct {
	Code int
}

func recoverPanicsT(t *testing.T) {
	if r := recover(); r != nil {
		if e, ok := r.(exitErr); ok {
			if e.Code != 1 {
				Fatalf(t, "unexpected exit code: %d", e.Code)
			}
			Logf(t, "expected exit code (1)")
			// expected exit
			return
		}
		Fatalf(t, "unexpected panic: %v", r)
	}
}

func waitFor(t *testing.T, desc string, condition func() bool, interval time.Duration, timeout time.Duration) error {
	t.Helper()
	Logf(t, "waiting for condition %q (interval: %s, timeout: %s)", desc, interval, timeout)
	checkTime := max(min(timeout/10, 1*time.Second), interval)
	checkIncr := checkTime
	since := time.Now()
	for !condition() {
		passed := time.Since(since)
		if timeout > 0 && passed >= timeout {
			Logf(t, "condition %q timed out after %s", desc, timeout)
			return fmt.Errorf("timed out after %s", timeout)
		}
		if timeout > 0 && passed >= checkTime {
			Logf(t, "waiting for condition %q (%s)", desc, checkTime)
			checkTime += checkIncr
		}
		time.Sleep(interval)
	}
	duration := time.Since(since)
	Logf(t, "condition %q fulfilled after %s", desc, duration)
	return nil
}

func sleepT(t *testing.T, duration time.Duration, interval ...time.Duration) {
	i := duration
	if len(interval) > 0 {
		i = interval[0]
	}
	_ = waitFor(t, fmt.Sprintf("sleep %s", duration), func() bool { return false }, i, duration)
}

func fatalOnErr(t *testing.T, desc string, err error) {
	t.Helper()
	if err != nil {
		Fatalf(t, "failed to %s: %s", desc, err)
	}
}

func getenvT(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}
