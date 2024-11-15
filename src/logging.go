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

package src

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

type logFormatter struct {
	component string
}

var logLevelChars = map[logrus.Level]string{
	logrus.PanicLevel: "PNC",
	logrus.FatalLevel: "FTL",
	logrus.ErrorLevel: "ERR",
	logrus.WarnLevel:  "WRN",
	logrus.InfoLevel:  "INF",
	logrus.DebugLevel: "DBG",
	logrus.TraceLevel: "TRC",
}

func (f *logFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	var fmt1 string
	var arg1 any
	if standalone {
		fmt1 = "[%s]"
		arg1 = time.Now().Format(time.StampMilli)
	} else {
		fmt1 = "pdns-etcd3[%d]"
		arg1 = pid
	}
	str := fmt.Sprintf(fmt1+" %-4s %s: %s", arg1, f.component, logLevelChars[entry.Level], entry.Message)
	if len(entry.Data) > 0 {
		str += " |"
	}
	for k, v := range entry.Data {
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				str += fmt.Sprintf(" *%s=<nil>", k)
			} else {
				str += fmt.Sprintf(" *%s=%+v", k, rv.Elem())
			}
		} else if rv.Kind() == reflect.String {
			str += fmt.Sprintf(" %s=%q", k, v)
		} else {
			str += fmt.Sprintf(" %s=%+v", k, v)
		}
	}
	str += "\n"
	return []byte(str), nil
}

var log struct {
	main *logrus.Logger
	pdns *logrus.Logger
	etcd *logrus.Logger
	data *logrus.Logger
	// TODO time logger (print durations)
}
var loggers = map[string]*logrus.Logger{}

func initLogging() {
	log.main = logrus.New()
	log.pdns = logrus.New()
	log.etcd = logrus.New()
	log.data = logrus.New()
	loggers["main"] = log.main
	loggers["pdns"] = log.pdns
	loggers["etcd"] = log.etcd
	loggers["data"] = log.data
	for component, logger := range loggers {
		logger.SetFormatter(&logFormatter{component: component})
	}
}

func setLoggingLevel(components string, level logrus.Level) {
	for _, component := range strings.Split(components, "+") {
		if logger, ok := loggers[component]; ok {
			log.main.Printf("Setting log level of %s to %s", component, level)
			logger.SetLevel(level)
		} else {
			log.main.WithFields(logrus.Fields{"component": component, "level": level}).Warn("setLoggingLevel(): invalid component")
		}
	}
}

func logFrom(logger *logrus.Logger, fieldsArgs ...any) *logrus.Entry {
	fields := logrus.Fields{}
	var name string
	for i, v := range fieldsArgs {
		if i%2 == 0 {
			if v, ok := v.(string); ok {
				name = v
			} else {
				name = fmt.Sprintf("%d", (i/2)+1)
			}
		} else {
			fields[name] = v
		}
	}
	return logger.WithFields(fields)
}
