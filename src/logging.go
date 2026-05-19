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
	"slices"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	FatalLevel     = -4
	ErrorLevel     = -3
	WarningLevel   = -2
	ImportantLevel = -1
)

// TODO enable client-specific logging (parameters from requests)

type logNodeParent struct {
	node      *LogNode
	component string
}
type LogNode struct {
	level    *int
	logger   *logrus.Logger
	parent   *logNodeParent
	children map[string]*LogNode
}

var RootLog = new(LogNode).init(nil, "").createLogger()

func (ln *LogNode) init(parent *LogNode, component string) *LogNode {
	ln.children = map[string]*LogNode{}
	if parent != nil {
		ln.parent = &logNodeParent{parent, component}
		parent.children[component] = ln
	}
	return ln
}

func (ln *LogNode) createLogger() *LogNode {
	ln.logger = &logrus.Logger{
		Out:       os.Stderr,
		Formatter: ln,
		Level:     logrus.DebugLevel,
	}
	return ln
}

func (ln *LogNode) logrusLogger() *logrus.Logger {
	node := ln
	for ; node.parent != nil; node = node.parent.node {
		if node.logger != nil {
			break
		}
	}
	return node.logger
}

func (ln *LogNode) ChildLog(component ...string) *LogNode {
	node := ln
	for _, comp := range component {
		if child, ok := node.children[comp]; ok {
			node = child
		} else {
			node = new(LogNode).init(node, comp)
		}
	}
	return node
}

func (ln *LogNode) getLevel() int {
	for node := ln; ; node = node.parent.node {
		if node.level != nil {
			return *node.level
		}
		if node.parent == nil {
			break
		}
	}
	return 0
}

func (ln *LogNode) SetLevel(level int) {
	ln.level = Ptr(max(level, FatalLevel))
}

func (ln *LogNode) component() []string {
	component := make([]string, 0, 10)
	for node := ln; node.parent != nil; node = node.parent.node {
		component = append(component, node.parent.component)
	}
	slices.Reverse(component)
	return component
}

func logrusLevel(level int) logrus.Level {
	if level <= FatalLevel {
		return logrus.PanicLevel
	}
	switch level {
	case ErrorLevel:
		return logrus.ErrorLevel
	case WarningLevel, ImportantLevel:
		return logrus.WarnLevel
	case 0:
		return logrus.InfoLevel
	default:
		return logrus.DebugLevel
	}
}

func levelChars(level int) string {
	if level <= FatalLevel {
		return "FTL"
	}
	switch level {
	case ErrorLevel:
		return "ERR"
	case WarningLevel:
		return "WRN"
	case ImportantLevel:
		return "IMP"
	case 0:
		return "INF"
	default:
		return fmt.Sprintf("D%+d", level)
	}
}

func evalSupplierR(v any, i int) any {
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Func {
		if rt := rv.Type(); rt.NumIn() == 0 && rt.NumOut() >= 1 {
			ovs := Map(rv.Call(nil), func(rv reflect.Value, _ int) any { return rv.Interface() })
			if i >= 0 || len(ovs) == 1 {
				return ovs[0]
			}
			return ovs
		}
	}
	return v
}

func (ln *LogNode) Logf(level int, component ...string) func(*pdnsClientID, string, ...any) func(...any) {
	node := ln
	for _, comp := range component {
		if child, ok := node.children[comp]; ok {
			node = child
		} else {
			break
		}
	}
	nodeLevel := node.getLevel()
	if level > FatalLevel && nodeLevel < level {
		return func(*pdnsClientID, string, ...any) func(...any) { return func(...any) {} }
	}
	return func(clientID *pdnsClientID, format string, args ...any) func(...any) {
		return func(fields ...any) {
			now := time.Now() // don't move upwards, because the upper function call(s) can be cached for performance (would result in equal timestamps)
			wrappedFields := logrus.Fields{
				"level":     level,
				"clientID":  clientID,
				"component": append(ln.component(), component...),
				"fields":    fields,
			}
			node.logrusLogger().WithTime(now).WithFields(wrappedFields).Log(logrusLevel(level), fmt.Sprintf(format, Map(args, evalSupplierR)...))
		}
	}
}

func (ln *LogNode) Fatalf(component ...string) func(*pdnsClientID, string, ...any) func(...any) {
	return ln.Logf(FatalLevel, component...)
}

func (ln *LogNode) Errorf(component ...string) func(*pdnsClientID, string, ...any) func(...any) {
	return ln.Logf(ErrorLevel, component...)
}

func (ln *LogNode) Warnf(component ...string) func(*pdnsClientID, string, ...any) func(...any) {
	return ln.Logf(WarningLevel, component...)
}

func (ln *LogNode) Importantf(component ...string) func(*pdnsClientID, string, ...any) func(...any) {
	return ln.Logf(ImportantLevel, component...)
}

func (ln *LogNode) Infof(component ...string) func(*pdnsClientID, string, ...any) func(...any) {
	return ln.Logf(0, component...)
}

//goland:noinspection GoUnhandledErrorResult
func (ln *LogNode) Format(entry *logrus.Entry) ([]byte, error) {
	var msg strings.Builder
	if !standalone {
		fmt.Fprintf(&msg, "pdns-etcd3[%d] ", pid)
	}
	fmt.Fprintf(&msg, "[%s]", entry.Time.Format(time.StampMicro))
	if clientID := entry.Data["clientID"].(*pdnsClientID); clientID != nil {
		fmt.Fprintf(&msg, " [%s]", *clientID)
	}
	fmt.Fprintf(&msg, " %s", levelChars(entry.Data["level"].(int)))
	if component := entry.Data["component"].([]string); len(component) > 0 {
		fmt.Fprintf(&msg, " %s", strings.Join(component, "."))
	}
	fmt.Fprintf(&msg, ": %s", entry.Message)
	if fields, ok := entry.Data["fields"]; ok {
		if fieldsSlice, ok := fields.([]any); ok && len(fieldsSlice) > 0 {
			fmt.Fprint(&msg, " |")
			var k *string
			for _, field := range fieldsSlice {
				field = evalSupplierR(field, -1)
				if k != nil {
					fmt.Fprintf(&msg, " %s=%s", *k, val2str(field))
					k = nil
				} else if s, ok := field.(string); ok {
					k = &s
				} else {
					fmt.Fprintf(&msg, " %s", val2str(field))
				}
			}
			if k != nil {
				fmt.Fprintf(&msg, " %q", *k)
			}
		} else if !ok { // need to check on ok again, because we could have jumped here when no fields were given
			fmt.Fprintf(&msg, " |? %s", val2str(fields))
		}
	}
	fmt.Fprint(&msg, "\n")
	return []byte(msg.String()), nil
}
