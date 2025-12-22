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
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

type ve[Value any] struct {
	v Value
	e string
}

type test[Input any, Value any] struct {
	input    Input
	expected ve[Value]
}

type testFunc[Input any, Value any] func(Input) (Value, error)

func check[Input any, Value any](t *testing.T, id string, f testFunc[Input, Value], in Input, expected ve[Value]) bool {
	t.Helper()
	return t.Run(id, func(t *testing.T) {
		t.Helper()
		got, err := f(in)
		if expected.e != "" {
			if err == nil {
				t.Errorf(`%#+v -> expected error with %q, got value: %#v`, in, expected.e, got)
			} else if !strings.Contains(err.Error(), expected.e) {
				t.Errorf(`%#+v -> expected error with %q, got error: %s`, in, expected.e, err)
			}
		} else {
			if err != nil {
				t.Errorf(`%#+v -> expected value: %v, got error: %s`, in, expected.v, err)
			} else {
				if unequal := testEqual(t, expected.v, got); unequal != nil {
					t.Errorf(`%s -> expected: %s, got: %s (%s)`, val2str(in), val2str(expected.v), val2str(got), unequal)
				}
			}
		}
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
	Test(t *testing.T, ov reflect.Value) *DeepError
}

type Ignore struct {
}

func (i Ignore) Test(t *testing.T, _ reflect.Value) *DeepError {
	t.Helper()
	return nil
}

type Matches struct {
	Regex string
}

func (m Matches) Test(t *testing.T, ov reflect.Value) *DeepError {
	t.Helper()
	if ov.Kind() == reflect.String {
		re := regexp.MustCompile(m.Regex)
		if !re.MatchString(ov.Interface().(string)) {
			return DeepErrorf("does not match regex (%s)", re)
		}
		return nil
	}
	return DeepErrorf("not a string (%T)", ov)
}

type SliceContains struct {
	Ordered, Size bool
	Elements      []any
}

func (need SliceContains) Test(t *testing.T, have reflect.Value) *DeepError {
	t.Helper()
	if have.Kind() == reflect.Slice {
		n, i, haveN := 0, 0, have.Len()
	NEED:
		for j, needJ := range need.Elements {
			if !need.Ordered {
				i = 0
			}
			var causes []*DeepError
			for ; i < haveN; i++ {
				if err := testEqualR(t, reflect.ValueOf(needJ), have.Index(i)); err == nil {
					n++
					continue NEED
				} else if need.Ordered && need.Size {
					return DeepErrorf("unequal element @%d", i)
				} else {
					causes = append(causes, err)
				}
			}
			if len(causes) > 1 {
				causesString := ""
				for _, cause := range causes {
					causesString += fmt.Sprintf(" [%s]", cause)
				}
				return DeepErrorf("missing needed element @%d, possible causes:%s]", j, causesString)
			} else if len(causes) == 1 {
				return &DeepError{fmt.Errorf("missing needed element @%d", j), causes[0]}
			}
			return DeepErrorf("missing needed element @%d, empty slice", j)
		}
		if need.Size && n != haveN {
			return DeepErrorf("missing or extra elements with 'Size' set (need %d, have %d)", n, haveN)
		}
		return nil
	}
	return DeepErrorf("not a slice (%T)", have)
}

func testEqual(t *testing.T, a, b any) *DeepError {
	t.Helper()
	return testEqualR(t, reflect.ValueOf(a), reflect.ValueOf(b))
}

func testEqualR(t *testing.T, a, b reflect.Value) *DeepError {
	t.Helper()
	if a.Kind() == reflect.Interface {
		a = a.Elem()
	}
	if b.Kind() == reflect.Interface {
		b = b.Elem()
	}
	conditionType := reflect.TypeOf((*Condition)(nil)).Elem()
	vt := a.Type()
	if vt.Implements(conditionType) {
		cond := a.Interface().(Condition)
		return cond.Test(t, b)
	}
	if vt != b.Type() {
		return DeepErrorf("different types (%s ≠ %s)", vt, b.Type())
	}
	switch vt.Kind() {
	case reflect.Pointer:
		if an, bn := a.IsNil(), b.IsNil(); !an && !bn {
			if err := testEqualR(t, a.Elem(), b.Elem()); err != nil {
				return &DeepError{fmt.Errorf("unequal (*)"), err}
			}
		} else if !an || !bn {
			return DeepErrorf("one <nil>, one not (%v ≠ %v)", a, b)
		}
	case reflect.Struct:
		for i, n := 0, vt.NumField(); i < n; i++ {
			if unequal := testEqualR(t, a.Field(i), b.Field(i)); unequal != nil {
				return &DeepError{fmt.Errorf("unequal value for %q", vt.Field(i).Name), unequal}
			}
		}
	case reflect.Slice, reflect.Array:
		n := a.Len()
		if n != b.Len() {
			return DeepErrorf("different lengths (%d ≠ %d)", n, b.Len())
		}
		for i := 0; i < n; i++ {
			if unequal := testEqualR(t, a.Index(i), b.Index(i)); unequal != nil {
				return &DeepError{fmt.Errorf("unequal elements at index %d", i), unequal}
			}
		}
	case reflect.Map:
		var missing []string
		for _, k := range a.MapKeys() {
			if bv := b.MapIndex(k); !bv.IsZero() {
				if unequal := testEqualR(t, a.MapIndex(k), bv); unequal != nil {
					return &DeepError{fmt.Errorf("unequal value for %q", k), unequal}
				}
			} else /*if _, ok := v.(Ignore); !ok*/ {
				missing = append(missing, fmt.Sprintf("%v", k))
			}
		}
		if len(missing) > 0 {
			return DeepErrorf("missing key(s): %s", strings.Join(missing, ", "))
		}
		var extra []string
		for _, k := range b.MapKeys() {
			if av := a.MapIndex(k); av.IsZero() {
				extra = append(extra, fmt.Sprintf("%v", k))
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

func handleExitInLogging(t *testing.T) func() {
	t.Logf("preventing os.Exit in logging")
	oldExitFunc := logrus.StandardLogger().ExitFunc
	logrus.StandardLogger().ExitFunc = func(code int) {
		panic(exitErr{code})
	}
	return func() {
		logrus.StandardLogger().ExitFunc = oldExitFunc
	}
}

func recoverPanics(t *testing.T) {
	if r := recover(); r != nil {
		if e, ok := r.(exitErr); ok {
			if e.Code != 1 {
				t.Fatalf("unexpected exit code: %d", e.Code)
			}
			t.Logf("expected exit code (1)")
			// expected exit
			return
		}
		t.Fatalf("unexpected panic: %v", r)
	}
}
